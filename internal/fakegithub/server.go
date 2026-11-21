// Package fakegithub is a mock of the GitHub REST API badil calls, served
// under /api/v3/ from in-memory state, with a control API under /_mock/ that
// changes the state and sends the matching signed webhook to badil. It is a
// dev and test tool, not part of badil's release.
package fakegithub

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// defaultTokenTTL is the lifetime of issued installation tokens when
// Config.TokenTTL is zero.
const defaultTokenTTL = time.Hour

// webhookTimeout bounds one webhook delivery.
const webhookTimeout = 30 * time.Second

// Config configures a Server.
type Config struct {
	// WebURL is the web base of every html_url and repo URL, without a
	// trailing "/" (badil's GITHUB_URL).
	WebURL string
	// WebhookURL is where webhooks are sent: badil's .../webhooks/github.
	WebhookURL string
	// WebhookSecret signs every webhook (X-Hub-Signature-256).
	WebhookSecret string
	// InstallationID is the only installation the mock knows; every webhook
	// carries it.
	InstallationID int64
	// AppSlug is the App's slug; its bot user is <AppSlug>[bot].
	AppSlug string
	// BotUserID is the user id of the App's bot.
	BotUserID int64
	// TokenTTL is the lifetime of issued installation tokens, defaultTokenTTL
	// when zero.
	TokenTTL time.Duration
}

// Server is the mock. Its state lives in memory, guarded by mu.
type Server struct {
	cfg     Config
	webhook *http.Client
	handler http.Handler

	mu sync.Mutex
	// repos are the repositories by lowercased owner/repo.
	repos map[string]*repository
	// tokens are the issued installation tokens and their expiry.
	tokens map[string]time.Time
	// calls counts the API calls per route.
	calls map[string]int
	// nextID is the last id given to a pull request or a comment.
	nextID int64
}

// repository is a repository of the mock, created on first use.
type repository struct {
	owner, name string
	pulls       map[int]*pullRequest
}

// pullRequest is a pull request and its comments.
type pullRequest struct {
	id       int64
	number   int
	open     bool
	merged   bool
	headRef  string
	headSHA  string
	headRepo string
	baseRef  string
	author   string
	labels   []string
	files    []changedFile
	comments []*comment
	created  time.Time
	updated  time.Time
}

// changedFile is a file a pull request changes.
type changedFile struct {
	Filename         string `json:"filename"`
	Status           string `json:"status,omitempty"`
	PreviousFilename string `json:"previous_filename,omitempty"`
}

// comment is an issue comment on a pull request.
type comment struct {
	id      int64
	body    string
	created time.Time
	updated time.Time
}

// New returns a mock configured by cfg, with no repositories.
func New(cfg Config) *Server {
	if cfg.TokenTTL == 0 {
		cfg.TokenTTL = defaultTokenTTL
	}
	cfg.WebURL = strings.TrimSuffix(cfg.WebURL, "/")
	s := &Server{
		cfg:     cfg,
		webhook: &http.Client{Timeout: webhookTimeout},
		tokens:  map[string]time.Time{},
	}
	s.resetLocked()
	mux := http.NewServeMux()
	s.routeAPI(mux)
	s.routeControl(mux)
	s.handler = mux
	return s
}

// Handler returns the mock's HTTP handler: the API under /api/v3/ and the
// control API under /_mock/.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// resetLocked clears the repositories and the API call counters. Issued
// tokens are kept, so a running badil keeps its cached installation token.
func (s *Server) resetLocked() {
	s.repos = map[string]*repository{}
	s.calls = map[string]int{}
}

// repo returns owner/name, created when missing. s.mu must be held.
func (s *Server) repo(owner, name string) *repository {
	key := strings.ToLower(owner + "/" + name)
	r, ok := s.repos[key]
	if !ok {
		r = &repository{owner: owner, name: name, pulls: map[int]*pullRequest{}}
		s.repos[key] = r
	}
	return r
}

// pull returns owner/name#number, or nil when it does not exist. s.mu must
// be held.
func (s *Server) pull(owner, name string, number int) (*repository, *pullRequest) {
	r := s.repo(owner, name)
	return r, r.pulls[number]
}

// newID returns a new pull request or comment id. s.mu must be held.
func (s *Server) newID() int64 {
	s.nextID++
	return s.nextID
}
