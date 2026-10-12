package backend

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// sharedStores opens one SQLite connection and hands it to the three stores that share it,
// which is exactly how NewApp wires them.
func sharedStores(t *testing.T) (*IDStore, *UserStore, *WatchStore) {
	t.Helper()
	dir := t.TempDir()
	logger := NewLogger(LogConfig{Level: "error", FileLevel: "error", DataDir: dir})
	t.Cleanup(func() { _ = logger.Close() })

	db, err := openSQLite(filepath.Join(dir, "shared.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = closeSQLite(db) })

	users, err := NewUserStore(db, logger)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	watch, err := NewWatchStore(db, logger)
	if err != nil {
		t.Fatalf("NewWatchStore: %v", err)
	}
	idStore := &IDStore{db: db, logger: logger, virtualToOriginal: map[string]*idEntry{}, originalToVirtual: map[string]string{}, originalIDToVirtual: map[string][]string{}, activeStreamServer: map[string]activeStreamEntry{}}
	return idStore, users, watch
}

// TestConcurrentWriteSurvivesTransactionRollback is the regression for the shared-connection
// transaction interference: a SQLite transaction belongs to the connection, not to the store
// that began it. With one connection behind the ID store, the user store and the watch store,
// an autocommit write from one of them used to land inside another's open transaction — and
// then disappeared with its ROLLBACK, even though the write had already reported success.
func TestConcurrentWriteSurvivesTransactionRollback(t *testing.T) {
	idStore, _, watch := sharedStores(t)

	entered := make(chan struct{})
	release := make(chan struct{})
	txDone := make(chan error, 1)
	go func() {
		txDone <- idStore.db.withWriteTx(func() error {
			close(entered)
			<-release
			return errors.New("injected failure: roll this transaction back")
		})
	}()
	<-entered

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- watch.RecordProgress(&WatchProgress{
			ProxyUserID: "user-1", VirtualItemID: "item-1", PositionTicks: 42,
		})
	}()

	// Give the write every chance to run while the transaction is still open. With the write
	// lock held it cannot, and the channel stays quiet until the rollback below.
	ranInsideTransaction := false
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("watch write failed: %v", err)
		}
		ranInsideTransaction = true
	case <-time.After(300 * time.Millisecond):
	}

	close(release)
	if err := <-txDone; err == nil {
		t.Fatal("expected the injected transaction failure")
	}
	if !ranInsideTransaction {
		if err := <-writeDone; err != nil {
			t.Fatalf("watch write failed: %v", err)
		}
	}

	progress, _ := watch.GetProgress("user-1", "item-1")
	if progress == nil || progress.PositionTicks != 42 {
		t.Fatalf("the watch write was lost with the rolled-back transaction: %#v", progress)
	}
}

