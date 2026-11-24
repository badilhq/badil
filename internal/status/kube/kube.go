// Package kube keeps the status of the previews (package status) in one
// ConfigMap per preview in badil's namespace.
package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"

	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/status"
)

// Labels of the status ConfigMaps.
const (
	managedByLabel = "app.kubernetes.io/managed-by"
	managedBy      = "badil"
	previewLabel   = "badil.dev/preview"
)

// namePrefix starts the name of a preview's status ConfigMap.
const namePrefix = "badil-status-"

// maxWriteAttempts bounds the attempts of a ConfigMap write that conflicts.
const maxWriteAttempts = 3

// ConfigMapName returns the name of the status ConfigMap of the preview slug.
func ConfigMapName(slug string) string {
	return namePrefix + slug
}

// configMaps reads and writes the status ConfigMaps in one namespace.
type configMaps struct {
	client    kubernetes.Interface
	namespace string
}

// New returns a Store persisted as ConfigMaps in namespace through client.
func New(client kubernetes.Interface, namespace string) *status.Store {
	return status.NewPersisted(&configMaps{client: client, namespace: namespace})
}

// InCluster returns a Store persisted in POD_NAMESPACE through the in-cluster
// configuration. Without one, or without POD_NAMESPACE, it returns a
// memory-only Store and logs why, once.
func InCluster(ctx context.Context) *status.Store {
	reason := ""
	cfg, err := rest.InClusterConfig()
	namespace := os.Getenv("POD_NAMESPACE")
	var client kubernetes.Interface
	switch {
	case err != nil:
		reason = err.Error()
	case namespace == "":
		reason = "POD_NAMESPACE is unset"
	default:
		if client, err = kubernetes.NewForConfig(cfg); err != nil {
			reason = err.Error()
		}
	}
	if reason != "" {
		slog.Default().InfoContext(ctx, "status persistence disabled", slog.String("reason", reason))
		return status.NewMemory()
	}
	return New(client, namespace)
}

// selector selects the ConfigMaps badil manages.
const selector = managedByLabel + "=" + managedBy

// managed returns the ConfigMaps badil manages that carry the preview label.
func (c *configMaps) managed(ctx context.Context) ([]corev1.ConfigMap, error) {
	items, err := c.client.CoreV1().ConfigMaps(c.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("list status configmaps in %s: %w", c.namespace, err)
	}
	return slices.DeleteFunc(items.Items, func(cm corev1.ConfigMap) bool { return cm.Labels[previewLabel] == "" }), nil
}

// List returns, by preview, the status read from the ConfigMaps badil
// manages that carry the preview label. A value that does not parse is
// logged and skipped.
func (c *configMaps) List(ctx context.Context) (map[string]map[string]preview.ServiceStatus, error) {
	items, err := c.managed(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]preview.ServiceStatus{}
	for _, cm := range items {
		services := map[string]preview.ServiceStatus{}
		for service, raw := range cm.Data {
			var e preview.ServiceStatus
			if err = json.Unmarshal([]byte(raw), &e); err != nil {
				slog.Default().WarnContext(ctx, "skipping unreadable status",
					slog.String("configmap", cm.Name), slog.String("service", service), slog.String("err", err.Error()))
				continue
			}
			services[service] = e
		}
		out[cm.Labels[previewLabel]] = services
	}
	return out, nil
}

// Names returns the previews of the ConfigMaps badil manages that carry the
// preview label.
func (c *configMaps) Names(ctx context.Context) ([]string, error) {
	items, err := c.managed(ctx)
	if err != nil {
		return nil, err
	}
	slugs := make([]string, 0, len(items))
	for _, cm := range items {
		slugs = append(slugs, cm.Labels[previewLabel])
	}
	return slugs, nil
}

// Write creates or updates the ConfigMap of the preview slug with the status
// snapshot returns, read at each attempt; it writes nothing once snapshot
// reports the preview gone. A conflict reads the ConfigMap again and
// replaces its data, up to maxWriteAttempts attempts: badil is its only
// writer.
func (c *configMaps) Write(
	ctx context.Context,
	slug string,
	snapshot func() (map[string]preview.ServiceStatus, bool),
) error {
	api := c.client.CoreV1().ConfigMaps(c.namespace)
	retriable := func(err error) bool { return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) }
	return retry.OnError(wait.Backoff{Steps: maxWriteAttempts}, retriable, func() error {
		services, ok := snapshot()
		if !ok {
			return nil
		}
		data, err := encode(services)
		if err != nil {
			return err
		}
		cm, err := api.Get(ctx, ConfigMapName(slug), metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			_, err = api.Create(ctx, c.configMap(slug, data), metav1.CreateOptions{})
		case err == nil:
			cm.Labels = mergeLabels(cm.Labels, slug)
			cm.Data = data
			_, err = api.Update(ctx, cm, metav1.UpdateOptions{})
		}
		if err != nil {
			return fmt.Errorf("write configmap %s: %w", ConfigMapName(slug), err)
		}
		return nil
	})
}

// Delete deletes the ConfigMap of the preview slug; one already gone is no
// error.
func (c *configMaps) Delete(ctx context.Context, slug string) error {
	err := c.client.CoreV1().ConfigMaps(c.namespace).Delete(ctx, ConfigMapName(slug), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete configmap %s: %w", ConfigMapName(slug), err)
	}
	return nil
}

// configMap returns the status ConfigMap of the preview slug holding data.
func (c *configMaps) configMap(slug string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigMapName(slug),
			Namespace: c.namespace,
			Labels:    mergeLabels(nil, slug),
		},
		Data: data,
	}
}

// mergeLabels returns labels with the status ConfigMap labels of the preview
// slug set.
func mergeLabels(labels map[string]string, slug string) map[string]string {
	if labels == nil {
		labels = map[string]string{}
	}
	labels[managedByLabel] = managedBy
	labels[previewLabel] = slug
	return labels
}

// encode returns services as ConfigMap data: one JSON entry per service.
func encode(services map[string]preview.ServiceStatus) (map[string]string, error) {
	data := make(map[string]string, len(services))
	for name, e := range services {
		raw, err := json.Marshal(e)
		if err != nil {
			return nil, fmt.Errorf("encode status of %s: %w", name, err)
		}
		data[name] = string(raw)
	}
	return data, nil
}
