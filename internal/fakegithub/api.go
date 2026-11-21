package fakegithub

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v71/github"
)

// apiPrefix is the API base path, the one go-github uses for a custom API base.
const apiPrefix = "/api/v3"

// Paging of the list routes, as GitHub does it.
const (
	defaultPerPage = 30
	maxPerPage     = 100
)

// appOwner is the login of the App's owner and of its installation's account.
const appOwner = "badil"

// tokenBytes is the number of random bytes of an installation token.
const tokenBytes = 20

// routeAPI registers the API routes under apiPrefix. Each route counts its
// calls under its pattern, before auth; any other path 404s.
func (s *Server) routeAPI(mux *http.ServeMux) {
	routes := []struct {
		pattern string
		auth    func(http.HandlerFunc) http.HandlerFunc
		handler http.HandlerFunc
	}{
		{"POST /app/installations/{id}/access_tokens", s.requireJWT, s.createToken},
		{"GET /app", s.requireJWT, s.getApp},
		{"GET /app/installations", s.requireJWT, s.listInstallations},
		{"GET /users/{login}", s.requireToken, s.getUser},
		{"GET /repos/{owner}/{repo}/pulls", s.requireToken, s.listPulls},
		{"GET /repos/{owner}/{repo}/pulls/{number}", s.requireToken, s.getPull},
		{"GET /repos/{owner}/{repo}/pulls/{number}/files", s.requireToken, s.listFiles},
		{"GET /repos/{owner}/{repo}/issues/{number}/comments", s.requireToken, s.listComments},
		{"POST /repos/{owner}/{repo}/issues/{number}/comments", s.requireToken, s.createComment},
		{"PATCH /repos/{owner}/{repo}/issues/comments/{id}", s.requireToken, s.editComment},
	}
	for _, rt := range routes {
		method, path, _ := strings.Cut(rt.pattern, " ")
		h := rt.auth(rt.handler)
		mux.HandleFunc(method+" "+apiPrefix+path, func(w http.ResponseWriter, r *http.Request) {
			s.count(rt.pattern)
			h(w, r)
		})
	}
	mux.HandleFunc(apiPrefix+"/", func(w http.ResponseWriter, r *http.Request) {
		route := r.Method + " " + strings.TrimPrefix(r.URL.Path, apiPrefix)
		s.count(route)
		slog.WarnContext(r.Context(), "unknown API route", slog.String("route", route))
		notFound(w)
	})
}

// count records one call of route.
func (s *Server) count(route string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[route]++
}

// requireJWT lets a request through when it carries a Bearer token, the App
// JWT, which is not verified.
func (s *Server) requireJWT(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); !ok || t == "" {
			badCredentials(w)
			return
		}
		next(w, r)
	}
}

// requireToken lets a request through when it carries, as "token <t>" or
// "Bearer <t>", an installation token the mock issued that has not expired.
func (s *Server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		t, ok := strings.CutPrefix(auth, "token ")
		if !ok {
			t, _ = strings.CutPrefix(auth, "Bearer ")
		}
		s.mu.Lock()
		expiry, issued := s.tokens[t]
		s.mu.Unlock()
		if t == "" || !issued || !time.Now().Before(expiry) {
			badCredentials(w)
			return
		}
		next(w, r)
	}
}

// createToken issues an installation token of the configured installation.
func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id != s.cfg.InstallationID {
		notFound(w)
		return
	}
	b := make([]byte, tokenBytes)
	_, _ = rand.Read(b)
	token := "ghs_" + hex.EncodeToString(b)
	expiry := time.Now().Add(s.cfg.TokenTTL)
	s.mu.Lock()
	s.tokens[token] = expiry
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, &gh.InstallationToken{
		Token:     gh.Ptr(token),
		ExpiresAt: &gh.Timestamp{Time: expiry.UTC()},
	})
}

// getApp answers GET /app.
func (s *Server) getApp(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, &gh.App{
		ID:      gh.Ptr(int64(1)),
		Slug:    gh.Ptr(s.cfg.AppSlug),
		Name:    gh.Ptr(s.cfg.AppSlug),
		Owner:   &gh.User{Login: gh.Ptr(appOwner)},
		HTMLURL: gh.Ptr(s.cfg.WebURL + "/apps/" + s.cfg.AppSlug),
	})
}

// listInstallations answers GET /app/installations: the configured
// installation.
func (s *Server) listInstallations(w http.ResponseWriter, r *http.Request) {
	writePage(w, r, []*gh.Installation{{
		ID:      gh.Ptr(s.cfg.InstallationID),
		Account: &gh.User{Login: gh.Ptr(appOwner), Type: gh.Ptr("Organization")},
	}})
}

