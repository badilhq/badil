package status

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
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
func New(client kubernetes.Interface, namespace string) *Store {
	return newStore(&configMaps{client: client, namespace: namespace})
}

// InCluster returns a Store persisted in POD_NAMESPACE through the in-cluster
// configuration. Without one, or without POD_NAMESPACE, it returns a
// memory-only Store and logs why, once.
func InCluster(ctx context.Context) *Store {
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
		return NewMemory()
	}
	return New(client, namespace)
}

// selector selects the ConfigMaps badil manages.
func selector() string {
	return managedByLabel + "=" + managedBy
}

// list returns, by preview, the status read from the ConfigMaps badil
// manages that carry the preview label. A value that does not parse is
// logged and skipped.
func (c *configMaps) list(ctx context.Context) (map[string]map[string]Entry, error) {
	items, err := c.client.CoreV1().ConfigMaps(c.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector()})
	if err != nil {
		return nil, fmt.Errorf("list status configmaps in %s: %w", c.namespace, err)
	}
	out := map[string]map[string]Entry{}
	for _, cm := range items.Items {
		slug := cm.Labels[previewLabel]
		if slug == "" {
			continue
		}
		services := map[string]Entry{}
		for service, raw := range cm.Data {
			var e Entry
			if err = json.Unmarshal([]byte(raw), &e); err != nil {
				slog.Default().WarnContext(ctx, "skipping unreadable status",
					slog.String("configmap", cm.Name), slog.String("service", service), slog.String("err", err.Error()))
				continue
			}
			services[service] = e
		}
		out[slug] = services
	}
	return out, nil
}

// names returns the previews of the ConfigMaps badil manages that carry the
// preview label.
func (c *configMaps) names(ctx context.Context) ([]string, error) {
	items, err := c.client.CoreV1().ConfigMaps(c.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector()})
	if err != nil {
		return nil, fmt.Errorf("list status configmaps in %s: %w", c.namespace, err)
	}
	var slugs []string
	for _, cm := range items.Items {
		if slug := cm.Labels[previewLabel]; slug != "" {
			slugs = append(slugs, slug)
		}
	}
	return slugs, nil
}

// write creates or updates the ConfigMap of the preview slug with the status
// snapshot returns, read at each attempt; it writes nothing once snapshot
// reports the preview gone. A conflict reads the ConfigMap again and
// replaces its data, up to maxWriteAttempts attempts: badil is its only
// writer.
func (c *configMaps) write(ctx context.Context, slug string, snapshot func() (map[string]Entry, bool)) error {
	api := c.client.CoreV1().ConfigMaps(c.namespace)
	var err error
	for range maxWriteAttempts {
		services, ok := snapshot()
		if !ok {
			return nil
		}
		var data map[string]string
		if data, err = encode(services); err != nil {
			return err
		}
		var cm *corev1.ConfigMap
		cm, err = api.Get(ctx, ConfigMapName(slug), metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			_, err = api.Create(ctx, c.configMap(slug, data), metav1.CreateOptions{})
		case err == nil:
			cm.Labels = mergeLabels(cm.Labels, slug)
			cm.Data = data
			_, err = api.Update(ctx, cm, metav1.UpdateOptions{})
		}
		if !apierrors.IsConflict(err) && !apierrors.IsAlreadyExists(err) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("write configmap %s: %w", ConfigMapName(slug), err)
	}
	return nil
}

// delete deletes the ConfigMap of the preview slug; one already gone is no
// error.
func (c *configMaps) delete(ctx context.Context, slug string) error {
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
func encode(services map[string]Entry) (map[string]string, error) {
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
