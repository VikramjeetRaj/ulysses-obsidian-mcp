package indexer

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/VikramjeetRaj/ulysses-obsidian-mcp/lib/index"
)

var bg = context.Background()

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func setup(t *testing.T) (*Indexer, *index.Index, string) {
	t.Helper()
	root := t.TempDir()
	knowledge := filepath.Join(root, "Knowledge")
	if err := os.MkdirAll(knowledge, 0755); err != nil {
		t.Fatal(err)
	}
	idx, err := index.New(filepath.Join(root, "state", "index.sqlite"), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	x, err := New(knowledge, idx, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	return x, idx, knowledge
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func runSync(t *testing.T, x *Indexer) Stats {
	t.Helper()
	stats, err := x.Sync(bg)
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

func TestSyncLifecycle(t *testing.T) {
	x, idx, knowledge := setup(t)
	write(t, filepath.Join(knowledge, "garden.md"), "tomatoes need sunlight")
	write(t, filepath.Join(knowledge, "sub", "deep.MD"), "peppers like warmth")
	write(t, filepath.Join(knowledge, "img", "garden-plan.png"), "png")

	if got := runSync(t, x); got != (Stats{NotesIndexed: 2, AttachmentsIndexed: 1}) {
		t.Fatalf("first sync = %+v", got)
	}
	if hits, _ := idx.SearchNotes(bg, "sunlight", 10); len(hits) != 1 || hits[0].Path != "garden.md" || hits[0].Title != "garden" {
		t.Fatalf("hits = %+v", hits)
	}
	if hits, _ := idx.SearchNotes(bg, "warmth", 10); len(hits) != 1 || hits[0].Path != filepath.Join("sub", "deep.MD") {
		t.Fatalf("hits = %+v", hits)
	}
	if found, _ := idx.SearchAttachments(bg, "plan", 10); len(found) != 1 {
		t.Fatalf("attachments = %+v", found)
	}

	// Nothing changed.
	if got := runSync(t, x); got != (Stats{NotesUnchanged: 2, AttachmentsUnchanged: 1}) {
		t.Fatalf("second sync = %+v", got)
	}

	// Edit one note (new mtime), delete the other note and the attachment.
	edited := filepath.Join(knowledge, "garden.md")
	write(t, edited, "tomatoes need water")
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(edited, later, later); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(knowledge, "sub", "deep.MD"))
	os.Remove(filepath.Join(knowledge, "img", "garden-plan.png"))
	if got := runSync(t, x); got != (Stats{NotesIndexed: 1, NotesRemoved: 1, AttachmentsRemoved: 1}) {
		t.Fatalf("third sync = %+v", got)
	}
	if hits, _ := idx.SearchNotes(bg, "sunlight", 10); len(hits) != 0 {
		t.Fatalf("stale hits = %+v", hits)
	}
	if hits, _ := idx.SearchNotes(bg, "water", 10); len(hits) != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	if hits, _ := idx.SearchNotes(bg, "warmth", 10); len(hits) != 0 {
		t.Fatalf("removed note still found: %+v", hits)
	}
	if found, _ := idx.SearchAttachments(bg, "plan", 10); len(found) != 0 {
		t.Fatalf("removed attachment still found: %+v", found)
	}
}

func TestSyncSkipsHiddenAndSymlinks(t *testing.T) {
	x, _, knowledge := setup(t)
	outside := filepath.Join(filepath.Dir(knowledge), "outside.md")
	write(t, outside, "secret")
	write(t, filepath.Join(knowledge, ".obsidian", "workspace.md"), "hidden")
	write(t, filepath.Join(knowledge, ".DS_Store"), "junk")
	write(t, filepath.Join(knowledge, "ok.md"), "fine")
	if err := os.Symlink(outside, filepath.Join(knowledge, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(knowledge, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if got := runSync(t, x); got != (Stats{NotesIndexed: 1}) {
		t.Fatalf("sync = %+v, want only ok.md", got)
	}
}

func TestSyncHonoursCancelledContext(t *testing.T) {
	x, _, knowledge := setup(t)
	write(t, filepath.Join(knowledge, "a.md"), "text")
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if _, err := x.Sync(ctx); err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestNewRejectsMissingKnowledge(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "missing"), nil, testLogger()); err == nil {
		t.Fatal("expected error")
	}
}
