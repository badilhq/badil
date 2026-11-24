// Package status keeps the latest status the renderer reported for each
// service of each preview. Memory is the source of truth for reads; with a
// Persistence, each preview's status is also written at most once per flush
// interval and read back on start.
package status

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"
)

// The statuses a renderer reports.
const (
	Ready       = "ready"
	Progressing = "progressing"
	Degraded    = "degraded"
	SyncFailed  = "sync-failed"
)

// Valid reports whether s is one of the statuses a renderer reports.
func Valid(s string) bool {
	switch s {
	case Ready, Progressing, Degraded, SyncFailed:
		return true
	}
	return false
}

// flushInterval is the shortest time between two writes of a preview's
// ConfigMap.
const flushInterval = 5 * time.Second

// Entry is the latest status of one service of a preview: the revision it is
// about, the status and when the renderer reported it.
type Entry struct {
	Revision string    `json:"revision"`
	Status   string    `json:"status"`
	At       time.Time `json:"at"`
}

// Lister lists the previews whose file is in the gitops repo:
// *store.GitHubStore.
type Lister interface {
	ListPreviewFiles(ctx context.Context) ([]string, error)
}

// Persistence keeps each preview's status outside the process:
// internal/status/kube keeps it in ConfigMaps.
type Persistence interface {
	// List returns, by preview, the persisted status of each service.
	List(ctx context.Context) (map[string]map[string]Entry, error)
	// Names returns the previews that have persisted status.
	Names(ctx context.Context) ([]string, error)
	// Write persists the status snapshot returns for the preview slug,
	// calling snapshot at each attempt and writing nothing once it reports
	// the preview gone.
	Write(ctx context.Context, slug string, snapshot func() (map[string]Entry, bool)) error
	// Delete removes the persisted status of slug, already absent being no
	// error.
	Delete(ctx context.Context, slug string) error
}

// previewStatus is the status of a preview's services.
type previewStatus struct {
	services map[string]Entry
	// ready holds service+"@"+revision for each ready recorded in this
	// process.
	ready map[string]bool
	// seq is the Store's seq when an event was last recorded for the
	// preview, 0 for a preview read back on start.
	seq uint64
}

// Store is the status of every preview, in memory and, with a Kubernetes
// client, in ConfigMaps. Its methods are safe for concurrent use.
type Store struct {
	persist  Persistence
	interval time.Duration

	mu       sync.Mutex
	previews map[string]*previewStatus
	// dirty holds the previews whose ConfigMap is behind memory.
	dirty map[string]bool
	// gone holds the previews whose ConfigMap is to be deleted.
	gone map[string]bool
	// seq counts the recorded events.
	seq uint64

	// wake asks the writer to delete the ConfigMaps of gone now; collect
	// asks the collector for a garbage collection.
	wake    chan struct{}
	collect chan struct{}
	stop    context.CancelFunc
	wg      sync.WaitGroup
}

// newStore returns an empty Store, persisted through p when it is not nil.
func newStore(p Persistence) *Store {
	return &Store{
		persist:  p,
		interval: flushInterval,
		previews: map[string]*previewStatus{},
		dirty:    map[string]bool{},
		gone:     map[string]bool{},
		wake:     make(chan struct{}, 1),
		collect:  make(chan struct{}, 1),
	}
}

// NewMemory returns a Store kept in memory only.
func NewMemory() *Store {
	return newStore(nil)
}

// NewPersisted returns an empty Store persisted through p, which must not be
// nil.
func NewPersisted(p Persistence) *Store {
	return newStore(p)
}

// Record sets the status of service in the preview slug to e, unless e is
// older than the stored entry. It reports whether e was recorded and, when
// it was, whether it is the first ready recorded in this process for that
// service at e's revision.
func (s *Store) Record(slug, service string, e Entry) (bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.previews[slug]
	if p == nil {
		p = &previewStatus{services: map[string]Entry{}, ready: map[string]bool{}}
		s.previews[slug] = p
	}
	if old, ok := p.services[service]; ok && e.At.Before(old.At) {
		return false, false
	}
	s.seq++
	p.services[service] = e
	p.seq = s.seq
	s.dirty[slug] = true
	delete(s.gone, slug)
	if e.Status != Ready {
		return true, false
	}
	key := service + "@" + e.Revision
	first := !p.ready[key]
	p.ready[key] = true
	return true, first
}

// Preview returns the status of the services of the preview slug, by
// service; none when nothing is known.
func (s *Store) Preview(slug string) map[string]Entry {
	if out, ok := s.snapshot(slug); ok {
		return out
	}
	return map[string]Entry{}
}

// Delete forgets the status of the previews slugs and queues the deletion of
// their ConfigMaps. It does not wait for Kubernetes.
func (s *Store) Delete(slugs ...string) {
	if len(slugs) == 0 {
		return
	}
	s.mu.Lock()
	for _, slug := range slugs {
		s.forget(slug)
	}
	s.mu.Unlock()
	s.signal(s.wake)
}

// forget drops slug from memory and, when persisted, queues the deletion of
// its ConfigMap. The caller holds s.mu.
func (s *Store) forget(slug string) {
	delete(s.previews, slug)
	delete(s.dirty, slug)
	if s.persist != nil {
		s.gone[slug] = true
	}
}

