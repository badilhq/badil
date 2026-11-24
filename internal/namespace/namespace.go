// Package namespace tells whether the namespace of a new preview is free for
// badil to deploy into: absent, or already a preview namespace of that slug.
// A preview's slug is its namespace, and Argo CD adopts a namespace that
// already exists, so badil refuses a new preview whose namespace exists and
// was not made for it.
package namespace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/badilhq/badil/internal/lifecycle"
)

// previewLabel is the label the preview-apps chart sets on a preview
// namespace, with the preview's slug as value.
const previewLabel = "badil.dev/preview"

// client checks namespaces through a Kubernetes client.
type client struct {
	kube kubernetes.Interface
}

// New returns a lifecycle.NamespaceChecker reading namespaces through kube.
func New(kube kubernetes.Interface) lifecycle.NamespaceChecker {
	return &client{kube: kube}
}

// Foreign implements lifecycle.NamespaceChecker with one Get of the
// namespace. Any error but NotFound, Forbidden included, is returned.
func (c *client) Foreign(ctx context.Context, name string) (bool, error) {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get namespace %s: %w", name, err)
	}
	return ns.Labels[previewLabel] != name, nil
}

// disabled is the Checker outside a cluster: no namespace is foreign.
type disabled struct{}

func (disabled) Foreign(context.Context, string) (bool, error) { return false, nil }

// failing is the Checker inside a cluster whose client cannot be built: every
// check returns err.
type failing struct{ err error }

func (f failing) Foreign(context.Context, string) (bool, error) { return false, f.err }

// InCluster returns a Checker reading namespaces through the in-cluster
// configuration. Outside a cluster, it returns a Checker for which no
// namespace is foreign, and logs why, once. Inside a cluster, when the client
// cannot be built, such as without a readable ServiceAccount token, it returns
// a Checker for which every check fails with that error.
func InCluster(ctx context.Context) lifecycle.NamespaceChecker {
	cfg, err := rest.InClusterConfig()
	if errors.Is(err, rest.ErrNotInCluster) {
		slog.Default().InfoContext(ctx, "namespace check disabled", slog.String("reason", err.Error()))
		return disabled{}
	}
	if err != nil {
		return failing{err: fmt.Errorf("in-cluster config: %w", err)}
	}
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return failing{err: fmt.Errorf("kubernetes client: %w", err)}
	}
	return New(kube)
}
