package kube_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/badilhq/badil/internal/expiry/kube"
)

const (
	ns    = "badil"
	front = "acme/front#1"
	api   = "acme/api#2"
)

var at = time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC) //nolint:gochecknoglobals // test fixture

// data returns the data of the expiry ConfigMap, failing t when it is missing.
func data(t *testing.T, c *fake.Clientset) map[string]string {
	t.Helper()
	cm, err := c.CoreV1().ConfigMaps(ns).Get(context.Background(), kube.ConfigMapName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get configmap: %v", err)
	}
	return cm.Data
}

func TestStore_MarkRecordedClear(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientset()
	s := kube.New(c, ns)
	if _, ok, err := s.Recorded(ctx, front); ok || err != nil {
		t.Fatalf("Recorded without configmap = ok %v, err %v", ok, err)
	}
	if err := s.Clear(ctx, front); err != nil {
		t.Fatalf("Clear without configmap: %v", err)
	}
	if err := s.Prune(ctx, func(string) bool { return false }); err != nil {
		t.Fatalf("Prune without configmap: %v", err)
	}
	if _, err := c.CoreV1().
		ConfigMaps(ns).
		Get(ctx, kube.ConfigMapName, metav1.GetOptions{}); !apierrors.IsNotFound(
		err,
	) {
		t.Fatalf("Clear or Prune created the configmap: %v", err)
	}
	if err := s.Mark(ctx, front, "https://github.com/acme/front/pull/1", "s1", at); err != nil {
		t.Fatalf("Mark: %v", err)
	}
	cm, _ := c.CoreV1().ConfigMaps(ns).Get(ctx, kube.ConfigMapName, metav1.GetOptions{})
	if cm.Labels["app.kubernetes.io/managed-by"] != "badil" || cm.Labels["badil.dev/preview"] != "" {
		t.Errorf("labels = %v, want managed-by badil and no preview label", cm.Labels)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(cm.Data[kube.DataKey(front)]), &got); err != nil {
		t.Fatalf("value of %s: %v", kube.DataKey(front), err)
	}
	want := map[string]string{
		"key": front,
		"pr":  "https://github.com/acme/front/pull/1",
		"sha": "s1",
		"at":  "2026-11-26T09:00:00Z",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("value[%s] = %q, want %q", k, got[k], v)
		}
	}
	if len(kube.DataKey(front)) != 32 {
		t.Errorf("data key %q is not 32 characters", kube.DataKey(front))
	}
	if err := s.Mark(ctx, front, "https://github.com/acme/front/pull/1", "s2", at); err != nil {
		t.Fatalf("Mark again: %v", err)
	}
	if sha, ok, err := s.Recorded(ctx, front); err != nil || !ok || sha != "s2" {
		t.Errorf("Recorded = %q, %v, %v; want s2", sha, ok, err)
	}
	if err := s.Clear(ctx, front); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, ok, _ := s.Recorded(ctx, front); ok {
		t.Error("Clear kept the record")
	}
}

func TestStore_PruneAndUndecodable(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: kube.ConfigMapName, Namespace: ns},
		Data:       map[string]string{"garbage": "{not json", kube.DataKey(api): "{}"},
	})
	s := kube.New(c, ns)
	if _, ok, err := s.Recorded(ctx, api); ok || err != nil {
		t.Errorf("Recorded of an undecodable value = ok %v, err %v; want no record, no error", ok, err)
	}
	_ = s.Mark(ctx, front, "", "s1", at)
	_ = s.Mark(ctx, "acme/gone#3", "", "g1", at)
	if err := s.Prune(ctx, func(key string) bool { return key == front }); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	d := data(t, c)
	if len(d) != 1 || d[kube.DataKey(front)] == "" {
		t.Errorf("data after Prune = %v, want only %s", d, front)
	}
}

// cmGVR is the resource of ConfigMaps in the fake's object tracker.
var cmGVR = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"} //nolint:gochecknoglobals // test fixture

// addRecord adds a record of key at sha to the expiry ConfigMap in c's
// tracker, as another writer would. Reactors run under the fake's lock, so
// they reach the tracker, never the clientset.
func addRecord(t *testing.T, c *fake.Clientset, key, sha string) {
	t.Helper()
	obj, err := c.Tracker().Get(cmGVR, ns, kube.ConfigMapName)
	if err != nil {
		t.Errorf("tracker get: %v", err)
		return
	}
	cm := obj.(*corev1.ConfigMap).DeepCopy()
	cm.Data[kube.DataKey(key)] = `{"key":"` + key + `","pr":"","sha":"` + sha + `","at":""}`
	if err = c.Tracker().Update(cmGVR, cm, ns); err != nil {
		t.Errorf("tracker update: %v", err)
	}
}

// TestStore_ConflictKeepsOtherWriter: a write whose Update conflicts because
// another writer added a key reads the ConfigMap again and keeps that key.
func TestStore_ConflictKeepsOtherWriter(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientset()
	s := kube.New(c, ns)
	if err := s.Mark(ctx, front, "", "s1", at); err != nil {
		t.Fatalf("Mark: %v", err)
	}
	updates := 0
	c.PrependReactor("update", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		updates++
		if updates > 1 {
			return false, nil, nil
		}
		// The other writer (the cleanup CronJob) wrote its record first.
		addRecord(t, c, api, "a1")
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, kube.ConfigMapName, nil)
	})
	if err := s.Mark(ctx, "acme/front#9", "", "n1", at); err != nil {
		t.Fatalf("Mark after a conflict: %v", err)
	}
	if updates != 2 {
		t.Errorf("updates = %d, want 2 (one conflict, one retry)", updates)
	}
	d := data(t, c)
	for _, key := range []string{front, api, "acme/front#9"} {
		if d[kube.DataKey(key)] == "" {
			t.Errorf("record of %s lost: data = %v", key, d)
		}
	}
}

func TestStore_CreateRace(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientset()
	s := kube.New(c, ns)
	creates := 0
	c.PrependReactor("create", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		creates++
		if creates > 1 {
			return false, nil, nil
		}
		// The other writer created the ConfigMap between the Get and the Create.
		if err := c.Tracker().Create(cmGVR, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: kube.ConfigMapName, Namespace: ns},
			Data:       map[string]string{kube.DataKey(api): `{"key":"acme/api#2","pr":"","sha":"a1","at":""}`},
		}, ns); err != nil {
			t.Errorf("tracker create: %v", err)
		}
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "configmaps"}, kube.ConfigMapName)
	})
	if err := s.Mark(ctx, front, "", "s1", at); err != nil {
		t.Fatalf("Mark: %v", err)
	}
	d := data(t, c)
	if d[kube.DataKey(front)] == "" || d[kube.DataKey(api)] == "" {
		t.Errorf("data = %v, want both records", d)
	}
}
