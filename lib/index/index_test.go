package index

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewCreatesDatabaseInWALMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "index.sqlite")
	idx, err := New(path, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected database file: %v", err)
	}
	var mode string
	if err := idx.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, err=%v; want wal", mode, err)
	}
	var timeout int
	if err := idx.db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil || timeout != 5000 {
		t.Fatalf("busy_timeout = %d, err=%v; want 5000", timeout, err)
	}
}

func TestNewRejectsBadPath(t *testing.T) {
	for _, path := range []string{"", "relative/index.sqlite"} {
		if _, err := New(path, testLogger()); err == nil {
			t.Errorf("New(%q): expected error", path)
		}
	}
}

func TestSchemaSearchAndCascade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	idx, err := New(path, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	idx.Close()
	// Reopening must not fail: the schema is idempotent.
	idx, err = New(path, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := idx.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := idx.db.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	exec(`INSERT INTO notes(path, title, revision, size, mtime_ns) VALUES ('a.md', 'Gardening', 'r1', 10, 1)`)
	exec(`INSERT INTO chunks(path, seq, content) VALUES ('a.md', 0, 'tomatoes need sunlight')`)

	if n := count(`SELECT count(*) FROM chunks_fts WHERE chunks_fts MATCH 'sunlight'`); n != 1 {
		t.Fatalf("content matches = %d, want 1", n)
	}
	if n := count(`SELECT count(*) FROM notes_fts WHERE notes_fts MATCH 'gardening'`); n != 1 {
		t.Fatalf("title matches = %d, want 1", n)
	}

	exec(`DELETE FROM notes WHERE path = 'a.md'`)
	if n := count(`SELECT count(*) FROM chunks`); n != 0 {
		t.Fatalf("chunks after cascade = %d, want 0", n)
	}
	if n := count(`SELECT count(*) FROM chunks_fts WHERE chunks_fts MATCH 'sunlight'`); n != 0 {
		t.Fatalf("stale content matches = %d, want 0", n)
	}
	if n := count(`SELECT count(*) FROM notes_fts WHERE notes_fts MATCH 'gardening'`); n != 0 {
		t.Fatalf("stale title matches = %d, want 0", n)
	}
}

func TestAttachmentFilenameSearch(t *testing.T) {
	idx, err := New(filepath.Join(t.TempDir(), "index.sqlite"), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	if _, err := idx.db.Exec(`INSERT INTO attachments(path, name, size, mtime_ns) VALUES ('img/garden-plan.png', 'garden-plan.png', 5, 1)`); err != nil {
		t.Fatal(err)
	}
	match := func(q string) int {
		t.Helper()
		var n int
		if err := idx.db.QueryRow(`SELECT count(*) FROM attachments_fts WHERE attachments_fts MATCH ?`, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := match("plan"); n != 1 {
		t.Fatalf("matches = %d, want 1", n)
	}
	if _, err := idx.db.Exec(`DELETE FROM attachments`); err != nil {
		t.Fatal(err)
	}
	if n := match("plan"); n != 0 {
		t.Fatalf("matches after delete = %d, want 0", n)
	}
}

func mustNew(t *testing.T) *Index {
	t.Helper()
	idx, err := New(filepath.Join(t.TempDir(), "index.sqlite"), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

func TestNoteCRUD(t *testing.T) {
	idx := mustNew(t)
	ctx := context.Background()

	if _, err := idx.GetNote(ctx, "a.md"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetNote error = %v, want ErrNotFound", err)
	}
	n := Note{Path: "a.md", Title: "Gardening", Revision: "r1", Size: 10, MtimeNs: 1}
	if err := idx.PutNote(ctx, n, []string{"tomatoes need sunlight"}); err != nil {
		t.Fatal(err)
	}
	if got, err := idx.GetNote(ctx, "a.md"); err != nil || got != n {
		t.Fatalf("GetNote = %+v, %v; want %+v", got, err, n)
	}

	// Replacing drops the old chunks.
	n.Revision = "r2"
	if err := idx.PutNote(ctx, n, []string{"peppers like warmth"}); err != nil {
		t.Fatal(err)
	}
	if hits, _ := idx.SearchNotes(ctx, "sunlight", 10); len(hits) != 0 {
		t.Fatalf("stale hits: %+v", hits)
	}
	if hits, _ := idx.SearchNotes(ctx, "warmth", 10); len(hits) != 1 {
		t.Fatalf("hits = %+v, want 1", hits)
	}

	if err := idx.DeleteNote(ctx, "a.md"); err != nil {
		t.Fatal(err)
	}
	if err := idx.DeleteNote(ctx, "a.md"); err != nil {
		t.Fatalf("deleting absent note: %v", err)
	}
	if _, err := idx.GetNote(ctx, "a.md"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetNote after delete = %v, want ErrNotFound", err)
	}
}

func TestSearchNotesRanksTitleFirstAndDedupes(t *testing.T) {
	idx := mustNew(t)
	ctx := context.Background()
	put := func(path, title, content string) {
		t.Helper()
		rev, chunks, err := ChunkAndHash(strings.NewReader(content), DefaultChunkSize)
		if err != nil {
			t.Fatal(err)
		}
		if err := idx.PutNote(ctx, Note{Path: path, Title: title, Revision: rev}, chunks); err != nil {
			t.Fatal(err)
		}
	}
	put("body.md", "Other", "all about tomatoes\n\n"+strings.Repeat("filler ", 1000)+"\n\nmore tomatoes")
	put("title.md", "Tomatoes", "unrelated text\n\n"+strings.Repeat("filler ", 1000)+"\n\ntomatoes again")

	hits, err := idx.SearchNotes(ctx, "tomatoes", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].Path != "title.md" || hits[1].Path != "body.md" {
		t.Fatalf("hits = %+v, want title.md then body.md", hits)
	}
	if hits, _ := idx.SearchNotes(ctx, "tomatoes", 1); len(hits) != 1 {
		t.Fatalf("limit not applied: %+v", hits)
	}
	// Operators and quotes in user input must not break the query.
	if _, err := idx.SearchNotes(ctx, `foo" AND (bar`, 10); err != nil {
		t.Fatalf("punctuation query failed: %v", err)
	}
	if hits, err := idx.SearchNotes(ctx, "  ", 10); err != nil || hits != nil {
		t.Fatalf("blank query = %+v, %v; want nil", hits, err)
	}
}

func TestAttachmentCRUDAndSearch(t *testing.T) {
	idx := mustNew(t)
	ctx := context.Background()
	a := Attachment{Path: "img/garden-plan.png", Name: "garden-plan.png", Size: 5, MtimeNs: 1}
	for range 2 { // second put replaces the first
		if err := idx.PutAttachment(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	found, err := idx.SearchAttachments(ctx, "plan", 10)
	if err != nil || len(found) != 1 || found[0] != a {
		t.Fatalf("found = %+v, %v; want [%+v]", found, err, a)
	}
	if err := idx.DeleteAttachment(ctx, a.Path); err != nil {
		t.Fatal(err)
	}
	if found, _ := idx.SearchAttachments(ctx, "plan", 10); len(found) != 0 {
		t.Fatalf("found after delete: %+v", found)
	}
}

func TestPutNoteFailureKeepsPreviousVersion(t *testing.T) {
	idx := mustNew(t)
	ctx := context.Background()
	n := Note{Path: "a.md", Title: "Old", Revision: "r1"}
	if err := idx.PutNote(ctx, n, []string{"original text"}); err != nil {
		t.Fatal(err)
	}
	// A cancelled context fails the insert midway; the old version must survive.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	n.Title, n.Revision = "New", "r2"
	if err := idx.PutNote(cancelled, n, []string{"replacement"}); err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if got, err := idx.GetNote(ctx, "a.md"); err != nil || got.Revision != "r1" {
		t.Fatalf("GetNote = %+v, %v; want revision r1", got, err)
	}
	if hits, _ := idx.SearchNotes(ctx, "original", 10); len(hits) != 1 {
		t.Fatalf("hits = %+v, want the old content kept", hits)
	}
}

func TestStates(t *testing.T) {
	idx := mustNew(t)
	ctx := context.Background()
	n := Note{Path: "a.md", Title: "A", Revision: "r1", Size: 3, MtimeNs: 7}
	a := Attachment{Path: "x.png", Name: "x.png", Size: 4, MtimeNs: 8}
	if err := idx.PutNote(ctx, n, []string{"hello"}); err != nil {
		t.Fatal(err)
	}
	if err := idx.PutAttachment(ctx, a); err != nil {
		t.Fatal(err)
	}
	notes, err := idx.NoteStates(ctx)
	if err != nil || len(notes) != 1 || notes["a.md"] != n {
		t.Fatalf("NoteStates = %+v, %v", notes, err)
	}
	atts, err := idx.AttachmentStates(ctx)
	if err != nil || len(atts) != 1 || atts["x.png"] != a {
		t.Fatalf("AttachmentStates = %+v, %v", atts, err)
	}
}

func TestDeleteUnder(t *testing.T) {
	idx := mustNew(t)
	ctx := context.Background()
	for _, p := range []string{"a.md", "dir/b.md", "dir/sub/c.md", "dirx/d.md"} {
		if err := idx.PutNote(ctx, Note{Path: filepath.FromSlash(p), Title: "t", Revision: "r"}, []string{"word"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"dir/x.png", "other.png"} {
		if err := idx.PutAttachment(ctx, Attachment{Path: filepath.FromSlash(p), Name: "n"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := idx.DeleteUnder(ctx, "dir"); err != nil {
		t.Fatal(err)
	}
	notes, _ := idx.NoteStates(ctx)
	atts, _ := idx.AttachmentStates(ctx)
	if len(notes) != 2 || notes["a.md"].Path == "" || notes[filepath.FromSlash("dirx/d.md")].Path == "" {
		t.Fatalf("notes left = %+v, want a.md and dirx/d.md", notes)
	}
	if len(atts) != 1 || atts["other.png"].Path == "" {
		t.Fatalf("attachments left = %+v, want other.png", atts)
	}
	if hits, _ := idx.SearchNotes(ctx, "word", 10); len(hits) != 2 {
		t.Fatalf("hits = %+v, want 2 (chunks of removed notes gone)", hits)
	}
}
