package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	"modernc.org/sqlite"
)

// SQLite result codes that mean the file is damaged or is not a database at all.
const (
	sqliteCorrupt = 11
	sqliteNotADB  = 26
)

var errCorrupt = errors.New("database is corrupt")

// openDB opens the database at path in WAL mode with a busy timeout, creates the schema if
// needed, and runs a quick integrity check.
func openDB(path string) (*sql.DB, error) {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create index schema: %w", err)
	}
	if err := quickCheck(context.Background(), db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// quickCheck runs SQLite's integrity check and returns an error wrapping errCorrupt unless
// the database reports "ok".
func quickCheck(ctx context.Context, db *sql.DB) error {
	var result string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check(1)`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("%w: %s", errCorrupt, result)
	}
	return nil
}

// isCorrupt reports whether err means the database file is damaged, as opposed to being
// unreachable (permissions, a lock, a full disk), which must not cost anyone their index.
func isCorrupt(err error) bool {
	if errors.Is(err, errCorrupt) {
		return true
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		code := se.Code() & 0xff
		return code == sqliteCorrupt || code == sqliteNotADB
	}
	return false
}

// quarantine renames the database and its WAL and shared-memory files so they are out of the
// way but kept for inspection, and returns the new name of the database file.
func quarantine(path string) (string, error) {
	dest := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000")
	if err := os.Rename(path, dest); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	removeSidecars(path)
	return dest, nil
}

// removeSidecars deletes the WAL and shared-memory files of the database at path. They belong
// to one specific database file and corrupt any other one they get paired with.
func removeSidecars(path string) {
	os.Remove(path + "-wal")
	os.Remove(path + "-shm")
}

// Check runs SQLite's integrity check on the database. It returns nil if the database is sound.
func (i *Index) Check(ctx context.Context) error {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := quickCheck(ctx, i.db); err != nil {
		return fmt.Errorf("check index: %w", err)
	}
	return nil
}

// ReplaceWith makes the finished database at newPath the live one. newPath must belong to an
// Index that has been closed, so it is complete and has no WAL file of its own. Callers using
// this Index from several goroutines are paused while the swap happens, and carry on against
// the new database. If the swap fails the old database stays in use.
//
// Other processes that have the old file open keep reading the old copy until they reopen it.
func (i *Index) ReplaceWith(ctx context.Context, newPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()

	if err := i.db.Close(); err != nil {
		return fmt.Errorf("close index before replacing it: %w", err)
	}
	removeSidecars(i.path)
	if err := os.Rename(newPath, i.path); err != nil {
		// Put the old database back in service.
		db, reopenErr := openDB(i.path)
		if reopenErr != nil {
			return errors.Join(fmt.Errorf("replace index: %w", err), fmt.Errorf("reopen old index: %w", reopenErr))
		}
		i.db = db
		return fmt.Errorf("replace index: %w", err)
	}
	removeSidecars(newPath)
	db, err := openDB(i.path)
	if err != nil {
		return fmt.Errorf("open replaced index: %w", err)
	}
	i.db = db
	i.log.Info("index replaced", "path", i.path)
	return nil
}
