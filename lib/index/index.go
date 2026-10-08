// Package index holds the persistent search index for the notes under <vault>/Knowledge/.
// The Markdown files stay the source of truth; the index is rebuildable.
package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

// ErrNotFound is returned when a note or attachment is not in the index.
var ErrNotFound = errors.New("not found in index")

const (
	defaultSearchLimit = 20
	maxSearchLimit     = 100
)

// Note is the indexed metadata of a Markdown note.
type Note struct {
	Path     string
	Title    string
	Revision string
	Size     int64
	MtimeNs  int64
}

// Attachment is the indexed metadata of an attachment file.
type Attachment struct {
	Path    string
	Name    string
	Size    int64
	MtimeNs int64
}

// Hit is a search result. For notes, Excerpt is a short piece of the matching text.
type Hit struct {
	Path    string
	Title   string
	Excerpt string
}

// Index is the search index, backed by a SQLite database.
type Index struct {
	path string
	log  *slog.Logger

	mu sync.RWMutex // read-locked by every operation; write-locked while the database is swapped
	db *sql.DB
}

// New opens (creating if needed) the SQLite database at path in WAL mode with a busy timeout,
// so several server processes can share it. The database must live outside <vault>/Knowledge/.
func New(path string, log *slog.Logger) (*Index, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("index path must be an absolute path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create index directory: %w", err)
	}
	db, err := openDB(path)
	if err != nil && isCorrupt(err) {
		// The index is only a cache of the notes, so a damaged one is set aside and replaced
		// by an empty one. The caller refills it with a Sync.
		moved, qerr := quarantine(path)
		if qerr != nil {
			return nil, fmt.Errorf("index %s is corrupt (%v) and could not be set aside: %w", path, err, qerr)
		}
		log.Warn("index corrupt, starting with an empty one", "path", path, "kept_as", moved)
		db, err = openDB(path)
	}
	if err != nil {
		return nil, fmt.Errorf("open index %s: %w", path, err)
	}
	log.Info("index ready", "path", path)
	return &Index{path: path, db: db, log: log}, nil
}

// Path returns where the database file lives.
func (i *Index) Path() string {
	return i.path
}

// Close closes the database.
func (i *Index) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.db.Close()
}

// PutNote replaces the note and its chunks in one short transaction. Read and chunk the content
// first (see ChunkAndHash) so no file or network I/O happens while the database is locked.
func (i *Index) PutNote(ctx context.Context, n Note, chunks []string) error {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("put note %s: %w", n.Path, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM notes WHERE path = ?`, n.Path); err != nil {
		return fmt.Errorf("put note %s: %w", n.Path, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO notes(path, title, revision, size, mtime_ns) VALUES (?, ?, ?, ?, ?)`,
		n.Path, n.Title, n.Revision, n.Size, n.MtimeNs); err != nil {
		return fmt.Errorf("put note %s: %w", n.Path, err)
	}
	insert, err := tx.PrepareContext(ctx, `INSERT INTO chunks(path, seq, content) VALUES (?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("put note %s: %w", n.Path, err)
	}
	defer insert.Close()

	for seq, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := insert.ExecContext(ctx, n.Path, seq, chunk); err != nil {
			return fmt.Errorf("put note %s chunk %d: %w", n.Path, seq, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("put note %s: %w", n.Path, err)
	}
	return nil
}

// GetNote returns the indexed metadata for path.
func (i *Index) GetNote(ctx context.Context, path string) (Note, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return Note{}, err
	}
	var n Note
	err := i.db.QueryRowContext(ctx,
		`SELECT path, title, revision, size, mtime_ns FROM notes WHERE path = ?`, path).
		Scan(&n.Path, &n.Title, &n.Revision, &n.Size, &n.MtimeNs)
	if errors.Is(err, sql.ErrNoRows) {
		return Note{}, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	if err != nil {
		return Note{}, fmt.Errorf("get note %s: %w", path, err)
	}
	return n, nil
}

// DeleteUnder removes the note or attachment at path and everything beneath it, for when a
// file or a whole folder disappears.
func (i *Index) DeleteUnder(ctx context.Context, path string) error {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	prefix := path + string(filepath.Separator)
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete under %s: %w", path, err)
	}
	defer tx.Rollback()
	for _, table := range []string{"notes", "attachments"} {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM `+table+` WHERE path = ? OR substr(path, 1, ?) = ?`,
			path, utf8.RuneCountInString(prefix), prefix); err != nil {
			return fmt.Errorf("delete under %s: %w", path, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete under %s: %w", path, err)
	}
	return nil
}

// NoteStates returns the indexed metadata of every note, keyed by path.
func (i *Index) NoteStates(ctx context.Context) (map[string]Note, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := i.db.QueryContext(ctx, `SELECT path, title, revision, size, mtime_ns FROM notes`)
	if err != nil {
		return nil, fmt.Errorf("list notes: %w", err)
	}
	defer rows.Close()
	states := map[string]Note{}
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.Path, &n.Title, &n.Revision, &n.Size, &n.MtimeNs); err != nil {
			return nil, fmt.Errorf("list notes: %w", err)
		}
		states[n.Path] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list notes: %w", err)
	}
	return states, nil
}