// CollectGarbage asks for a garbage collection (collectGarbage) in the
// background, once Start ran. It does not wait: requests made while one runs
// make one more run.
func (s *Store) CollectGarbage() {
	s.signal(s.collect)
}

// signal sends on ch, a channel of capacity 1, unless a send is pending.
func (s *Store) signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Load reads every preview's status back from its ConfigMap. It reads
// nothing when s is memory-only. A failed listing is returned.
func (s *Store) Load(ctx context.Context) error {
	if s.persist == nil {
		return nil
	}
	loaded, err := s.persist.List(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for slug, services := range loaded {
		s.previews[slug] = &previewStatus{services: services, ready: map[string]bool{}}
	}
	return nil
}

// Start runs, until Close, the garbage collector over lister and, when s is
// persisted, the ConfigMap writer, and asks for a first garbage collection.
func (s *Store) Start(ctx context.Context, lister Lister) {
	ctx, s.stop = context.WithCancel(ctx)
	s.wg.Go(func() { s.runCollector(ctx, lister) })
	if s.persist != nil {
		s.wg.Go(func() { s.runWriter(ctx) })
	}
	s.CollectGarbage()
}

// Close stops what Start runs and, when s is persisted, deletes the queued
// ConfigMaps and writes the dirty previews once, within ctx.
func (s *Store) Close(ctx context.Context) {
	if s.stop != nil {
		s.stop()
	}
	s.wg.Wait()
	if s.persist != nil {
		s.deleteGone(ctx)
		s.flush(ctx)
	}
}

// runCollector collects garbage on each request until ctx is done. Runs
// never overlap.
func (s *Store) runCollector(ctx context.Context, lister Lister) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.collect:
			s.collectGarbage(ctx, lister)
		}
	}
}

// runWriter deletes the queued ConfigMaps when woken, and writes the dirty
// previews every interval, until ctx is done.
func (s *Store) runWriter(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
			s.deleteGone(ctx)
		case <-ticker.C:
			s.deleteGone(ctx)
			s.flush(ctx)
		}
	}
}

// collectGarbage forgets the status of the previews whose file is not in
// the gitops repo, as lister lists it, and deletes their ConfigMaps. A
// preview recorded after the listing started is kept: the listing may be
// older than the tree its event read. The server reads the preview again
// after recording an event and deletes its status when the file is gone. A
// failed listing is logged.
func (s *Store) collectGarbage(ctx context.Context, lister Lister) {
	s.mu.Lock()
	seq := s.seq
	s.mu.Unlock()
	slugs, err := lister.ListPreviewFiles(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Default().WarnContext(ctx, "status garbage collection failed", slog.String("err", err.Error()))
		}
		return
	}
	var stored []string
	if s.persist != nil {
		if stored, err = s.persist.Names(ctx); err != nil {
			if ctx.Err() == nil {
				slog.Default().WarnContext(ctx, "status garbage collection failed", slog.String("err", err.Error()))
			}
			return
		}
	}
	present := map[string]bool{}
	for _, slug := range slugs {
		present[slug] = true
	}
	s.mu.Lock()
	var removed []string
	for slug, p := range s.previews {
		if !present[slug] && p.seq <= seq {
			s.forget(slug)
			removed = append(removed, slug)
		}
	}
	for _, slug := range stored {
		if !present[slug] && s.previews[slug] == nil {
			s.gone[slug] = true
		}
	}
	s.mu.Unlock()
	if len(removed) > 0 {
		slices.Sort(removed)
		slog.Default().InfoContext(ctx, "status garbage collected", slog.Any("previews", removed))
	}
	s.signal(s.wake)
}

// deleteGone deletes the ConfigMaps of the queued previews. A failed delete
// stays queued for the next run, unless the preview was recorded since.
func (s *Store) deleteGone(ctx context.Context) {
	s.mu.Lock()
	slugs := slices.Sorted(maps.Keys(s.gone))
	clear(s.gone)
	s.mu.Unlock()
	for _, slug := range slugs {
		if err := s.persist.Delete(ctx, slug); err != nil {
			slog.Default().WarnContext(ctx, "deleting status configmap failed",
				slog.String("preview", slug), slog.String("err", err.Error()))
			s.mu.Lock()
			if s.previews[slug] == nil {
				s.gone[slug] = true
			}
			s.mu.Unlock()
		}
	}
}

// flush writes the ConfigMap of each dirty preview from memory. A failed
// write is logged and the preview stays dirty for the next flush.
func (s *Store) flush(ctx context.Context) {
	s.mu.Lock()
	slugs := slices.Sorted(maps.Keys(s.dirty))
	clear(s.dirty)
	s.mu.Unlock()
	for _, slug := range slugs {
		if err := s.persist.Write(ctx, slug, func() (map[string]Entry, bool) { return s.snapshot(slug) }); err != nil {
			slog.Default().WarnContext(ctx, "writing status configmap failed",
				slog.String("preview", slug), slog.String("err", err.Error()))
			s.mu.Lock()
			if s.previews[slug] != nil {
				s.dirty[slug] = true
			}
			s.mu.Unlock()
		}
	}
}

// snapshot returns a copy of the status of the preview slug, and false when
// it is no longer in memory.
func (s *Store) snapshot(slug string) (map[string]Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.previews[slug]
	if p == nil {
		return nil, false
	}
	return maps.Clone(p.services), true
}
