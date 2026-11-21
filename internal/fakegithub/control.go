package fakegithub

import (
	"cmp"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"time"

	gh "github.com/google/go-github/v71/github"
)

// Defaults of the control calls.
const (
	defaultBase       = "main"
	defaultAuthor     = "mock-user"
	defaultFileStatus = "modified"
	defaultPushRef    = "refs/heads/main"
	forkOwner         = "mock-fork"
)

// controlResponse is the answer of a mutating control call: the resulting
// pull request, when the call is about one, and the webhook's outcome.
type controlResponse struct {
	PullRequest *gh.PullRequest `json:"pull_request,omitempty"`
	Webhook     delivery        `json:"webhook"`
}

// controlError is a control call refused with status and message.
type controlError struct {
	status int
	msg    string
}

// routeControl registers the control API under /_mock/; any other path 404s.
func (s *Server) routeControl(mux *http.ServeMux) {
	const pull = "/_mock/repos/{owner}/{repo}/pulls/{number}"
	mux.HandleFunc("POST /_mock/repos/{owner}/{repo}/pulls", s.openPull)
	mux.HandleFunc("POST "+pull+"/labels", s.addLabel)
	mux.HandleFunc("DELETE "+pull+"/labels/{name}", s.removeLabel)
	mux.HandleFunc("POST "+pull+"/push", s.pushPull)
	mux.HandleFunc("POST "+pull+"/close", s.closePull)
	mux.HandleFunc("POST "+pull+"/reopen", s.reopenPull)
	mux.HandleFunc("GET "+pull+"/comments", s.readComments)
	mux.HandleFunc("POST /_mock/repos/{owner}/{repo}/push", s.pushRepo)
	mux.HandleFunc("GET /_mock/stats", s.stats)
	mux.HandleFunc("POST /_mock/reset", s.reset)
	mux.HandleFunc("/_mock/", func(w http.ResponseWriter, _ *http.Request) { notFound(w) })
}

// openRequest is the body of the open call.
type openRequest struct {
	Number  *int          `json:"number"`
	HeadRef string        `json:"head_ref"`
	HeadSHA string        `json:"head_sha"`
	Base    string        `json:"base"`
	Author  string        `json:"author"`
	Labels  []string      `json:"labels"`
	Fork    bool          `json:"fork"`
	Files   []changedFile `json:"files"`
}

