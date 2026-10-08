package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func quarantined(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "index.sqlite.corrupt-*"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// fill creates an index at path with some notes and closes it, leaving a complete file.
func fill(t *testing.T, path string, n int) {
	t.Helper()
	idx, err := New(path, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		name := filepath.Join("dir", strings.Repeat("n", i+1)+".md")
		if err := idx.PutNote(bg, Note{Path: name, Title: "t", Revision: "r"}, []string{strings.Repeat("filler words ", 200)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := idx.Close(); err != nil {
		t.Fatal(err)
	}
}

var bg = context.Background()

func TestNewRecoversFromCorruptDatabase(t *testing.T) {
	for name, damage := range map[string]func(t *testing.T, path string){
		"garbage file": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(strings.Repeat("this is not a database ", 500)), 0644); err != nil {
				t.Fatal(err)
			}
		},
		"truncated file": func(t *testing.T, path string) {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(path, info.Size()/2); err != nil {
				t.Fatal(err)
			}
		},
		"zeroed header": func(t *testing.T, path string) {
			f, err := os.OpenFile(path, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := f.WriteAt(make([]byte, 100), 0); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "index.sqlite")
			fill(t, path, 40)
			damage(t, path)

			idx, err := New(path, testLogger())
			if err != nil {
				t.Fatalf("New on a damaged index: %v", err)
			}
			defer idx.Close()

			if got := quarantined(t, dir); len(got) != 1 {
				t.Fatalf("quarantined files = %v, want exactly one", got)
			}
			if states, err := idx.NoteStates(bg); err != nil || len(states) != 0 {
				t.Fatalf("recovered index should be empty: %d notes, err %v", len(states), err)
			}
			// ... and fully usable.
			if err := idx.PutNote(bg, Note{Path: "a.md", Title: "A", Revision: "r"}, []string{"hello world"}); err != nil {
				t.Fatal(err)
			}
			if hits, err := idx.SearchNotes(bg, "hello", 10); err != nil || len(hits) != 1 {
				t.Fatalf("search after recovery: %+v, %v", hits, err)
			}
			if err := idx.Check(bg); err != nil {
				t.Fatalf("Check after recovery: %v", err)
			}
		})
	}
}

func TestNewKeepsHealthyDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.sqlite")
	fill(t, path, 5)

	idx, err := New(path, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	if states, _ := idx.NoteStates(bg); len(states) != 5 {
		t.Fatalf("notes after reopen = %d, want 5", len(states))
	}
	if got := quarantined(t, dir); len(got) != 0 {
		t.Fatalf("healthy database was quarantined: %v", got)
	}
}

func TestNewDoesNotQuarantineWhenDatabaseIsUnreachable(t *testing.T) {
	dir := t.TempDir()
	// A directory where the database should be: unusable, but not "corrupt".
	if err := os.Mkdir(filepath.Join(dir, "index.sqlite"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(filepath.Join(dir, "index.sqlite"), testLogger()); err == nil {
		t.Fatal("expected an error")
	}
	if got := quarantined(t, dir); len(got) != 0 {
		t.Fatalf("an unreachable database was quarantined: %v", got)
	}
	if info, err := os.Stat(filepath.Join(dir, "index.sqlite")); err != nil || !info.IsDir() {
		t.Fatalf("the original path was disturbed: %v", err)
	}
}

func TestReplaceWithSwapsDatabaseAndKeepsOldOnFailure(t *testing.T) {
	dir := t.TempDir()
	livePath := filepath.Join(dir, "index.sqlite")
	newPath := filepath.Join(dir, "index.sqlite.new")

	live, err := New(livePath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if err := live.PutNote(bg, Note{Path: "old.md", Title: "Old", Revision: "r"}, []string{"oldword"}); err != nil {
		t.Fatal(err)
	}

	// A missing replacement leaves the live index working.
	if err := live.ReplaceWith(bg, filepath.Join(dir, "missing.sqlite")); err == nil {
		t.Fatal("expected an error for a missing replacement")
	}
	if hits, err := live.SearchNotes(bg, "oldword", 10); err != nil || len(hits) != 1 {
		t.Fatalf("live index broken after failed swap: %+v, %v", hits, err)
	}

	// A good replacement takes over.
	fresh, err := New(newPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.PutNote(bg, Note{Path: "new.md", Title: "New", Revision: "r"}, []string{"newword"}); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}
	if err := live.ReplaceWith(bg, newPath); err != nil {
		t.Fatal(err)
	}
	if hits, _ := live.SearchNotes(bg, "oldword", 10); len(hits) != 0 {
		t.Fatalf("old content survived the swap: %+v", hits)
	}
	if hits, _ := live.SearchNotes(bg, "newword", 10); len(hits) != 1 {
		t.Fatalf("new content missing after the swap: %+v", hits)
	}
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Fatalf("replacement file should have been moved: %v", err)
	}
	if err := live.Check(bg); err != nil {
		t.Fatal(err)
	}
}
