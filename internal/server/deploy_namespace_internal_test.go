package server

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// namespaceChecker is a namespace.Checker: every namespace is foreign when
// foreign is set, and every check fails with err when it is set.
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
			fg := &fakeGitops{catalog: []string{"backend-1"}}
			fg.open(t)
			srv := NewServer(fg, fg, "test-token", withFanOut(fg), WithNamespaces(tc.c))
			rec := postDeploy(t, srv, previewBody(), "test-token")

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

// TestWithNamespaces checks that the namespace check reaches the reconciles
// and the deploys.
func TestWithNamespaces(t *testing.T) {
	fg := &fakeGitops{}
	fg.open(t)
	c := namespaceChecker{foreign: true}
	srv := NewServer(fg, fg, "test-token", WithNamespaces(c))
	if srv.reconcileAPI().Namespaces != c || srv.deployAPI().Namespaces != c {
		t.Error("the namespace check is not on the reconcile and deploy APIs")
	}
	if NewServer(fg, fg, "test-token").reconcileAPI().Namespaces != nil {
		t.Error("a namespace check without WithNamespaces")
	}
}
