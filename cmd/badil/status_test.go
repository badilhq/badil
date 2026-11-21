package main

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/badilhq/badil/internal/server"
	"github.com/badilhq/badil/internal/status"
	"github.com/badilhq/badil/internal/store"
	"github.com/badilhq/badil/internal/store/storetest"
)

const statusNamespace = "badil"

// previewFile is a minimal preview file with one service.
const previewFile = "apiVersion: badil.dev/v1beta1\nservices:\n  - name: backend-1\n"

// statusCM is the status ConfigMap of slug, as badil writes it.
func statusCM(slug string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: status.ConfigMapName(slug), Namespace: statusNamespace,
			Labels: map[string]string{"app.kubernetes.io/managed-by": "badil", "badil.dev/preview": slug},
		},
		Data: map[string]string{"backend-1": `{"revision":"aaa","status":"ready","at":"2026-11-19T21:00:00Z"}`},
	}
}

// hasCM reports whether slug's status ConfigMap exists.
func hasCM(t *testing.T, client *fake.Clientset, slug string) bool {
	t.Helper()
	_, err := client.CoreV1().ConfigMaps(statusNamespace).Get(
		context.Background(), status.ConfigMapName(slug), metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	return err == nil
}

// waitFor fails t unless cond holds within 2 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("not within 2s: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestStatusHooks checks serve's wiring of the status store to the gitops
// store, with no in-server sweep: a preview deleted by a landed Apply loses
// its status and ConfigMap through store.WithApplied, and one deleted by a
// commit made outside the server (as badil cleanup does) loses them once a
// fetch moves the head past it.
func TestStatusHooks(t *testing.T) {
	remote := storetest.NewRemote(t, map[string]string{
		"badil.yaml":                    "services: {}\n",
		"previews/applied/values.yaml":  previewFile,
		"previews/external/values.yaml": previewFile,
		"previews/kept/values.yaml":     previewFile,
	})
	client := fake.NewClientset(statusCM("applied"), statusCM("external"), statusCM("kept"))
	ctx := context.Background()
	previewStatus := status.New(client, statusNamespace)
	if err := loadStatus(ctx, previewStatus); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, store.CloneConfig{URL: remote.URL()}, storetest.Identity,
		statusHooks(previewStatus, server.NewWake())...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	previewStatus.Start(ctx, st)
	t.Cleanup(func() { previewStatus.Close(ctx) })

	if _, err = st.Apply(ctx, store.Op{Kind: store.OpSweep, Change: func(tree *store.Tree) (store.Summary, error) {
		tree.DeletePreview("applied")
		return store.Summary{Subject: "chore(preview): sweep"}, nil
	}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(previewStatus.Preview("applied")) != 0 {
		t.Error("applied: status kept after the Apply deleting it")
	}
	waitFor(t, "applied configmap deleted", func() bool { return !hasCM(t, client, "applied") })

	remote.Set("previews/external/values.yaml", "")
	st.Invalidate()
	if _, err = st.ListPreviews(ctx); err != nil {
		t.Fatalf("ListPreviews: %v", err)
	}
	waitFor(t, "external collected", func() bool {
		return !hasCM(t, client, "external") && len(previewStatus.Preview("external")) == 0
	})
	if len(previewStatus.Preview("kept")) != 1 || !hasCM(t, client, "kept") {
		t.Error("kept: status or configmap removed")
	}
}