// getUser answers GET /users/{login} for the App's bot only.
func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("login") != s.botLogin() {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, s.botUser())
}

// listPulls answers GET /repos/{owner}/{repo}/pulls: the pull requests in
// state (default open, or closed, or all), by number.
func (s *Server) listPulls(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	s.mu.Lock()
	repo := s.repo(r.PathValue("owner"), r.PathValue("repo"))
	numbers := make([]int, 0, len(repo.pulls))
	for n := range repo.pulls {
		numbers = append(numbers, n)
	}
	slices.Sort(numbers)
	var out []*gh.PullRequest
	for _, n := range numbers {
		pr := repo.pulls[n]
		if state == "all" || (state == "closed") != pr.open {
			out = append(out, s.pullJSON(repo, pr))
		}
	}
	s.mu.Unlock()
	writePage(w, r, out)
}

// getPull answers GET /repos/{owner}/{repo}/pulls/{number}.
func (s *Server) getPull(w http.ResponseWriter, r *http.Request) {
	s.withPull(w, r, func(repo *repository, pr *pullRequest) {
		writeJSON(w, http.StatusOK, s.pullJSON(repo, pr))
	})
}

// listFiles answers GET /repos/{owner}/{repo}/pulls/{number}/files.
func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	s.withPull(w, r, func(_ *repository, pr *pullRequest) {
		files := make([]*gh.CommitFile, 0, len(pr.files))
		for _, f := range pr.files {
			files = append(files, &gh.CommitFile{
				Filename:         gh.Ptr(f.Filename),
				Status:           gh.Ptr(f.Status),
				PreviousFilename: optional(f.PreviousFilename),
			})
		}
		writePage(w, r, files)
	})
}

// listComments answers GET /repos/{owner}/{repo}/issues/{number}/comments.
func (s *Server) listComments(w http.ResponseWriter, r *http.Request) {
	s.withPull(w, r, func(_ *repository, pr *pullRequest) {
		writePage(w, r, s.commentsJSON(pr))
	})
}

// commentBody is the body of a comment create or edit.
type commentBody struct {
	Body *string `json:"body"`
}

// createComment answers POST /repos/{owner}/{repo}/issues/{number}/comments.
func (s *Server) createComment(w http.ResponseWriter, r *http.Request) {
	var in commentBody
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Body == nil {
		writeMessage(w, http.StatusBadRequest, "Problems parsing JSON")
		return
	}
	s.withPull(w, r, func(_ *repository, pr *pullRequest) {
		now := time.Now()
		c := &comment{id: s.newID(), body: *in.Body, created: now, updated: now}
		pr.comments = append(pr.comments, c)
		writeJSON(w, http.StatusCreated, s.commentJSON(c))
	})
}