// openPull opens a pull request and sends pull_request opened.
func (s *Server) openPull(w http.ResponseWriter, r *http.Request) {
	var in openRequest
	if err := decode(r, &in, false); err != nil {
		writeMessage(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.HeadRef == "" || in.HeadSHA == "" {
		writeMessage(w, http.StatusBadRequest, "head_ref and head_sha are required")
		return
	}
	files, cerr := normalizeFiles(in.Files)
	if cerr != nil {
		writeMessage(w, cerr.status, cerr.msg)
		return
	}
	owner, name := r.PathValue("owner"), r.PathValue("repo")
	s.mu.Lock()
	repo := s.repo(owner, name)
	number, cerr := nextNumber(repo, in.Number)
	if cerr != nil {
		s.mu.Unlock()
		writeMessage(w, cerr.status, cerr.msg)
		return
	}
	now := time.Now()
	pr := &pullRequest{
		id:       s.newID(),
		number:   number,
		open:     true,
		headRef:  in.HeadRef,
		headSHA:  in.HeadSHA,
		headRepo: repo.owner + "/" + repo.name,
		baseRef:  cmp.Or(in.Base, defaultBase),
		author:   cmp.Or(in.Author, defaultAuthor),
		labels:   slices.Clone(in.Labels),
		files:    files,
		created:  now,
		updated:  now,
	}
	if in.Fork {
		pr.headRepo = forkOwner + "/" + repo.name
	}
	repo.pulls[number] = pr
	out, ev := s.pullJSON(repo, pr), s.pullEvent(repo, pr, "opened", "")
	s.mu.Unlock()
	writeJSON(
		w,
		http.StatusCreated,
		controlResponse{PullRequest: out, Webhook: s.send(r.Context(), "pull_request", "opened", ev)},
	)
}

// nextNumber returns the number of a new pull request of repo: want, which
// must be positive and free, or the next free number. s.mu must be held.
func nextNumber(repo *repository, want *int) (int, *controlError) {
	if want != nil {
		if *want < 1 {
			return 0, &controlError{http.StatusBadRequest, "number must be positive"}
		}
		if repo.pulls[*want] != nil {
			return 0, &controlError{http.StatusConflict, "pull request " + strconv.Itoa(*want) + " exists"}
		}
		return *want, nil
	}
	n := 1
	for existing := range repo.pulls {
		n = max(n, existing+1)
	}
	return n, nil
}

// normalizeFiles checks that every file has a filename and defaults its
// status. nil stays nil.
func normalizeFiles(files []changedFile) ([]changedFile, *controlError) {
	if files == nil {
		return nil, nil
	}
	out := make([]changedFile, len(files))
	for i, f := range files {
		if f.Filename == "" {
			return nil, &controlError{http.StatusBadRequest, "every file needs a filename"}
		}
		f.Status = cmp.Or(f.Status, defaultFileStatus)
		out[i] = f
	}
	return out, nil
}

// labelRequest is the body of the label call.
type labelRequest struct {
	Name string `json:"name"`
}

// addLabel adds a label and sends pull_request labeled. A label the pull
// request has is not added twice, and the webhook is still sent.
func (s *Server) addLabel(w http.ResponseWriter, r *http.Request) {
	var in labelRequest
	if err := decode(r, &in, false); err != nil || in.Name == "" {
		writeMessage(w, http.StatusBadRequest, "name is required")
		return
	}
	s.change(w, r, "labeled", in.Name, func(pr *pullRequest) *controlError {
		if !slices.Contains(pr.labels, in.Name) {
			pr.labels = append(pr.labels, in.Name)
		}
		return nil
	})
}

// removeLabel removes a label and sends pull_request unlabeled; a label the
// pull request does not have 404s.
func (s *Server) removeLabel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.change(w, r, "unlabeled", name, func(pr *pullRequest) *controlError {
		i := slices.Index(pr.labels, name)
		if i < 0 {
			return &controlError{http.StatusNotFound, "Label does not exist"}
		}
		pr.labels = slices.Delete(pr.labels, i, i+1)
		return nil
	})
}

// pushRequest is the body of the pull request push call.
type pushRequest struct {
	HeadSHA string        `json:"head_sha"`
	Files   []changedFile `json:"files"`
}

// pushPull sets a new head SHA, and the files when given, and sends
// pull_request synchronize.
func (s *Server) pushPull(w http.ResponseWriter, r *http.Request) {
	var in pushRequest
	if err := decode(r, &in, false); err != nil || in.HeadSHA == "" {
		writeMessage(w, http.StatusBadRequest, "head_sha is required")
		return
	}
	files, cerr := normalizeFiles(in.Files)
	if cerr != nil {
		writeMessage(w, cerr.status, cerr.msg)
		return
	}
	s.change(w, r, "synchronize", "", func(pr *pullRequest) *controlError {
		pr.headSHA = in.HeadSHA
		if files != nil {
			pr.files = files
		}
		return nil
	})
}

// closeRequest is the body of the close call.
type closeRequest struct {
	Merged bool `json:"merged"`
}

// closePull closes the pull request, merged or not, and sends pull_request
// closed.
func (s *Server) closePull(w http.ResponseWriter, r *http.Request) {
	var in closeRequest
	if err := decode(r, &in, true); err != nil {
		writeMessage(w, http.StatusBadRequest, err.Error())
		return
	}
	s.change(w, r, "closed", "", func(pr *pullRequest) *controlError {
		pr.open, pr.merged = false, in.Merged
		return nil
	})
}

// reopenPull reopens the pull request and sends pull_request reopened.
func (s *Server) reopenPull(w http.ResponseWriter, r *http.Request) {
	s.change(w, r, "reopened", "", func(pr *pullRequest) *controlError {
		pr.open, pr.merged = true, false
		return nil
	})
}

// change applies fn to the request's pull request under s.mu, then sends
// pull_request action (with label on labeled and unlabeled) and answers with
// the pull request and the webhook's outcome. An unknown pull request 404s.
func (s *Server) change(
	w http.ResponseWriter,
	r *http.Request,
	action, label string,
	fn func(*pullRequest) *controlError,
) {
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		notFound(w)
		return
	}
	s.mu.Lock()
	repo, pr := s.pull(r.PathValue("owner"), r.PathValue("repo"), number)
	if pr == nil {
		s.mu.Unlock()
		notFound(w)
		return
	}
	if cerr := fn(pr); cerr != nil {
		s.mu.Unlock()
		writeMessage(w, cerr.status, cerr.msg)
		return
	}
	pr.updated = time.Now()
	out, ev := s.pullJSON(repo, pr), s.pullEvent(repo, pr, action, label)
	s.mu.Unlock()
	writeJSON(
		w,
		http.StatusOK,
		controlResponse{PullRequest: out, Webhook: s.send(r.Context(), "pull_request", action, ev)},
	)
}

