// Package expiry keeps the PRs of the previews the sweep expired for the TTL
// (lifecycle.ExpiryStore) in memory. Package expiry/kube keeps them in a
// ConfigMap.
package expiry

import (
	"context"
	"sync"
	"time"

	"github.com/badilhq/badil/internal/lifecycle"
)

// Memory is an in-memory lifecycle.ExpiryStore, safe for concurrent use. It
// is used out of a cluster and in tests.
type Memory struct {
	mu      sync.Mutex
	records map[string]string
}

var _ lifecycle.ExpiryStore = (*Memory)(nil)

// NewMemory returns an empty Memory.
func NewMemory() *Memory {
	return &Memory{records: map[string]string{}}
}

// Mark records key at headSHA, replacing any record of key.
func (m *Memory) Mark(_ context.Context, key, _, headSHA string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[key] = headSHA
	return nil
}

// Recorded returns the head SHA key is recorded at.
func (m *Memory) Recorded(_ context.Context, key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sha, ok := m.records[key]
	return sha, ok, nil
}

// Clear removes the record of key.
func (m *Memory) Clear(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, key)
	return nil
}

// Prune removes every record whose key keep returns false for.
func (m *Memory) Prune(_ context.Context, keep func(key string) bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key := range m.records {
		if !keep(key) {
			delete(m.records, key)
		}
	}
	return nil
}