// editComment answers PATCH /repos/{owner}/{repo}/issues/comments/{id}.
func (s *Server) editComment(w http.ResponseWriter, r *http.Request) {
	var in commentBody
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Body == nil {
		writeMessage(w, http.StatusBadRequest, "Problems parsing JSON")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		notFound(w)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pr := range s.repo(r.PathValue("owner"), r.PathValue("repo")).pulls {
		for _, c := range pr.comments {
			if c.id == id {
				c.body, c.updated = *in.Body, time.Now()
				writeJSON(w, http.StatusOK, s.commentJSON(c))
				return
			}
		}
	}
	notFound(w)
}

// withPull runs fn with s.mu held on the pull request of the request's
// owner, repo and number, or answers 404 when it does not exist.
func (s *Server) withPull(w http.ResponseWriter, r *http.Request, fn func(*repository, *pullRequest)) {
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		notFound(w)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, pr := s.pull(r.PathValue("owner"), r.PathValue("repo"), number)
	if pr == nil {
		notFound(w)
		return
	}
	fn(repo, pr)
}

// botLogin is the login of the App's bot user.
func (s *Server) botLogin() string {
	return s.cfg.AppSlug + "[bot]"
}

// botUser is the App's bot user.
func (s *Server) botUser() *gh.User {
	return &gh.User{Login: gh.Ptr(s.botLogin()), ID: gh.Ptr(s.cfg.BotUserID), Type: gh.Ptr("Bot")}
}

// repoJSON is repo as the API and webhooks return it.
func (s *Server) repoJSON(owner, name string) *gh.Repository {
	web := s.cfg.WebURL + "/" + owner + "/" + name
	return &gh.Repository{
		Name:          gh.Ptr(name),
		FullName:      gh.Ptr(owner + "/" + name),
		Owner:         &gh.User{Login: gh.Ptr(owner)},
		HTMLURL:       gh.Ptr(web),
		CloneURL:      gh.Ptr(web + ".git"),
		DefaultBranch: gh.Ptr("main"),
	}
}

// pullJSON is pr of repo as the API returns it. s.mu must be held.
func (s *Server) pullJSON(repo *repository, pr *pullRequest) *gh.PullRequest {
	state := "closed"
	if pr.open {
		state = "open"
	}
	labels := make([]*gh.Label, 0, len(pr.labels))
	for _, l := range pr.labels {
		labels = append(labels, &gh.Label{Name: gh.Ptr(l)})
	}
	headOwner, headName, _ := strings.Cut(pr.headRepo, "/")
	return &gh.PullRequest{
		ID:           gh.Ptr(pr.id),
		Number:       gh.Ptr(pr.number),
		State:        gh.Ptr(state),
		Merged:       gh.Ptr(pr.merged),
		ChangedFiles: gh.Ptr(len(pr.files)),
		HTMLURL:      gh.Ptr(s.cfg.WebURL + "/" + repo.owner + "/" + repo.name + "/pull/" + strconv.Itoa(pr.number)),
		User:         &gh.User{Login: gh.Ptr(pr.author)},
		Labels:       labels,
		Head: &gh.PullRequestBranch{
			Ref:  gh.Ptr(pr.headRef),
			SHA:  gh.Ptr(pr.headSHA),
			Repo: s.repoJSON(headOwner, headName),
		},
		Base: &gh.PullRequestBranch{
			Ref:  gh.Ptr(pr.baseRef),
			Repo: s.repoJSON(repo.owner, repo.name),
		},
		CreatedAt: &gh.Timestamp{Time: pr.created.UTC()},
		UpdatedAt: &gh.Timestamp{Time: pr.updated.UTC()},
	}
}

// commentJSON is c as the API returns it, posted by the App's bot.
func (s *Server) commentJSON(c *comment) *gh.IssueComment {
	return &gh.IssueComment{
		ID:        gh.Ptr(c.id),
		Body:      gh.Ptr(c.body),
		User:      s.botUser(),
		CreatedAt: &gh.Timestamp{Time: c.created.UTC()},
		UpdatedAt: &gh.Timestamp{Time: c.updated.UTC()},
	}
}

// commentsJSON is the comments of pr, in order. s.mu must be held.
func (s *Server) commentsJSON(pr *pullRequest) []*gh.IssueComment {
	out := make([]*gh.IssueComment, 0, len(pr.comments))
	for _, c := range pr.comments {
		out = append(out, s.commentJSON(c))
	}
	return out
}

// writePage writes the page of items the request's per_page (default 30, at
// most 100) and page (default 1) select, with a Link header to the next page
// when more remain.
func writePage[T any](w http.ResponseWriter, r *http.Request, items []T) {
	q := r.URL.Query()
	perPage, err := strconv.Atoi(q.Get("per_page"))
	if err != nil || perPage < 1 {
		perPage = defaultPerPage
	}
	perPage = min(perPage, maxPerPage)
	page, err := strconv.Atoi(q.Get("page"))
	if err != nil || page < 1 {
		page = 1
	}
	start := min((page-1)*perPage, len(items))
	end := min(start+perPage, len(items))
	if end < len(items) {
		q.Set("page", strconv.Itoa(page+1))
		q.Set("per_page", strconv.Itoa(perPage))
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		next := url.URL{Scheme: scheme, Host: r.Host, Path: r.URL.Path, RawQuery: q.Encode()}
		w.Header().Set("Link", "<"+next.String()+`>; rel="next"`)
	}
	out := items[start:end]
	if out == nil {
		out = []T{}
	}
	writeJSON(w, http.StatusOK, out)
}

// optional is s as a pointer, nil when s is empty.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// writeJSON writes v as JSON with status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeMessage writes GitHub's error body {"message": msg} with status.
func writeMessage(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"message": msg})
}

// notFound writes GitHub's 404.
func notFound(w http.ResponseWriter) {
	writeMessage(w, http.StatusNotFound, "Not Found")
}

// badCredentials writes GitHub's 401 for a missing or bad token.
func badCredentials(w http.ResponseWriter) {
	writeMessage(w, http.StatusUnauthorized, "Bad credentials")
}
