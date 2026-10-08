package vault

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var bg = context.Background()

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func mustNew(t *testing.T, path string) *Vault {
	t.Helper()
	v, err := New(path, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNewAcceptsDirectory(t *testing.T) {
	path := t.TempDir()
	mustNew(t, path)
	if info, err := os.Stat(filepath.Join(path, "Knowledge")); err != nil || !info.IsDir() {
		t.Fatalf("expected Knowledge directory, got info=%v err=%v", info, err)
	}
}

func TestNewAcceptsExistingKnowledgeDirectory(t *testing.T) {
	path := t.TempDir()
	if err := os.Mkdir(filepath.Join(path, "Knowledge"), 0755); err != nil {
		t.Fatal(err)
	}
	mustNew(t, path)
}

func TestNewFailsWhenKnowledgeIsFile(t *testing.T) {
	path := t.TempDir()
	if err := os.WriteFile(filepath.Join(path, "Knowledge"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path, testLogger()); err == nil {
		t.Fatal("expected an error")
	}
}

func TestNewFailsForFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.md")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path, testLogger()); err == nil {
		t.Fatal("expected an error")
	}
}

func TestNewFailsForMissingPath(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "missing"), testLogger()); err == nil {
		t.Fatal("expected an error")
	}
}

func TestValidatePath(t *testing.T) {
	vaultPath := t.TempDir()
	svc := mustNew(t, vaultPath)
	knowledge := filepath.Join(vaultPath, "Knowledge")
	other := filepath.Join(vaultPath, "Knowledge-other", "note.md")
	for _, tc := range []struct {
		name      string
		path      string
		wantError bool
	}{
		{"nested file", filepath.Join(knowledge, "new", "note.md"), false},
		{"Knowledge itself", knowledge, true},
		{"vault root", vaultPath, true},
		{"sibling prefix", other, true},
		{"traversal", filepath.Join(knowledge, "..", "outside.md"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.validatePath(tc.path)
			if (err != nil) != tc.wantError {
				t.Fatalf("validatePath(%q) error = %v, wantError = %v", tc.path, err, tc.wantError)
			}
		})
	}
}

func TestValidatePathRejectsSymlinkEscape(t *testing.T) {
	vaultPath := t.TempDir()
	svc := mustNew(t, vaultPath)
	outside := t.TempDir()
	link := filepath.Join(vaultPath, "Knowledge", "outside")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := svc.validatePath(filepath.Join(link, "new", "note.md")); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}

