// Package kube keeps the PRs of the previews the sweep expired for the TTL
// (lifecycle.ExpiryStore) in the ConfigMap badil-expired of badil's
// namespace, written by the server.
package kube

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"

	"github.com/badilhq/badil/internal/expiry"
	"github.com/badilhq/badil/internal/lifecycle"
)

// ConfigMapName is the name of the expiry ConfigMap.
const ConfigMapName = "badil-expired"

// Label of the expiry ConfigMap. It carries no badil.dev/preview label, so
// the status store's listing ignores it.
const (
	managedByLabel = "app.kubernetes.io/managed-by"
	managedBy      = "badil"
)

// maxWriteAttempts bounds the attempts of a write that conflicts.
const maxWriteAttempts = 3

// record is the value of one data key: the PRKey, the PR's URL (for
// operators), the head SHA it expired at and when.
type record struct {
	Key string `json:"key"`
	PR  string `json:"pr"`
	SHA string `json:"sha"`
	At  string `json:"at"`
}

// Store is the ConfigMap lifecycle.ExpiryStore. Each write reads the
// ConfigMap again and changes only its own data keys, as the server's sweep,
// catch-up and deploys write it concurrently; Recorded reads it on every call.
type Store struct {
	client    kubernetes.Interface
	namespace string
}

var _ lifecycle.ExpiryStore = (*Store)(nil)

// New returns the Store over the ConfigMap badil-expired in namespace.
func New(client kubernetes.Interface, namespace string) *Store {
	return &Store{client: client, namespace: namespace}
}

// InCluster returns the Store over the in-cluster configuration and
// POD_NAMESPACE. Without one, without POD_NAMESPACE or when the client
// can't be made, it logs why and returns an in-memory store.
func InCluster(ctx context.Context) lifecycle.ExpiryStore {
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
		slog.Default().InfoContext(ctx, "expiry persistence disabled", slog.String("reason", reason))
		return expiry.NewMemory()
	}
	return New(client, namespace)
}

// DataKey returns the data key of the PRKey key: the first 32 hex characters
// of its SHA-256.
func DataKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:32]
}

// Mark records key at headSHA, replacing any record of key.
func (s *Store) Mark(ctx context.Context, key, prURL, headSHA string, at time.Time) error {
	raw, err := json.Marshal(record{Key: key, PR: prURL, SHA: headSHA, At: at.UTC().Format(time.RFC3339)})
	if err != nil {
		return fmt.Errorf("encode expiry record of %s: %w", key, err)
	}
	return s.update(ctx, true, func(data map[string]string) bool {
		data[DataKey(key)] = string(raw)
		return true
	})
}

// Recorded returns the head SHA key is recorded at. A missing ConfigMap or
// key, or a value that does not decode, is no record.
func (s *Store) Recorded(ctx context.Context, key string) (string, bool, error) {
	cm, err := s.client.CoreV1().ConfigMaps(s.namespace).Get(ctx, ConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read configmap %s: %w", ConfigMapName, err)
	}
	r, ok := decode(cm.Data[DataKey(key)])
	if !ok || r.Key != key {
		return "", false, nil
	}
	return r.SHA, true, nil
}

// Clear removes the record of key; a missing ConfigMap or record is no error.
func (s *Store) Clear(ctx context.Context, key string) error {
	return s.update(ctx, false, func(data map[string]string) bool {
		k := DataKey(key)
		if _, ok := data[k]; !ok {
			return false
		}
		delete(data, k)
		return true
	})
}

// Prune removes every record whose key keep returns false for, and every
// value that does not decode.
func (s *Store) Prune(ctx context.Context, keep func(key string) bool) error {
	return s.update(ctx, false, func(data map[string]string) bool {
		changed := false
		for k, raw := range data {
			if r, ok := decode(raw); ok && keep(r.Key) {
				continue
			}
			delete(data, k)
			changed = true
		}
		return changed
	})
}

// update applies change to the ConfigMap's data, read fresh at each
// attempt, and writes it back when change reports a change. A missing
// ConfigMap is created when create is set, and left missing otherwise. A
// conflicting write is retried, up to maxWriteAttempts attempts.
func (s *Store) update(ctx context.Context, create bool, change func(data map[string]string) bool) error {
	api := s.client.CoreV1().ConfigMaps(s.namespace)
	retriable := func(err error) bool { return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) }
	return retry.OnError(wait.Backoff{Steps: maxWriteAttempts}, retriable, func() error {
		cm, err := api.Get(ctx, ConfigMapName, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			if !create {
				return nil
			}
			data := map[string]string{}
			change(data)
			_, err = api.Create(ctx, s.configMap(data), metav1.CreateOptions{})
		case err == nil:
			if cm.Data == nil {
				cm.Data = map[string]string{}
			}
			if !change(cm.Data) {
				return nil
			}
			if cm.Labels == nil {
				cm.Labels = map[string]string{}
			}
			cm.Labels[managedByLabel] = managedBy
			_, err = api.Update(ctx, cm, metav1.UpdateOptions{})
		}
		if err != nil {
			return fmt.Errorf("write configmap %s: %w", ConfigMapName, err)
		}
		return nil
	})
}

// configMap returns the expiry ConfigMap holding data.
func (s *Store) configMap(data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigMapName,
			Namespace: s.namespace,
			Labels:    map[string]string{managedByLabel: managedBy},
		},
		Data: data,
	}
}

// decode parses a data value; ok is false when it does not decode or has no
// key.
func decode(raw string) (record, bool) {
	var r record
	if raw == "" || json.Unmarshal([]byte(raw), &r) != nil || r.Key == "" {
		return record{}, false
	}
	return r, true
}
