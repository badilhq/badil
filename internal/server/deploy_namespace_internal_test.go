package server

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/badilhq/badil/internal/app"
)

// namespaceChecker is a lifecycle.NamespaceChecker: every namespace is foreign
// when foreign is set, and every check fails with err when it is set.
type namespaceChecker struct {
	foreign bool
	err     error
}

func (c namespaceChecker) Foreign(context.Context, string) (bool, error) {
	return c.foreign, c.err
}

func TestDeploy_NamespaceRefusals(t *testing.T) {
	cases := []struct {
		name   string
		c      namespaceChecker
		status int
		body   string
	}{
		{
			name:   "namespace in use",
			c:      namespaceChecker{foreign: true},
			status: http.StatusConflict,
			body:   `{"error":"namespace feature-pricing exists and is not a preview"}` + "\n",
		},
		{
			name:   "check error",
			c:      namespaceChecker{err: errors.New("namespaces \"feature-pricing\" is forbidden")},
			status: http.StatusBadGateway,
			body:   `{"error":"namespace check failed: namespaces \"feature-pricing\" is forbidden"}` + "\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fg := &fakeGitops{pr: pricingPR(), catalog: []string{"backend-1"}}
			fg.open(t)
			a := app.New(app.Config{Store: fg, Forge: fg, FanOut: fakeFanOut(fg), Namespaces: tc.c})
			srv := NewServer(a, fg, nil, "test-token")
			rec := postDeploy(t, srv, validBody(), "test-token")

			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.status, rec.Body.String())
			}
			if got := rec.Body.String(); got != tc.body {
				t.Errorf("body = %q, want %q", got, tc.body)
			}
			if fg.capturedSlug != "" || fg.fanOutBodyCalled {
				t.Error("a refused deploy must not write nor comment")
			}
		})
	}
}
