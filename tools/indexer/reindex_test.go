package indexer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/VikramjeetRaj/ulysses-obsidian-mcp/lib/index"
)

func TestReindexFixesContentSyncWouldNotNotice(t *testing.T) {
	x, idx, knowledge := setup(t)
	write(t, filepath.Join(knowledge, "garden.md"), "tomatoes need sunlight")
	write(t, filepath.Join(knowledge, "sub", "peppers.md"), "peppers like warmth")
	write(t, filepath.Join(knowledge, "img", "plan.png"), "png")
	runSync(t, x)

	// Corrupt the live content through the index itself: replace a note's chunks with wrong text
	// but keep its size and mtime, which is all Sync looks at.
	states, err := idx.NoteStates(bg)
	if err != nil {
		t.Fatal(err)
	}
	stale := states["garden.md"]
	if err := idx.PutNote(bg, stale, []string{"ghostword"}); err != nil {
		t.Fatal(err)
	}
	if got := runSync(t, x); got.NotesIndexed != 0 {
		t.Fatalf("Sync noticed the tampering (%+v); the test premise is wrong", got)
	}
	if hits, _ := idx.SearchNotes(bg, "ghostword", 10); len(hits) != 1 {
		t.Fatalf("tampering not visible: %+v", hits)
	}

	stats, err := x.Reindex(bg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.NotesIndexed != 2 || stats.AttachmentsIndexed != 1 {
		t.Fatalf("stats = %+v, want 2 notes and 1 attachment", stats)
	}
	if hits, _ := idx.SearchNotes(bg, "ghostword", 10); len(hits) != 0 {
		t.Fatalf("stale content survived the reindex: %+v", hits)
	}
	if hits, _ := idx.SearchNotes(bg, "sunlight", 10); len(hits) != 1 || hits[0].Path != "garden.md" {
		t.Fatalf("real content missing after reindex: %+v", hits)
	}
	if found, _ := idx.SearchAttachments(bg, "plan", 10); len(found) != 1 {
		t.Fatalf("attachment missing after reindex: %+v", found)
	}
	if _, err := os.Stat(idx.Path() + ".new"); !os.IsNotExist(err) {
		t.Fatalf("temporary generation left behind: %v", err)
	}
}

func TestReindexDropsGhostsAndKeepsWorking(t *testing.T) {
	x, idx, knowledge := setup(t)
	write(t, filepath.Join(knowledge, "a.md"), "alpha")
	runSync(t, x)

	// A row for a file that does not exist.
	if err := idx.PutNote(bg, index.Note{Path: "ghost.md", Title: "ghost", Revision: "r"}, []string{"phantom"}); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Reindex(bg); err != nil {
		t.Fatal(err)
	}
	if hits, _ := idx.SearchNotes(bg, "phantom", 10); len(hits) != 0 {
		t.Fatalf("ghost note survived: %+v", hits)
	}
	// The index must stay writable after the swap, and further syncs must work.
	write(t, filepath.Join(knowledge, "b.md"), "bravo")
	if got := runSync(t, x); got.NotesIndexed != 1 {
		t.Fatalf("sync after reindex = %+v", got)
	}
	if hits, _ := idx.SearchNotes(bg, "bravo", 10); len(hits) != 1 {
		t.Fatalf("new note not searchable after reindex: %+v", hits)
	}
}

func TestReindexFailureKeepsPreviousIndex(t *testing.T) {
	x, idx, knowledge := setup(t)
	write(t, filepath.Join(knowledge, "a.md"), "alpha")
	runSync(t, x)

	ctx, cancel := context.WithCancel(bg)
	cancel()
	if _, err := x.Reindex(ctx); err == nil {
		t.Fatal("expected an error from a cancelled reindex")
	}
	if hits, err := idx.SearchNotes(bg, "alpha", 10); err != nil || len(hits) != 1 {
		t.Fatalf("live index damaged by a failed reindex: %+v, %v", hits, err)
	}
	if _, err := os.Stat(idx.Path() + ".new"); !os.IsNotExist(err) {
		t.Fatalf("temporary generation left behind: %v", err)
	}
}

func TestReindexRefusesToRunTwice(t *testing.T) {
	x, _, _ := setup(t)
	x.reindexing.Lock()
	defer x.reindexing.Unlock()
	if _, err := x.Reindex(bg); !errors.Is(err, ErrReindexRunning) {
		t.Fatalf("error = %v, want ErrReindexRunning", err)
	}
}
