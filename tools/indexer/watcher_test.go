package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// startWatch runs Watch in the background and returns once it is watching.
func startWatch(t *testing.T, x *Indexer) {
	t.Helper()
	x.debounce = 50 * time.Millisecond
	ready := make(chan struct{})
	x.onWatching = func() { close(ready) }
	ctx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() { done <- x.Watch(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Watch: %v", err)
		}
	})
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("Watch returned early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not start")
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func TestWatchFollowsFileChanges(t *testing.T) {
	x, idx, knowledge := setup(t)
	startWatch(t, x)
	found := func(q string) bool {
		hits, _ := idx.SearchNotes(bg, q, 10)
		return len(hits) > 0
	}

	// New note.
	note := filepath.Join(knowledge, "garden.md")
	write(t, note, "tomatoes need sunlight")
	eventually(t, "new note searchable", func() bool { return found("sunlight") })

	// Edited note.
	write(t, note, "tomatoes need water")
	eventually(t, "edit searchable", func() bool { return found("water") && !found("sunlight") })

	// New attachment.
	write(t, filepath.Join(knowledge, "garden-plan.png"), "png")
	eventually(t, "attachment searchable", func() bool {
		a, _ := idx.SearchAttachments(bg, "plan", 10)
		return len(a) == 1
	})

	// Deleted note.
	if err := os.Remove(note); err != nil {
		t.Fatal(err)
	}
	eventually(t, "deleted note gone", func() bool { return !found("water") })
}

func TestWatchFollowsFolders(t *testing.T) {
	x, idx, knowledge := setup(t)
	startWatch(t, x)
	found := func(q string) bool {
		hits, _ := idx.SearchNotes(bg, q, 10)
		return len(hits) > 0
	}

	// A new folder is watched, including a note created inside it afterwards.
	write(t, filepath.Join(knowledge, "projects", "a.md"), "alpha")
	eventually(t, "note in new folder", func() bool { return found("alpha") })
	write(t, filepath.Join(knowledge, "projects", "b.md"), "bravo")
	eventually(t, "second note in new folder", func() bool { return found("bravo") })

	// Renaming the folder moves its notes.
	if err := os.Rename(filepath.Join(knowledge, "projects"), filepath.Join(knowledge, "archive")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "renamed folder indexed under new path", func() bool {
		hits, _ := idx.SearchNotes(bg, "alpha", 10)
		return len(hits) == 1 && hits[0].Path == filepath.Join("archive", "a.md")
	})
	eventually(t, "old folder paths gone", func() bool {
		notes, _ := idx.NoteStates(bg)
		_, old := notes[filepath.Join("projects", "a.md")]
		return !old && len(notes) == 2
	})

	// Removing the folder removes its notes.
	if err := os.RemoveAll(filepath.Join(knowledge, "archive")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "folder removal", func() bool { return !found("alpha") && !found("bravo") })
}

func TestWatchIgnoresHidden(t *testing.T) {
	x, idx, knowledge := setup(t)
	startWatch(t, x)
	write(t, filepath.Join(knowledge, ".obsidian", "workspace.md"), "hidden")
	write(t, filepath.Join(knowledge, "visible.md"), "shown")
	eventually(t, "visible note", func() bool {
		hits, _ := idx.SearchNotes(bg, "shown", 10)
		return len(hits) == 1
	})
	if notes, _ := idx.NoteStates(bg); len(notes) != 1 {
		t.Fatalf("notes = %+v, want only visible.md", notes)
	}
}