// TestWriteLockSerializesStoresAcrossATransaction states the same guarantee directly: while
// one store holds an open transaction on the shared connection, another store's write waits.
func TestWriteLockSerializesStoresAcrossATransaction(t *testing.T) {
	idStore, users, _ := sharedStores(t)

	entered := make(chan struct{})
	release := make(chan struct{})
	txDone := make(chan error, 1)
	go func() {
		txDone <- idStore.db.withWriteTx(func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	writeDone := make(chan error, 1)
	go func() {
		_, err := users.Create("carol", "carol1234", []string{"srv-0"})
		writeDone <- err
	}()

	select {
	case err := <-writeDone:
		t.Fatalf("a write from another store ran while a transaction was open on the shared connection (err=%v)", err)
	case <-time.After(300 * time.Millisecond):
	}

	close(release)
	if err := <-txDone; err != nil {
		t.Fatalf("transaction: %v", err)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("user create: %v", err)
	}
	if user := users.GetByUsername("carol"); user == nil {
		t.Fatal("the user create did not commit after the transaction finished")
	}
}

// TestServerRemovalWritesWaitForOpenTransaction is the regression for the three
// server/user removal writes that bypassed the shared connection's write lock:
// UserStore.RemoveServerGrants, HiddenLibraryStore.RemoveServer and
// HiddenLibraryStore.RemoveUser issued their DELETE as an autocommit statement,
// which lands inside any transaction another store has open on the shared
// connection and disappears with its rollback. They must wait like every other
// write.
func TestServerRemovalWritesWaitForOpenTransaction(t *testing.T) {
	idStore, users, _ := sharedStores(t)
	db := idStore.db
	logger := NewLogger(LogConfig{Level: "error", FileLevel: "error", DataDir: t.TempDir()})
	t.Cleanup(func() { _ = logger.Close() })
	hidden, err := NewHiddenLibraryStore(db, logger)
	if err != nil {
		t.Fatalf("NewHiddenLibraryStore: %v", err)
	}

	if _, err := users.Create("dave", "dave1234", []string{"srv-0"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	dave := users.GetByUsername("dave")
	if err := hidden.SetServerHidden(dave.ID, "srv-0", []string{"lib-1"}); err != nil {
		t.Fatalf("set hidden: %v", err)
	}

	assertWaitsForTransaction := func(name string, write func() error) {
		t.Helper()
		entered := make(chan struct{})
		release := make(chan struct{})
		txDone := make(chan error, 1)
		go func() {
			txDone <- db.withWriteTx(func() error {
				close(entered)
				<-release
				return nil
			})
		}()
		<-entered

		writeDone := make(chan error, 1)
		go func() { writeDone <- write() }()

		select {
		case err := <-writeDone:
			t.Fatalf("%s ran while a transaction was open on the shared connection (err=%v)", name, err)
		case <-time.After(300 * time.Millisecond):
		}
		close(release)
		if err := <-txDone; err != nil {
			t.Fatalf("transaction: %v", err)
		}
		if err := <-writeDone; err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	assertWaitsForTransaction("RemoveServerGrants", func() error { return users.RemoveServerGrants("srv-0") })
	assertWaitsForTransaction("HiddenLibraryStore.RemoveServer", func() error { return hidden.RemoveServer("srv-0") })
	assertWaitsForTransaction("HiddenLibraryStore.RemoveUser", func() error { return hidden.RemoveUser(dave.ID) })

	if got := users.GetByUsername("dave"); got != nil && len(got.AllowedServers) != 0 {
		t.Fatalf("RemoveServerGrants did not commit: %#v", got.AllowedServers)
	}
}
 
 // TestReadWaitsForOpenTransaction is the read-side regression for the shared
 // connection: a SELECT stepping rows on the same connection an open
 // transaction belongs to runs inside that transaction's scope and observes
 // its uncommitted rows. WatchStore reads must take the connection read lock
 // so they wait for the transaction instead.
 func TestReadWaitsForOpenTransaction(t *testing.T) {
 	idStore, _, watch := sharedStores(t)
 	db := idStore.db
 
 	entered := make(chan struct{})
 	release := make(chan struct{})
 	txDone := make(chan error, 1)
 	go func() {
 		txDone <- db.withWriteTx(func() error {
 			// An uncommitted row only this transaction can see.
 			if err := db.execParams(
 				`INSERT INTO user_watch_progress (proxy_user_id, virtual_item_id, server_id, position_ticks)
 				 VALUES ('user-1', 'item-tx', 'srv-0', 99)`,
 			); err != nil {
 				return err
 			}
 			close(entered)
 			<-release
 			return nil
 		})
 	}()
 	<-entered
 
 	type readResult struct {
 		progress *WatchProgress
 		err      error
 	}
 	readDone := make(chan readResult, 1)
 	go func() {
 		progress, err := watch.GetProgress("user-1", "item-tx")
 		readDone <- readResult{progress: progress, err: err}
 	}()
 
 	// The read must not finish while the transaction is still open: completing
 	// here means it stepped rows inside the transaction and observed the
  // uncommitted insert.
 	select {
 	case got := <-readDone:
 		t.Fatalf("read completed inside the open transaction: %#v (err=%v)", got.progress, got.err)
 	case <-time.After(300 * time.Millisecond):
 	}
 
 	close(release)
 	if err := <-txDone; err != nil {
 		t.Fatalf("transaction: %v", err)
 	}
 	got := <-readDone
 	if got.err != nil {
 		t.Fatalf("read: %v", got.err)
 	}
 	if got.progress == nil || got.progress.PositionTicks != 99 {
 		t.Fatalf("read after commit = %#v, want the committed row", got.progress)
 	}
 }
