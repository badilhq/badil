// Package namespace tells whether the namespace of a new preview is free for
// badil to deploy into: absent, or already a preview namespace of that slug.
// A preview's slug is its namespace, and Argo CD adopts a namespace that
// already exists, so badil refuses a new preview whose namespace exists and
// was not made for it.
package namespace

import (
	"context"
	"fmt"
	"log/slog"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Labels of a preview namespace, set by the preview-apps chart.
const (
	partOfLabel   = "app.kubernetes.io/part-of"
	partOf        = "preview-environments"
	instanceLabel = "app.kubernetes.io/instance"
)

// Checker reads the namespace of a new preview.
type Checker interface {
	// Foreign reports whether the namespace name exists and is not a preview
	// namespace for the slug name: one labelled
	// app.kubernetes.io/part-of=preview-environments and
	// app.kubernetes.io/instance=<name>. A failed read is returned.
	Foreign(ctx context.Context, name string) (bool, error)
}

// client checks namespaces through a Kubernetes client.
type client struct {
	kube kubernetes.Interface
}

// New returns a Checker reading namespaces through kube.
func New(kube kubernetes.Interface) Checker {
	return &client{kube: kube}
}

// Foreign implements Checker with one Get of the namespace. Any error but
// NotFound, Forbidden included, is returned.
func (c *client) Foreign(ctx context.Context, name string) (bool, error) {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get namespace %s: %w", name, err)
	}
	return ns.Labels[partOfLabel] != partOf || ns.Labels[instanceLabel] != name, nil
}

// disabled is the Checker outside a cluster: no namespace is foreign.
type disabled struct{}

func (disabled) Foreign(context.Context, string) (bool, error) { return false, nil }

// InCluster returns a Checker reading namespaces through the in-cluster
// configuration. Without one, it returns a Checker for which no namespace is
// foreign, and logs why, once.
func InCluster(ctx context.Context) Checker {
	cfg, err := rest.InClusterConfig()
	var kube kubernetes.Interface
	if err == nil {
		kube, err = kubernetes.NewForConfig(cfg)
	}
	if err != nil {
		slog.Default().InfoContext(ctx, "namespace check disabled", slog.String("reason", err.Error()))
		return disabled{}
	}
	return New(kube)
}
