package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/badilhq/badil/internal/store"
)

// postSync posts body to /v1/sync with bearer, none when "".
func postSync(t *testing.T, s *Server, body, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/sync", strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// decodeSync decodes a 200 /v1/sync answer.
func decodeSync(t *testing.T, rec *httptest.ResponseRecorder) SyncResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var resp SyncResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return resp
}

func TestSync_RequiresToken(t *testing.T) {
	fg := syncFake()
	s, _, _ := newPushServer(t, fg)
	for _, bearer := range []string{"", "wrong"} {
		if rec := postSync(t, s, `{}`, bearer); rec.Code != http.StatusUnauthorized {
			t.Errorf("bearer %q: status = %d, want 401", bearer, rec.Code)
		}
	}
	if got := fg.synced(); len(got) != 0 {
		t.Errorf("synced = %v, want none", got)
	}
}

func TestSync_All(t *testing.T) {
	for _, body := range []string{"", "{}", `{"slug":null}`} {
		t.Run(body, func(t *testing.T) {
			fg := syncFake()
			fg.listed = []string{"a", "b", "c"}
			fg.previews = previewFiles("a", "b", "c")
			fg.syncResult = map[string]*store.SyncResult{"a": {Changed: true}}
			fg.syncErr = map[string]error{"c": errors.New("sync boom")}
			s, m, _ := newPushServer(t, fg)
			resp := decodeSync(t, postSync(t, s, body, "t"))
			if !slices.Equal(resp.Synced, []string{"a"}) || resp.Unchanged != 1 ||
				!slices.Equal(resp.Failed, []string{"c"}) {
				t.Errorf("response = %+v, want a synced, 1 unchanged, c failed", resp)
			}
			assertCounter(t, m, "badil_previews_synced_total", " 1")
		})
	}
}

func TestSync_EmptyListsAreArrays(t *testing.T) {
	s, _, _ := newPushServer(t, syncFake())
	rec := postSync(t, s, `{}`, "t")
	if got := strings.TrimSpace(rec.Body.String()); got != `{"synced":[],"unchanged":2,"failed":[]}` {
		t.Errorf("body = %s", got)
	}
}

func TestSync_OneSlug(t *testing.T) {
	fg := syncFake()
	fg.syncResult = map[string]*store.SyncResult{"b": {Changed: true}}
	s, m, _ := newPushServer(t, fg)
	resp := decodeSync(t, postSync(t, s, `{"slug":"b"}`, "t"))
	if !slices.Equal(resp.Synced, []string{"b"}) || resp.Unchanged != 0 || len(resp.Failed) != 0 {
		t.Errorf("response = %+v, want b synced", resp)
	}
	if got := fg.synced(); !slices.Equal(got, []string{"b"}) {
		t.Errorf("synced = %v, want only b", got)
	}
	assertCounter(t, m, "badil_previews_synced_total", " 1")
}

func TestSync_Refused(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		wantErr    string
		catalogErr error
		listErr    error
	}{
		{name: "path slug", body: `{"slug":"../x"}`, status: http.StatusBadRequest, wantErr: "invalid slug"},
		{name: "empty slug", body: `{"slug":""}`, status: http.StatusBadRequest, wantErr: "invalid slug"},
		{name: "branch name", body: `{"slug":"Feature/X"}`, status: http.StatusBadRequest, wantErr: "invalid slug"},
		{name: "unknown field", body: `{"slugs":["a"]}`, status: http.StatusBadRequest, wantErr: "invalid body: "},
		{name: "malformed", body: `{"slug":`, status: http.StatusBadRequest, wantErr: "invalid body: "},
		{
			name: "unknown slug", body: `{"slug":"zz"}`, status: http.StatusNotFound,
			wantErr: "preview zz not found",
		},
		{
			name: "badil.yaml unreadable", body: `{}`, status: http.StatusServiceUnavailable,
			wantErr: "badil.yaml unavailable: catalog boom", catalogErr: errors.New("catalog boom"),
		},
		{
			name: "gitops unauthorized", body: `{}`, status: http.StatusBadGateway,
			wantErr: "gitops: unauthorized", listErr: store.ErrUnauthorized,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fg := syncFake()
			fg.catalogErr, fg.listErr = tc.catalogErr, tc.listErr
			fg.started = make(chan string, 4)
			s, _, _ := newPushServer(t, fg)
			rec := postSync(t, s, tc.body, "t")
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.status, rec.Body)
			}
			var resp ErrorResponse
			if err := json.Unmarshal(
				rec.Body.Bytes(),
				&resp,
			); err != nil ||
				!strings.HasPrefix(resp.Error, tc.wantErr) {
				t.Errorf("body = %s, want error %q", rec.Body, tc.wantErr)
			}
			if got := fg.synced(); len(got) != 0 {
				t.Errorf("synced = %v, want none", got)
			}
			if tc.status == http.StatusBadRequest {
				if got := drain(fg.started); len(got) != 0 {
					t.Errorf("gitops reads = %v, want none before the slug is valid", got)
				}
			}
		})
	}
}
