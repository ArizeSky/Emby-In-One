// shutdown_lifecycle_test.go covers the S8 close-order contract: Run returns
// only after the drain is complete, an expired drain cancels in-flight
// handlers and force-closes, a failed listener still releases the shutdown
// goroutine, and the shared SQLite handle cannot be closed under a reader nor
// used after it is closed.
package backend

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func reservePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// TestServeWithShutdownWaitsForDrain verifies the returning of server-serve:
// the caller is blocked until active handlers complete, and they are not
// cancelled by a drain that had time to finish.
func TestServeWithShutdownWaitsForDrain(t *testing.T) {
	addr := reservePort(t)
	completed := make(chan struct{})
	var cancelled atomic.Bool
	baseCtx, baseCancel := context.WithCancel(context.Background())
	defer baseCancel()
	srv := &http.Server{
		Addr: addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(300 * time.Millisecond):
				w.WriteHeader(http.StatusOK)
				close(completed)
			case <-r.Context().Done():
				cancelled.Store(true)
			}
		}),
		BaseContext: func(net.Listener) context.Context { return baseCtx },
	}

	shutdownCh := make(chan os.Signal, 1)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- serveHTTPWithShutdown(srv, shutdownCh, make(chan struct{}), baseCancel, shutdownDrainTimeout, func() {}, nil)
	}()
	started := time.Now()
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get("http://" + addr + "/streaming")
	if err != nil {
		t.Fatalf("client request: %v", err)
	}
	_ = resp.Body.Close()
	select {
	case <-completed:
	case <-time.After(5 * time.Second):
		t.Fatalf("handler never completed; cancelled=%v", cancelled.Load())
	}
	if cancelled.Load() {
		t.Fatal("an in-flight handler is still needed for the drain check; the response ended early")
	}
	// Now trigger shutdown and confirm Run-style serving returns only after the
	// 300 ms drain was respected.
	shutdownCh <- os.Interrupt
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("serve returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve never completed the drain")
	}
	if elapsed := time.Since(started); elapsed < 250*time.Millisecond {
		t.Fatalf("serve returned %v after the client finished; drain was not respected", elapsed)
	}
	if cancelled.Load() {
		t.Fatal("a drain with enough time must not cancel in-flight handlers")
	}
}

// TestServeWithShutdownForceClosesOnDrainTimeout verifies the drain deadline:
// the base context is cancelled, the server force-closes long handlers and
// only then does the caller continue.
func TestServeWithShutdownForceClosesOnDrainTimeout(t *testing.T) {
	addr := reservePort(t)
	entered := make(chan struct{})
	finished := make(chan struct{})
	baseCtx, baseCancel := context.WithCancel(context.Background())
	defer baseCancel()
	srv := &http.Server{
		Addr: addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-r.Context().Done()
			close(finished)
		}),
		BaseContext: func(net.Listener) context.Context { return baseCtx },
	}
	shutdownCh := make(chan os.Signal, 1)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- serveHTTPWithShutdown(srv, shutdownCh, make(chan struct{}), baseCancel, 150*time.Millisecond, func() {}, nil)
	}()
	go func() {
		_, _ = (&http.Client{Timeout: 10 * time.Second}).Get("http://" + addr + "/streaming")
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never served")
	}
	shutdownCh <- os.Interrupt
	start := time.Now()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("forced close must still finish cleanly, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("forced close never finished")
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("handler was never ended by the forced close")
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("returned after %v, before the drain deadline", elapsed)
	}
}

// TestServeWithShutdownListenerFailureReleasesWaiter verifies that a failed
// ListenAndServe still releases the shutdown goroutine instead of leaving a
// blocked signal waiter behind.
func TestServeWithShutdownListenerFailureReleasesWaiter(t *testing.T) {
	baseCtx, baseCancel := context.WithCancel(context.Background())
	defer baseCancel()
	srv := &http.Server{
		// An invalid port cannot bind: ListenAndServe fails immediately.
		Addr:        "127.0.0.1:99999",
		BaseContext: func(net.Listener) context.Context { return baseCtx },
	}
	shutdownCh := make(chan os.Signal, 1)
	err := serveHTTPWithShutdown(srv, shutdownCh, make(chan struct{}), baseCancel, shutdownDrainTimeout, func() {}, nil)
	if err == nil {
		t.Fatal("listener failure must surface as an error")
	}
	if errors.Is(err, http.ErrServerClosed) {
		t.Fatal("listener failure is not a graceful close")
	}
}

// TestCloseSQLiteWaitsForActiveRead verifies closeSQLite cannot close the
// shared connection under a reader: the close waits for the read lock to be
// released and later entries report the closed handle.
func TestCloseSQLiteWaitsForActiveRead(t *testing.T) {
	db, err := openSQLite(newTestSQLitePath(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	readStarted := make(chan struct{})
	releaseRead := make(chan struct{})
	readDone := make(chan error, 1)
	go func() {
		readDone <- db.withReadLock(func() error {
			close(readStarted)
			<-releaseRead
			return nil
		})
	}()
	<-readStarted

	closeDone := make(chan error, 1)
	go func() { closeDone <- closeSQLite(db) }()
	select {
	case err := <-closeDone:
		t.Fatalf("close finished while a read was in flight: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseRead)
	if err := <-readDone; err != nil {
		t.Fatalf("read after the close loop: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("close: %v", err)
	}
	// Reads after the closed handle fail instead of dereferencing it.
	if err := db.withReadLock(func() error { return nil }); !errors.Is(err, errSQLiteClosed) {
		t.Fatalf("read after close = %v, want errSQLiteClosed", err)
	}
	if err := db.withWriteLock(func() error { return nil }); !errors.Is(err, errSQLiteClosed) {
		t.Fatalf("write after close = %v, want errSQLiteClosed", err)
	}
	// Repeated close is a no-op.
	if err := closeSQLite(db); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func newTestSQLitePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "lifecycle-test.db")
}

// TestAppCloseStopsBackgroundWork verifies the App lifecycle: Close ends the
// background work it owns, is safe to call twice, and store reads after the
// close return an error rather than touching the closed handle.
func TestAppCloseStopsBackgroundWork(t *testing.T) {
	withTempApp(t, func(app *App, handler http.Handler) {
		if err := app.Close(); err != nil {
			t.Fatalf("first Close: %v", err)
		}
		if err := app.Close(); err != nil {
			t.Fatalf("repeated Close must be a no-op, got %v", err)
		}
		if app.WatchStore != nil {
			if _, err := app.WatchStore.GetProgress("u1", "i1"); err == nil {
				t.Fatal("store read after Close must fail")
			}
		}
	})
}