func newTestVault(t *testing.T) (*Vault, string) {
	t.Helper()
	vaultPath := t.TempDir()
	svc := mustNew(t, vaultPath)
	return svc, filepath.Join(vaultPath, "Knowledge")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func sha(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func TestReadNoteReturnsContentHashAndRelativePath(t *testing.T) {
	svc, knowledge := newTestVault(t)
	path := filepath.Join(knowledge, "sub", "idea.md")
	writeFile(t, path, "hello")

	note, err := svc.ReadNote(bg, path)
	if err != nil {
		t.Fatal(err)
	}
	if note.Content != "hello" || note.HashVal != sha("hello") || note.Path != filepath.Join("sub", "idea.md") {
		t.Fatalf("unexpected note: %+v", note)
	}
}

func TestReadNoteErrors(t *testing.T) {
	svc, knowledge := newTestVault(t)
	writeFile(t, filepath.Join(knowledge, "dir", "x.md"), "x")
	for _, tc := range []struct{ name, path string }{
		{"missing note", filepath.Join(knowledge, "missing.md")},
		{"directory", filepath.Join(knowledge, "dir")},
		{"outside Knowledge", filepath.Join(filepath.Dir(knowledge), "outside.md")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.ReadNote(bg, tc.path); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestCreateNoteWritesFileAndReturnsRevision(t *testing.T) {
	svc, knowledge := newTestVault(t)
	path := filepath.Join(knowledge, "new.md")

	rev, err := svc.CreateNote(bg, path, "body")
	if err != nil {
		t.Fatal(err)
	}
	if rev != sha("body") {
		t.Fatalf("revision = %s, want %s", rev, sha("body"))
	}
	if got, _ := os.ReadFile(path); string(got) != "body" {
		t.Fatalf("file content = %q", got)
	}
}

func TestCreateNoteRejectsPathOutsideKnowledge(t *testing.T) {
	svc, knowledge := newTestVault(t)
	outside := filepath.Join(filepath.Dir(knowledge), "outside.md")
	if _, err := svc.CreateNote(bg, outside, "x"); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("file outside Knowledge must not be created")
	}
}

func TestCreateNoteDoesNotOverwriteExistingNote(t *testing.T) {
	svc, knowledge := newTestVault(t)
	path := filepath.Join(knowledge, "note.md")
	writeFile(t, path, "original")

	_, err := svc.CreateNote(bg, path, "replacement")
	if !errors.Is(err, ErrExists) {
		t.Fatalf("error = %v, want ErrExists", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "original" {
		t.Fatalf("create overwrote the note: %q", got)
	}
}

func TestCreateNoteTwiceFailsSecondTime(t *testing.T) {
	svc, knowledge := newTestVault(t)
	path := filepath.Join(knowledge, "note.md")
	if _, err := svc.CreateNote(bg, path, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateNote(bg, path, "second"); !errors.Is(err, ErrExists) {
		t.Fatalf("error = %v, want ErrExists", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "first" {
		t.Fatalf("content = %q, want first", got)
	}
}

func TestUpdateNoteReplacesContentWhenRevisionMatches(t *testing.T) {
	svc, knowledge := newTestVault(t)
	path := filepath.Join(knowledge, "note.md")
	writeFile(t, path, "old")

	rev, err := svc.UpdateNote(bg, path, "new", sha("old"))
	if err != nil {
		t.Fatal(err)
	}
	if rev != sha("new") {
		t.Fatalf("revision = %s, want %s", rev, sha("new"))
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Fatalf("content = %q, want new", got)
	}
}

func TestUpdateNoteRevisionCanBeChained(t *testing.T) {
	svc, knowledge := newTestVault(t)
	path := filepath.Join(knowledge, "note.md")
	rev, err := svc.CreateNote(bg, path, "v1")
	if err != nil {
		t.Fatal(err)
	}
	rev, err = svc.UpdateNote(bg, path, "v2", rev)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateNote(bg, path, "v3", rev); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "v3" {
		t.Fatalf("content = %q, want v3", got)
	}
}

func TestUpdateNoteLeavesNoTempFilesAndKeepsPermissions(t *testing.T) {
	svc, knowledge := newTestVault(t)
	path := filepath.Join(knowledge, "sub", "note.md")
	writeFile(t, path, "old")
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.UpdateNote(bg, path, "new", sha("old")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "note.md" {
		t.Fatalf("unexpected directory contents: %v", entries)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0640 {
		t.Fatalf("mode = %v, want 0640", info.Mode().Perm())
	}
}

func TestUpdateNoteRejectsPathOutsideKnowledge(t *testing.T) {
	svc, knowledge := newTestVault(t)
	outside := filepath.Join(filepath.Dir(knowledge), "outside.md")
	writeFile(t, outside, "old")
	if _, err := svc.UpdateNote(bg, outside, "new", sha("old")); err == nil {
		t.Fatal("expected an error")
	}
	if got, _ := os.ReadFile(outside); string(got) != "old" {
		t.Fatalf("file outside Knowledge changed: %q", got)
	}
}

func TestUpdateNoteRejectsStaleRevision(t *testing.T) {
	svc, knowledge := newTestVault(t)
	path := filepath.Join(knowledge, "note.md")
	writeFile(t, path, "original")

	if _, err := svc.UpdateNote(bg, path, "replacement", sha("something else")); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "original" {
		t.Fatalf("stale update changed the note: %q", got)
	}
}

func TestUpdateNoteRejectsMissingNote(t *testing.T) {
	svc, knowledge := newTestVault(t)
	if _, err := svc.UpdateNote(bg, filepath.Join(knowledge, "missing.md"), "x", sha("")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestListNotesReturnsPathsAndHashesWithoutContent(t *testing.T) {
	svc, knowledge := newTestVault(t)
	writeFile(t, filepath.Join(knowledge, "b.md"), "bee")
	writeFile(t, filepath.Join(knowledge, "a.md"), "ay")

	page, err := svc.ListNotes(bg, knowledge, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Notes) != 2 {
		t.Fatalf("got %d notes, want 2", len(page.Notes))
	}
	want := []struct{ path, hash string }{{"a.md", sha("ay")}, {"b.md", sha("bee")}}
	for i, w := range want {
		n := page.Notes[i]
		if n.Path != w.path || n.HashVal != w.hash || n.Content != "" {
			t.Errorf("note %d = %+v, want path %s hash %s and empty content", i, n, w.path, w.hash)
		}
	}
	if page.Page.Total != 2 || page.Page.Current != 1 || page.Page.Size != 10 {
		t.Errorf("unexpected page: %+v", page.Page)
	}
}

func TestListNotesHashMatchesReadNote(t *testing.T) {
	svc, knowledge := newTestVault(t)
	path := filepath.Join(knowledge, "n.md")
	writeFile(t, path, "same bytes")

	page, err := svc.ListNotes(bg, knowledge, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	note, err := svc.ReadNote(bg, path)
	if err != nil {
		t.Fatal(err)
	}
	if page.Notes[0].HashVal != note.HashVal {
		t.Fatalf("list hash %s != read hash %s", page.Notes[0].HashVal, note.HashVal)
	}
}

func TestListNotesIgnoresNonMarkdownDirectoriesAndSubfolderNotes(t *testing.T) {
	svc, knowledge := newTestVault(t)
	writeFile(t, filepath.Join(knowledge, "keep.md"), "k")
	writeFile(t, filepath.Join(knowledge, "UPPER.MD"), "u")
	writeFile(t, filepath.Join(knowledge, "image.png"), "p")
	writeFile(t, filepath.Join(knowledge, "notes.txt"), "t")
	writeFile(t, filepath.Join(knowledge, "folder.md", "inner.md"), "dir named like a note")
	writeFile(t, filepath.Join(knowledge, "sub", "nested.md"), "n")

	page, err := svc.ListNotes(bg, knowledge, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range page.Notes {
		got = append(got, n.Path)
	}
	if fmt.Sprint(got) != "[UPPER.MD keep.md]" {
		t.Fatalf("got %v, want [UPPER.MD keep.md]", got)
	}
}

func TestListNotesListsSubfolder(t *testing.T) {
	svc, knowledge := newTestVault(t)
	writeFile(t, filepath.Join(knowledge, "root.md"), "r")
	writeFile(t, filepath.Join(knowledge, "sub", "nested.md"), "n")

	page, err := svc.ListNotes(bg, filepath.Join(knowledge, "sub"), 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Notes) != 1 || page.Notes[0].Path != filepath.Join("sub", "nested.md") {
		t.Fatalf("unexpected notes: %+v", page.Notes)
	}
}

func TestListNotesPaginates(t *testing.T) {
	svc, knowledge := newTestVault(t)
	for i := 1; i <= 5; i++ {
		writeFile(t, filepath.Join(knowledge, fmt.Sprintf("n%d.md", i)), "x")
	}
	for _, tc := range []struct {
		cursor string
		want   string
	}{
		{"", "[n1.md n2.md]"},
		{"1", "[n1.md n2.md]"},
		{"2", "[n3.md n4.md]"},
		{"3", "[n5.md]"},
		{"4", "[]"},
	} {
		t.Run("cursor "+tc.cursor, func(t *testing.T) {
			page, err := svc.ListNotes(bg, knowledge, 2, tc.cursor)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, n := range page.Notes {
				got = append(got, n.Path)
			}
			if fmt.Sprint(got) != tc.want {
				t.Errorf("got %v, want %s", got, tc.want)
			}
			if page.Page.Total != 5 || page.Page.Size != 2 {
				t.Errorf("unexpected page: %+v", page.Page)
			}
		})
	}
}

func TestListNotesEmptyFolderReturnsEmptySlice(t *testing.T) {
	svc, knowledge := newTestVault(t)
	page, err := svc.ListNotes(bg, knowledge, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.Notes == nil || len(page.Notes) != 0 || page.Page.Total != 0 {
		t.Fatalf("unexpected page: %+v", page)
	}
}

func TestListNotesAppliesDefaultAndMaximumLimit(t *testing.T) {
	svc, knowledge := newTestVault(t)
	writeFile(t, filepath.Join(knowledge, "a.md"), "a")
	for _, tc := range []struct{ limit, want int }{
		{0, defaultListLimit},
		{-5, defaultListLimit},
		{maxListLimit + 1000, maxListLimit},
	} {
		page, err := svc.ListNotes(bg, knowledge, tc.limit, "")
		if err != nil {
			t.Fatal(err)
		}
		if page.Page.Size != tc.want {
			t.Errorf("limit %d: size = %d, want %d", tc.limit, page.Page.Size, tc.want)
		}
	}
}

func TestListNotesRejectsInvalidCursor(t *testing.T) {
	svc, knowledge := newTestVault(t)
	for _, cursor := range []string{"abc", "0", "-1", "1.5"} {
		if _, err := svc.ListNotes(bg, knowledge, 10, cursor); err == nil {
			t.Errorf("cursor %q: expected an error", cursor)
		}
	}
}

func TestListNotesRejectsInvalidFolders(t *testing.T) {
	svc, knowledge := newTestVault(t)
	writeFile(t, filepath.Join(knowledge, "file.md"), "f")
	vaultPath := filepath.Dir(knowledge)
	writeFile(t, filepath.Join(vaultPath, "Knowledge-other", "x.md"), "x")
	for _, tc := range []struct{ name, folder string }{
		{"vault root", vaultPath},
		{"sibling prefix", filepath.Join(vaultPath, "Knowledge-other")},
		{"traversal", filepath.Join(knowledge, "..")},
		{"missing folder", filepath.Join(knowledge, "missing")},
		{"a file", filepath.Join(knowledge, "file.md")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.ListNotes(bg, tc.folder, 10, ""); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestListNotesRejectsSymlinkedFolderEscape(t *testing.T) {
	svc, knowledge := newTestVault(t)
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.md"), "secret")
	link := filepath.Join(knowledge, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListNotes(bg, link, 10, ""); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}

func TestListNotesSkipsSymlinkedFiles(t *testing.T) {
	svc, knowledge := newTestVault(t)
	outside := filepath.Join(t.TempDir(), "secret.md")
	writeFile(t, outside, "secret")
	writeFile(t, filepath.Join(knowledge, "real.md"), "r")
	if err := os.Symlink(outside, filepath.Join(knowledge, "link.md")); err != nil {
		t.Fatal(err)
	}
	page, err := svc.ListNotes(bg, knowledge, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Notes) != 1 || page.Notes[0].Path != "real.md" {
		t.Fatalf("unexpected notes: %+v", page.Notes)
	}
}

func TestReadNoteReturnsErrNotFoundForMissingNote(t *testing.T) {
	svc, knowledge := newTestVault(t)
	if _, err := svc.ReadNote(bg, filepath.Join(knowledge, "missing.md")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestPathsOutsideKnowledgeReturnErrInvalidPath(t *testing.T) {
	svc, knowledge := newTestVault(t)
	outside := filepath.Join(filepath.Dir(knowledge), "outside.md")
	if _, err := svc.ReadNote(bg, outside); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("ReadNote error = %v, want ErrInvalidPath", err)
	}
	if _, err := svc.CreateNote(bg, outside, "x"); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("CreateNote error = %v, want ErrInvalidPath", err)
	}
	if _, err := svc.UpdateNote(bg, outside, "x", sha("")); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("UpdateNote error = %v, want ErrInvalidPath", err)
	}
	if _, err := svc.ListNotes(bg, filepath.Dir(knowledge), 10, ""); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("ListNotes error = %v, want ErrInvalidPath", err)
	}
}

func TestCancelledContextStopsEveryOperation(t *testing.T) {
	svc, knowledge := newTestVault(t)
	path := filepath.Join(knowledge, "n.md")
	writeFile(t, path, "old")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := svc.ReadNote(ctx, path); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadNote error = %v", err)
	}
	if _, err := svc.CreateNote(ctx, filepath.Join(knowledge, "new.md"), "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("CreateNote error = %v", err)
	}
	if _, err := svc.UpdateNote(ctx, path, "new", sha("old")); !errors.Is(err, context.Canceled) {
		t.Errorf("UpdateNote error = %v", err)
	}
	if _, err := svc.ListNotes(ctx, knowledge, 10, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("ListNotes error = %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Errorf("cancelled update changed the note: %q", got)
	}
	if _, err := os.Stat(filepath.Join(knowledge, "new.md")); err == nil {
		t.Error("cancelled create wrote a file")
	}
}

func newLoggedVault(t *testing.T) (*Vault, *bytes.Buffer, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	vaultPath := t.TempDir()
	v, err := New(vaultPath, slog.New(slog.NewTextHandler(buf, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return v, buf, filepath.Join(vaultPath, "Knowledge")
}

func TestNewLogsStartup(t *testing.T) {
	_, buf, _ := newLoggedVault(t)
	if !strings.Contains(buf.String(), `level=INFO msg="vault ready"`) {
		t.Fatalf("missing startup log: %s", buf)
	}
}

func TestWritesLogSuccessWithPath(t *testing.T) {
	svc, buf, knowledge := newLoggedVault(t)
	path := filepath.Join(knowledge, "n.md")
	rev, err := svc.CreateNote(bg, path, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateNote(bg, path, "v2", rev); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`msg="note created" path=` + path, `msg="note updated" path=` + path} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("log missing %q: %s", want, buf)
		}
	}
}

func TestFailedOperationsLogNothingBeyondStartup(t *testing.T) {
	svc, buf, knowledge := newLoggedVault(t)
	path := filepath.Join(knowledge, "n.md")
	writeFile(t, path, "v1")
	before := buf.Len()

	svc.CreateNote(bg, path, "again")
	svc.UpdateNote(bg, path, "v2", sha("stale"))
	svc.ReadNote(bg, filepath.Join(knowledge, "missing.md"))
	svc.ListNotes(bg, knowledge, 10, "abc")
	if buf.Len() != before {
		t.Fatalf("errors must be returned, not logged: %s", buf.String()[before:])
	}
}

func TestLogsDoNotContainNoteContent(t *testing.T) {
	svc, buf, knowledge := newLoggedVault(t)
	path := filepath.Join(knowledge, "n.md")
	if _, err := svc.CreateNote(bg, path, "TOP-SECRET-BODY"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateNote(bg, path, "ANOTHER-SECRET", sha("TOP-SECRET-BODY")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReadNote(bg, path); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "SECRET") {
		t.Fatalf("log leaks note content: %s", buf)
	}
}
