package app_test

import (
	"context"
	"testing"

	"github.com/badilhq/badil/internal/app"
)

// namespaceChecker is a lifecycle.NamespaceChecker: every namespace is foreign
// when foreign is set.
type namespaceChecker struct {
	foreign bool
}

func (c namespaceChecker) Foreign(context.Context, string) (bool, error) {
	return c.foreign, nil
}

// TestConfig_Namespaces checks that the namespace check reaches the
// reconciles and the deploys.
func TestConfig_Namespaces(t *testing.T) {
	c := namespaceChecker{foreign: true}
	a := app.New(app.Config{Namespaces: c})
	if a.ReconcileAPI().Namespaces != c || a.DeployAPI().Namespaces != c {
		t.Error("the namespace check is not on the reconcile and deploy APIs")
	}
	if app.New(app.Config{}).ReconcileAPI().Namespaces != nil {
		t.Error("a namespace check without Config.Namespaces")
	}
}
