package namespace_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/badilhq/badil/internal/namespace"
)

// pricing is the namespace pricing with labels.
func pricing(labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "pricing", Labels: labels}}
}

func TestForeign(t *testing.T) {
	previewOf := func(slug string) map[string]string {
		return map[string]string{"badil.dev/preview": slug}
	}
	cases := []struct {
		name     string
		existing []runtime.Object
		want     bool
	}{
		{name: "not found", want: false},
		{
			name:     "preview namespace for the slug",
			existing: []runtime.Object{pricing(previewOf("pricing"))},
			want:     false,
		},
		{name: "without labels", existing: []runtime.Object{pricing(nil)}, want: true},
		{
			name:     "part-of only",
			existing: []runtime.Object{pricing(map[string]string{"app.kubernetes.io/part-of": "preview-environments"})},
			want:     true,
		},
		{name: "preview of another slug", existing: []runtime.Object{pricing(previewOf("search"))}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := namespace.New(fake.NewClientset(tc.existing...))
			got, err := c.Foreign(context.Background(), "pricing")
			if err != nil {
				t.Fatalf("Foreign: %v", err)
			}
			if got != tc.want {
				t.Errorf("Foreign = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestForeign_GetError(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "namespaces"},
			"pricing",
			errors.New("rbac"),
		)
	})
	got, err := namespace.New(client).Foreign(context.Background(), "pricing")
	if !apierrors.IsForbidden(err) {
		t.Fatalf("Foreign err = %v, want Forbidden", err)
	}
	if got {
		t.Error("Foreign = true on a Get error")
	}
}

// TestInCluster_DisabledOutsideACluster checks that without in-cluster
// configuration no namespace is foreign, and that it logs why once.
func TestInCluster_DisabledOutsideACluster(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	c := namespace.InCluster(context.Background())
	if n := strings.Count(buf.String(), `level=INFO msg="namespace check disabled" reason=`); n != 1 {
		t.Errorf("logs = %q, want one namespace check disabled line", buf.String())
	}
	got, err := c.Foreign(context.Background(), "kube-system")
	if err != nil || got {
		t.Errorf("Foreign = %v, %v, want false, nil", got, err)
	}
}

// TestInCluster_FailsWithoutToken checks that inside a cluster, without a
// readable ServiceAccount token, every check fails instead of being disabled.
func TestInCluster_FailsWithoutToken(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "443")
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	c := namespace.InCluster(context.Background())
	if strings.Contains(buf.String(), "namespace check disabled") {
		t.Errorf("logs = %q, want no namespace check disabled line", buf.String())
	}
	got, err := c.Foreign(context.Background(), "kube-system")
	if err == nil || !strings.Contains(err.Error(), "in-cluster config") {
		t.Errorf("Foreign err = %v, want an in-cluster config error", err)
	}
	if got {
		t.Error("Foreign = true on a config error")
	}
}
