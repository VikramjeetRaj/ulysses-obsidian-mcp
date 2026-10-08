package vault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrExists is returned by CreateNote when the note already exists.
	ErrExists = errors.New("note already exists")
	// ErrConflict is returned by UpdateNote when the note changed since expectedRevision.
	ErrConflict = errors.New("note has changed")
	// ErrNotFound is returned when a note does not exist.
	ErrNotFound = errors.New("note not found")
	// ErrTooLarge is returned when a file is bigger than the vault will return in one read.
	ErrTooLarge = errors.New("file too large")
	// ErrInvalidPath is returned when a path is not inside the Knowledge directory.
	ErrInvalidPath = errors.New("invalid path")
)

const (
	defaultListLimit = 50
	maxListLimit     = 200

	// maxAttachmentBytes caps what ReadAttachment returns, since the bytes travel in one message.
	maxAttachmentBytes = 10 << 20
	// trashDir is where TrashNote moves notes, inside Knowledge/.
	trashDir = ".trash"
)

// Vault gives access to the Markdown notes under <vault>/Knowledge/.
type Vault struct {
	path string
	log  *slog.Logger
}

// New validates the vault folder, creates its Knowledge directory if needed, and returns a Vault.
func New(path string, log *slog.Logger) (*Vault, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("vault must be a directory: %s", path)
	}
	if err := os.MkdirAll(filepath.Join(path, "Knowledge"), 0755); err != nil {
		return nil, fmt.Errorf("create Knowledge directory: %w", err)
	}
	log.Info("vault ready", "path", path)
	return &Vault{path: path, log: log}, nil
}

// ReadNote returns the note's content and its content hash.
func (v *Vault) ReadNote(ctx context.Context, path string) (Note, error) {
	if err := ctx.Err(); err != nil {
		return Note{}, err
	}
	if err := v.validatePath(path); err != nil {
		return Note{}, err
	}

	isFile, err := isRegularFile(path)
	if err != nil {
		return Note{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if !isFile {
		return Note{}, fmt.Errorf("%w: %s", ErrNotFound, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Note{}, fmt.Errorf("read %s: %w", path, err)
	}

	sum256 := sha256.Sum256(data)
	return Note{
		Path:    v.relativePath(path),
		Content: string(data),
		HashVal: hex.EncodeToString(sum256[:]),
	}, nil
}

// CreateNote writes a new note, creating missing folders, and returns its revision. It fails
// with ErrExists rather than overwrite an existing note.
func (v *Vault) CreateNote(ctx context.Context, path, content string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := v.validatePath(path); err != nil {
		return "", err
	}
	contentBytes := []byte(content)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("create folder for %s: %w", path, err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("%w: %s", ErrExists, path)
		}
		return "", fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := file.Write(contentBytes); err != nil {
		file.Close()
		os.Remove(path)
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("close %s: %w", path, err)
	}
	v.log.Info("note created", "path", path)
	return hashBytes(contentBytes), nil
}

// UpdateNote replaces the note's content if its current hash equals expectedRevision,
// and returns the new revision. The new content goes to a temporary file beside the
// note and is renamed into place, so readers never see a partial write.
func (v *Vault) UpdateNote(ctx context.Context, path, content, expectedRevision string) (string, error) {
	note, err := v.ReadNote(ctx, path)
	if err != nil {
		return "", err
	}
	if note.HashVal != expectedRevision {
		return "", fmt.Errorf("%w: %s", ErrConflict, path)
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	contentBytes := []byte(content)
	if _, err := tmp.Write(contentBytes); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("write temp file for %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("close temp file for %s: %w", path, err)
	}
	if err := os.Chmod(tmpPath, info.Mode().Perm()); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("chmod temp file for %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("replace %s: %w", path, err)
	}

	v.log.Info("note updated", "path", path)
	return hashBytes(contentBytes), nil
}

// ListNotes lists the Markdown notes directly inside folder, sorted by name.
// cursor is a 1-based page number (empty means the first page). Entries carry the
// note's path relative to Knowledge/ and its hash; Content is left empty.
func (v *Vault) ListNotes(ctx context.Context, folder string, limit int, cursor string) (NotePage, error) {
	names, page, err := v.listPage(ctx, folder, limit, cursor, isMarkdown)
	if err != nil {
		return NotePage{}, err
	}
	result := NotePage{Notes: []Note{}, Page: page}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return NotePage{}, err
		}
		full := filepath.Join(folder, name)
		hash, err := hashFile(full)
		if err != nil {
			return NotePage{}, fmt.Errorf("hash %s: %w", full, err)
		}
		result.Notes = append(result.Notes, Note{Path: v.relativePath(full), HashVal: hash})
	}
	return result, nil
}

// ListAttachments lists the non-Markdown files directly inside folder, sorted by name, with the
// same paging as ListNotes. Dot-files are skipped.
func (v *Vault) ListAttachments(ctx context.Context, folder string, limit int, cursor string) (AttachmentPage, error) {
	names, page, err := v.listPage(ctx, folder, limit, cursor, func(name string) bool {
		return !isMarkdown(name) && !strings.HasPrefix(name, ".")
	})
	if err != nil {
		return AttachmentPage{}, err
	}
	result := AttachmentPage{Attachments: []Attachment{}, Page: page}
	for _, name := range names {
		full := filepath.Join(folder, name)
		info, err := os.Stat(full)
		if err != nil {
			return AttachmentPage{}, fmt.Errorf("stat %s: %w", full, err)
		}
		result.Attachments = append(result.Attachments, Attachment{Path: v.relativePath(full), Size: info.Size()})
	}
	return result, nil
}

// ReadAttachment returns the bytes of a non-Markdown file, up to 10 MiB.
func (v *Vault) ReadAttachment(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := v.validatePath(path); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	if info.Size() > maxAttachmentBytes {
		return nil, fmt.Errorf("%w: %s is %d bytes, limit is %d", ErrTooLarge, path, info.Size(), maxAttachmentBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxAttachmentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > maxAttachmentBytes {
		return nil, fmt.Errorf("%w: %s grew past %d bytes while reading", ErrTooLarge, path, maxAttachmentBytes)
	}
	return data, nil
}

// TrashNote moves the note into Knowledge/.trash/ if its current hash equals expectedRevision,
// and returns its new path relative to Knowledge/. Nothing is deleted, so the note can be
// restored by moving it back. A note already in the trash is rejected.
func (v *Vault) TrashNote(ctx context.Context, path, expectedRevision string) (string, error) {
	note, err := v.ReadNote(ctx, path)
	if err != nil {
		return "", err
	}
	if note.HashVal != expectedRevision {
		return "", fmt.Errorf("%w: %s", ErrConflict, path)
	}
	rel := note.Path
	if rel == trashDir || strings.HasPrefix(rel, trashDir+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s is already in the trash", ErrInvalidPath, rel)
	}
	knowledge, err := filepath.EvalSymlinks(filepath.Join(v.path, "Knowledge"))
	if err != nil {
		return "", fmt.Errorf("resolve Knowledge directory: %w", err)
	}

	dest := filepath.Join(knowledge, trashDir, rel)
	if _, err := os.Lstat(dest); err == nil {
		ext := filepath.Ext(dest)
		dest = strings.TrimSuffix(dest, ext) + "-" + time.Now().UTC().Format("20060102T150405.000") + ext
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return "", fmt.Errorf("create trash folder: %w", err)
	}
	if err := os.Rename(path, dest); err != nil {
		return "", fmt.Errorf("move %s to trash: %w", rel, err)
	}
	trashed, err := filepath.Rel(knowledge, dest)
	if err != nil {
		trashed = dest
	}
	v.log.Info("note trashed", "path", rel, "trash", trashed)
	return trashed, nil
}

// listPage validates folder and returns the sorted names of the regular files in it that keep
// accepts, for the requested page, along with the page description. Symlinks are skipped so a
// link cannot expose files outside Knowledge/.
func (v *Vault) listPage(ctx context.Context, folder string, limit int, cursor string, keep func(name string) bool) ([]string, Page, error) {
	if err := ctx.Err(); err != nil {
		return nil, Page{}, err
	}
	if limit <= 0 {
		limit = defaultListLimit
	}
	limit = min(limit, maxListLimit)
	page := 1
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 1 {
			return nil, Page{}, fmt.Errorf("invalid cursor %q: must be a positive page number", cursor)
		}
		page = n
	}

	if err := v.validateFolder(folder); err != nil {
		return nil, Page{}, err
	}
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil, Page{}, fmt.Errorf("list %s: %w", folder, err)
	}
	var names []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && keep(entry.Name()) {
			names = append(names, entry.Name())
		}
	}

	result := Page{Total: len(names), Current: page, Size: limit}
	start := (page - 1) * limit
	if start >= len(names) {
		return nil, result, nil
	}
	return names[start:min(start+limit, len(names))], result, nil
}

func isMarkdown(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".md")
}