// pullEvent is the pull_request webhook of action on pr, with label when
// set. s.mu must be held.
func (s *Server) pullEvent(repo *repository, pr *pullRequest, action, label string) *gh.PullRequestEvent {
	ev := &gh.PullRequestEvent{
		Action:       gh.Ptr(action),
		Number:       gh.Ptr(pr.number),
		PullRequest:  s.pullJSON(repo, pr),
		Repo:         s.repoJSON(repo.owner, repo.name),
		Sender:       &gh.User{Login: gh.Ptr(pr.author)},
		Installation: &gh.Installation{ID: gh.Ptr(s.cfg.InstallationID)},
	}
	if label != "" {
		ev.Label = &gh.Label{Name: gh.Ptr(label)}
	}
	return ev
}

// repoPushRequest is the body of the repository push call.
type repoPushRequest struct {
	Ref     string `json:"ref"`
	After   string `json:"after"`
	Deleted bool   `json:"deleted"`
}

// pushRepo sends a push webhook of the repository.
func (s *Server) pushRepo(w http.ResponseWriter, r *http.Request) {
	var in repoPushRequest
	if err := decode(r, &in, false); err != nil || in.After == "" {
		writeMessage(w, http.StatusBadRequest, "after is required")
		return
	}
	s.mu.Lock()
	repo := s.repo(r.PathValue("owner"), r.PathValue("repo"))
	full := s.repoJSON(repo.owner, repo.name)
	s.mu.Unlock()
	ev := &gh.PushEvent{
		Ref:     gh.Ptr(cmp.Or(in.Ref, defaultPushRef)),
		After:   gh.Ptr(in.After),
		Deleted: gh.Ptr(in.Deleted),
		Repo: &gh.PushEventRepository{
			Name:          full.Name,
			FullName:      full.FullName,
			Owner:         full.Owner,
			HTMLURL:       full.HTMLURL,
			CloneURL:      full.CloneURL,
			DefaultBranch: full.DefaultBranch,
		},
		Sender:       &gh.User{Login: gh.Ptr(defaultAuthor)},
		Installation: &gh.Installation{ID: gh.Ptr(s.cfg.InstallationID)},
	}
	writeJSON(w, http.StatusOK, controlResponse{Webhook: s.send(r.Context(), "push", "", ev)})
}

// readComments answers the comments of the pull request, in order.
func (s *Server) readComments(w http.ResponseWriter, r *http.Request) {
	s.withPull(w, r, func(_ *repository, pr *pullRequest) {
		writeJSON(w, http.StatusOK, s.commentsJSON(pr))
	})
}

// stats answers the API calls per route since the start or the last reset.
func (s *Server) stats(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	calls := maps.Clone(s.calls)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, calls)
}

// reset clears the repositories, pull requests, comments and API call
// counters.
func (s *Server) reset(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.resetLocked()
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// decode reads the request's JSON body into v, refusing unknown fields. An
// empty body is accepted when optional.
func decode(r *http.Request, v any, optional bool) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if optional && errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
