package indexer

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestReconcileSyncsPeriodically(t *testing.T) {
	x, idx, knowledge := setup(t)
	ctx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() { done <- x.Reconcile(ctx, 50*time.Millisecond) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Reconcile: %v", err)
		}
	})

	write(t, filepath.Join(knowledge, "late.md"), "arrived while nobody watched")
	eventually(t, "reconcile picks up the note", func() bool {
		hits, _ := idx.SearchNotes(bg, "arrived", 10)
		return len(hits) == 1
	})
}

func TestReconcileRejectsBadInterval(t *testing.T) {
	x, _, _ := setup(t)
	if err := x.Reconcile(bg, 0); err == nil {
		t.Fatal("expected error for zero interval")
	}
}