// validateFolder is like validatePath, but also accepts the Knowledge directory itself.
func (v *Vault) validateFolder(folder string) error {
	knowledge, err := filepath.EvalSymlinks(filepath.Join(v.path, "Knowledge"))
	if err != nil {
		return fmt.Errorf("resolve Knowledge directory: %w", err)
	}
	if resolved, err := resolvePath(folder); err == nil && resolved == knowledge {
		return nil
	}
	return v.validatePath(folder)
}

// relativePath returns path relative to the Knowledge directory, or path unchanged if that fails.
func (v *Vault) relativePath(path string) string {
	knowledge, err := filepath.EvalSymlinks(filepath.Join(v.path, "Knowledge"))
	if err != nil {
		return path
	}
	resolved, err := resolvePath(path)
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(knowledge, resolved)
	if err != nil {
		return path
	}
	return rel
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// validatePath checks that path is a descendant of this vault's Knowledge directory.
func (v *Vault) validatePath(path string) error {
	knowledge, err := filepath.EvalSymlinks(filepath.Join(v.path, "Knowledge"))
	if err != nil {
		return fmt.Errorf("resolve Knowledge directory: %w", err)
	}
	resolved, err := resolvePath(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	rel, err := filepath.Rel(knowledge, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("%w: path must be inside %s", ErrInvalidPath, knowledge)
	}
	return nil
}

// resolvePath follows symlinks in the existing portion of a path, including for new files.
func resolvePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := abs
	var missing []string
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for _, m := range slices.Backward(missing) {
				resolved = filepath.Join(resolved, m)
			}
			return resolved, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func isRegularFile(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Path explicitly does not exist
			return false, nil
		}
		// Return any other errors (e.g., permission denied)
		return false, err
	}

	// Check if it is a regular file (not a directory, symlink, named pipe, etc.)
	return info.Mode().IsRegular(), nil
}