// DeleteNote removes the note and its chunks. Deleting an absent note is not an error.
func (i *Index) DeleteNote(ctx context.Context, path string) error {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := i.db.ExecContext(ctx, `DELETE FROM notes WHERE path = ?`, path); err != nil {
		return fmt.Errorf("delete note %s: %w", path, err)
	}
	return nil
}

// PutAttachment inserts or replaces the attachment.
func (i *Index) PutAttachment(ctx context.Context, a Attachment) error {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("put attachment %s: %w", a.Path, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM attachments WHERE path = ?`, a.Path); err != nil {
		return fmt.Errorf("put attachment %s: %w", a.Path, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO attachments(path, name, size, mtime_ns) VALUES (?, ?, ?, ?)`,
		a.Path, a.Name, a.Size, a.MtimeNs); err != nil {
		return fmt.Errorf("put attachment %s: %w", a.Path, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("put attachment %s: %w", a.Path, err)
	}
	return nil
}

// DeleteAttachment removes the attachment. Deleting an absent attachment is not an error.
func (i *Index) DeleteAttachment(ctx context.Context, path string) error {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := i.db.ExecContext(ctx, `DELETE FROM attachments WHERE path = ?`, path); err != nil {
		return fmt.Errorf("delete attachment %s: %w", path, err)
	}
	return nil
}

// AttachmentStates returns the indexed metadata of every attachment, keyed by path.
func (i *Index) AttachmentStates(ctx context.Context) (map[string]Attachment, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := i.db.QueryContext(ctx, `SELECT path, name, size, mtime_ns FROM attachments`)
	if err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}
	defer rows.Close()
	states := map[string]Attachment{}
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.Path, &a.Name, &a.Size, &a.MtimeNs); err != nil {
			return nil, fmt.Errorf("list attachments: %w", err)
		}
		states[a.Path] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}
	return states, nil
}

// SearchNotes returns notes whose title or content contains every word in query.
// Title hits come before content hits, and each note appears once.
func (i *Index) SearchNotes(ctx context.Context, query string, limit int) ([]Hit, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	rows, err := i.db.QueryContext(ctx, `
		SELECT notes.path, notes.title, notes.title AS excerpt, 0 AS grp, bm25(notes_fts) AS score
		FROM notes_fts JOIN notes ON notes.rowid = notes_fts.rowid
		WHERE notes_fts MATCH ?1
		UNION ALL
		SELECT c.path, n.title, snippet(chunks_fts, 0, '', '', '...', 16), 1, bm25(chunks_fts)
		FROM chunks_fts JOIN chunks c ON c.id = chunks_fts.rowid JOIN notes n ON n.path = c.path
		WHERE chunks_fts MATCH ?1
		ORDER BY 4, 5`, match)
	if err != nil {
		return nil, fmt.Errorf("search notes: %w", err)
	}
	defer rows.Close()

	limit = clampLimit(limit)
	seen := map[string]bool{}
	var hits []Hit
	for rows.Next() && len(hits) < limit {
		var h Hit
		var grp int
		var score float64
		if err := rows.Scan(&h.Path, &h.Title, &h.Excerpt, &grp, &score); err != nil {
			return nil, fmt.Errorf("search notes: %w", err)
		}
		if !seen[h.Path] {
			seen[h.Path] = true
			hits = append(hits, h)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search notes: %w", err)
	}
	return hits, nil
}

// SearchAttachments returns attachments whose filename contains every word in query.
func (i *Index) SearchAttachments(ctx context.Context, query string, limit int) ([]Attachment, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	rows, err := i.db.QueryContext(ctx, `
		SELECT a.path, a.name, a.size, a.mtime_ns
		FROM attachments_fts JOIN attachments a ON a.rowid = attachments_fts.rowid
		WHERE attachments_fts MATCH ?
		ORDER BY bm25(attachments_fts)
		LIMIT ?`, match, clampLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("search attachments: %w", err)
	}
	defer rows.Close()

	var found []Attachment
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.Path, &a.Name, &a.Size, &a.MtimeNs); err != nil {
			return nil, fmt.Errorf("search attachments: %w", err)
		}
		found = append(found, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search attachments: %w", err)
	}
	return found, nil
}

// ftsQuery turns user input into an FTS5 query that matches every word literally,
// so punctuation and FTS operators in the input cannot cause a syntax error.
func ftsQuery(query string) string {
	words := strings.Fields(query)
	for n, w := range words {
		words[n] = `"` + strings.ReplaceAll(w, `"`, `""`) + `"`
	}
	return strings.Join(words, " ")
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultSearchLimit
	}
	return min(limit, maxSearchLimit)
}
