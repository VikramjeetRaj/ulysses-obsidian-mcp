// Package indexer fills and refreshes the search index from the files under <vault>/Knowledge/.
package indexer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/VikramjeetRaj/ulysses-obsidian-mcp/lib/index"
)

const defaultDebounce = 500 * time.Millisecond

// Indexer keeps an Index in step with the Knowledge directory.
type Indexer struct {
	knowledge string
	index     *index.Index
	log       *slog.Logger

	mu         sync.Mutex // serializes Sync, Reindex's final step and watcher updates
	reindexing sync.Mutex // held while a Reindex runs, so only one runs at a time
	debounce   time.Duration
	onWatching func() // called once the watcher is set up; used by tests
}

// Stats counts what a Sync did.
type Stats struct {
	NotesIndexed         int
	NotesUnchanged       int
	NotesRemoved         int
	AttachmentsIndexed   int
	AttachmentsUnchanged int
	AttachmentsRemoved   int
}

// New returns an Indexer for the Knowledge directory at knowledge.
func New(knowledge string, idx *index.Index, log *slog.Logger) (*Indexer, error) {
	resolved, err := filepath.EvalSymlinks(knowledge)
	if err != nil {
		return nil, fmt.Errorf("resolve Knowledge directory: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("knowledge directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("knowledge must be a directory: %s", knowledge)
	}
	return &Indexer{knowledge: resolved, index: idx, log: log, debounce: defaultDebounce}, nil
}

// Sync walks Knowledge/ and brings the index in line with it: new and changed files are
// indexed, unchanged ones skipped (same size and modification time), and rows for files that
// no longer exist are removed. Markdown files are notes; every other regular file is an
// attachment (only its name is indexed). Symlinks and dot-files or dot-folders are skipped.
// A file that fails to index does not stop the walk; the errors are joined and returned
// together with the stats.
func (x *Indexer) Sync(ctx context.Context) (Stats, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.syncInto(ctx, x.index)
}

// syncInto is Sync against any index. The caller holds x.mu if idx is the live index.
func (x *Indexer) syncInto(ctx context.Context, idx *index.Index) (Stats, error) {
	var stats Stats
	notes, err := idx.NoteStates(ctx)
	if err != nil {
		return stats, err
	}
	attachments, err := idx.AttachmentStates(ctx)
	if err != nil {
		return stats, err
	}

	var errs []error
	err = x.walk(ctx, x.knowledge, func(full string, d fs.DirEntry) error {
		if d.IsDir() {
			return nil
		}
		rel, info, err := x.statFile(full, d)
		if err != nil {
			return err
		}
		unchanged := func(size, mtime int64) bool {
			return size == info.Size() && mtime == info.ModTime().UnixNano()
		}
		if isNote(rel) {
			old, seen := notes[rel]
			delete(notes, rel)
			if seen && unchanged(old.Size, old.MtimeNs) {
				stats.NotesUnchanged++
				return nil
			}
			stats.NotesIndexed++
		} else {
			old, seen := attachments[rel]
			delete(attachments, rel)
			if seen && unchanged(old.Size, old.MtimeNs) {
				stats.AttachmentsUnchanged++
				return nil
			}
			stats.AttachmentsIndexed++
		}
		return x.putFile(ctx, idx, full, rel, info)
	})
	if ctx.Err() != nil {
		return stats, ctx.Err()
	}
	if err != nil {
		errs = append(errs, err)
	}

	// Whatever was indexed but not seen on disk is gone.
	for path := range notes {
		if err := idx.DeleteNote(ctx, path); err != nil {
			errs = append(errs, err)
			continue
		}
		stats.NotesRemoved++
	}
	for path := range attachments {
		if err := idx.DeleteAttachment(ctx, path); err != nil {
			errs = append(errs, err)
			continue
		}
		stats.AttachmentsRemoved++
	}

	x.log.Info("index synced",
		"notes_indexed", stats.NotesIndexed, "notes_unchanged", stats.NotesUnchanged, "notes_removed", stats.NotesRemoved,
		"attachments_indexed", stats.AttachmentsIndexed, "attachments_unchanged", stats.AttachmentsUnchanged,
		"attachments_removed", stats.AttachmentsRemoved)
	return stats, errors.Join(errs...)
}

// walk visits root and everything under it that should be indexed or watched: directories and
// regular files, skipping dot-files, dot-folders and symlinks. An error from visit or from
// reading an entry is collected and the walk continues; the collected errors are returned
// joined. Cancellation stops the walk at once.
func (x *Indexer) walk(ctx context.Context, root string, visit func(full string, d fs.DirEntry) error) error {
	var errs []error
	err := filepath.WalkDir(root, func(full string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			errs = append(errs, err)
			return nil
		}
		if full != x.knowledge && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			return nil // symlinks and special files are ignored
		}
		if err := visit(full, d); err != nil {
			errs = append(errs, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return errors.Join(errs...)
}

// statFile returns the path of full relative to Knowledge/ and its file info.
func (x *Indexer) statFile(full string, d fs.DirEntry) (string, fs.FileInfo, error) {
	rel, err := filepath.Rel(x.knowledge, full)
	if err != nil {
		return "", nil, err
	}
	info, err := d.Info()
	if err != nil {
		return "", nil, fmt.Errorf("stat %s: %w", rel, err)
	}
	return rel, info, nil
}

func isNote(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".md")
}

// putFile writes the file at full to the index, as a note or an attachment. Indexing the same
// file again is harmless. info must come from before the file is read, so a change made during
// the read is picked up by the next update.
func (x *Indexer) putFile(ctx context.Context, idx *index.Index, full, rel string, info fs.FileInfo) error {
	if !isNote(rel) {
		return idx.PutAttachment(ctx, index.Attachment{
			Path: rel, Name: filepath.Base(rel), Size: info.Size(), MtimeNs: info.ModTime().UnixNano(),
		})
	}
	// Read and chunk first, then write to the index in one short step.
	file, err := os.Open(full)
	if err != nil {
		return fmt.Errorf("index %s: %w", rel, err)
	}
	defer file.Close()
	revision, chunks, err := index.ChunkAndHash(file, index.DefaultChunkSize)
	if err != nil {
		return fmt.Errorf("index %s: %w", rel, err)
	}
	return idx.PutNote(ctx, index.Note{
		Path:     rel,
		Title:    strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel)),
		Revision: revision,
		Size:     info.Size(),
		MtimeNs:  info.ModTime().UnixNano(),
	}, chunks)
}
