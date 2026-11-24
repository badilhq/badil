package server_test

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/badilhq/badil/internal/server"
)

// recordExit returns an exit function that sends its code on the returned
// channel.
func recordExit() (func(code int), <-chan int) {
	codes := make(chan int, 1)
	return func(code int) { codes <- code }, codes
}

// TestServeInBackground_ExitsOnListenError checks that a listener that cannot
// listen calls exit with code 1.
func TestServeInBackground_ExitsOnListenError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	exit, codes := recordExit()
	srv := &http.Server{Addr: ln.Addr().String(), ReadHeaderTimeout: time.Second}
	server.ServeInBackground(t.Context(), "server", srv, exit)
	select {
	case code := <-codes:
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("exit not called within 2s")
	}
}

// TestServeInBackground_ShutdownDoesNotExit checks that shutting a listener
// down does not call exit.
func TestServeInBackground_ShutdownDoesNotExit(t *testing.T) {
	exit, codes := recordExit()
	srv := &http.Server{Addr: "127.0.0.1:0", ReadHeaderTimeout: time.Second}
	server.ServeInBackground(t.Context(), "server", srv, exit)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := server.ShutdownServers(ctx, srv); err != nil {
		t.Fatalf("ShutdownServers: %v", err)
	}
	select {
	case code := <-codes:
		t.Errorf("exit(%d) called after shutdown, want no call", code)
	case <-time.After(100 * time.Millisecond):
	}
}
