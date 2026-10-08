package indexer

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/VikramjeetRaj/ulysses-obsidian-mcp/lib/index"
)

// ErrReindexRunning is returned when a Reindex is already in progress.
var ErrReindexRunning = errors.New("a reindex is already running")

// Reindex rebuilds the whole index from the files on disk, as a new generation:
//
//  1. Build a complete second index file beside the live one while searches and the watcher
//     carry on using the live index.
//  2. Pause index updates, run the sync again on the new index to pick up anything edited
//     during step 1, and validate it (integrity check and counts that match the disk).
//  3. Switch the live index over to the new file.
//
// If any step fails the live index is left exactly as it was. The returned stats count what
// the new index contains (NotesIndexed, AttachmentsIndexed) and what vanished during the build.
func (x *Indexer) Reindex(ctx context.Context) (Stats, error) {
	if !x.reindexing.TryLock() {
		return Stats{}, ErrReindexRunning
	}
	defer x.reindexing.Unlock()

	newPath := x.index.Path() + ".new"
	removeIndexFiles(newPath)
	defer removeIndexFiles(newPath) // after a successful swap there is nothing left; after a failure this cleans up

	fresh, err := index.New(newPath, x.log)
	if err != nil {
		return Stats{}, fmt.Errorf("create new index generation: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			fresh.Close()
		}
	}()

	// Step 1: the slow part, without blocking anyone.
	if _, err := x.syncInto(ctx, fresh); err != nil {
		return Stats{}, fmt.Errorf("build new index generation: %w", err)
	}

	// Step 2: catch up on edits made during the build, with index updates paused.
	x.mu.Lock()
	defer x.mu.Unlock()
	last, err := x.syncInto(ctx, fresh)
	if err != nil {
		return Stats{}, fmt.Errorf("catch up new index generation: %w", err)
	}
	if err := fresh.Check(ctx); err != nil {
		return Stats{}, fmt.Errorf("validate new index generation: %w", err)
	}
	notes, err := fresh.NoteStates(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("validate new index generation: %w", err)
	}
	attachments, err := fresh.AttachmentStates(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("validate new index generation: %w", err)
	}
	if want := last.NotesIndexed + last.NotesUnchanged; len(notes) != want {
		return Stats{}, fmt.Errorf("validate new index generation: %d notes indexed, %d on disk", len(notes), want)
	}
	if want := last.AttachmentsIndexed + last.AttachmentsUnchanged; len(attachments) != want {
		return Stats{}, fmt.Errorf("validate new index generation: %d attachments indexed, %d on disk", len(attachments), want)
	}

	// Step 3: switch. The new file must be closed first so it is complete.
	closed = true
	if err := fresh.Close(); err != nil {
		return Stats{}, fmt.Errorf("finish new index generation: %w", err)
	}
	if err := x.index.ReplaceWith(ctx, newPath); err != nil {
		return Stats{}, err
	}

	stats := Stats{
		NotesIndexed:       len(notes),
		NotesRemoved:       last.NotesRemoved,
		AttachmentsIndexed: len(attachments),
		AttachmentsRemoved: last.AttachmentsRemoved,
	}
	x.log.Info("index rebuilt", "notes", stats.NotesIndexed, "attachments", stats.AttachmentsIndexed)
	return stats, nil
}

// removeIndexFiles deletes a database file and its WAL and shared-memory files.
func removeIndexFiles(path string) {
	os.Remove(path)
	os.Remove(path + "-wal")
	os.Remove(path + "-shm")
}
