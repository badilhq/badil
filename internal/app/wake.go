package app

import "sync"

// Wake is a broadcast signal: each Notify wakes every goroutine blocked on
// the channel Wait returned before it. Its methods are safe for concurrent
// use, and Notify never blocks.
type Wake struct {
	mu sync.Mutex
	ch chan struct{}
}

// NewWake returns a Wake no one waits on yet.
func NewWake() *Wake {
	return &Wake{ch: make(chan struct{})}
}

// Notify wakes every waiter: it closes the current channel and replaces it.
func (w *Wake) Notify() {
	w.mu.Lock()
	defer w.mu.Unlock()
	close(w.ch)
	w.ch = make(chan struct{})
}

// Wait returns the channel the next Notify closes.
func (w *Wake) Wait() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ch
}
