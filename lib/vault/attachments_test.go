package vault

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListAttachmentsListsNonMarkdownFiles(t *testing.T) {
	svc, knowledge := newTestVault(t)
	writeFile(t, filepath.Join(knowledge, "b.png"), "12345")
	writeFile(t, filepath.Join(knowledge, "a.pdf"), "123")
	writeFile(t, filepath.Join(knowledge, "note.md"), "x")
	writeFile(t, filepath.Join(knowledge, ".DS_Store"), "junk")
	writeFile(t, filepath.Join(knowledge, "sub", "nested.png"), "n")

	page, err := svc.ListAttachments(bg, knowledge, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []Attachment{{Path: "a.pdf", Size: 3}, {Path: "b.png", Size: 5}}
	if len(page.Attachments) != len(want) || page.Attachments[0] != want[0] || page.Attachments[1] != want[1] {
		t.Fatalf("attachments = %+v, want %+v", page.Attachments, want)
	}
	if page.Page.Total != 2 || page.Page.Current != 1 {
		t.Errorf("page = %+v", page.Page)
	}
}

func TestListAttachmentsRejectsFolderOutsideKnowledge(t *testing.T) {
	svc, knowledge := newTestVault(t)
	if _, err := svc.ListAttachments(bg, filepath.Dir(knowledge), 10, ""); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("error = %v, want ErrInvalidPath", err)
	}
}

func TestReadAttachment(t *testing.T) {
	svc, knowledge := newTestVault(t)
	path := filepath.Join(knowledge, "img", "pic.bin")
	writeFile(t, path, "\x00\x01binary")

	data, err := svc.ReadAttachment(bg, path)
	if err != nil || !bytes.Equal(data, []byte("\x00\x01binary")) {
		t.Fatalf("data = %q, err = %v", data, err)
	}
	if _, err := svc.ReadAttachment(bg, filepath.Join(knowledge, "missing.png")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: error = %v, want ErrNotFound", err)
	}
	if _, err := svc.ReadAttachment(bg, filepath.Join(filepath.Dir(knowledge), "x.png")); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("outside: error = %v, want ErrInvalidPath", err)
	}
}

func TestReadAttachmentRejectsLargeFile(t *testing.T) {
	svc, knowledge := newTestVault(t)
	path := filepath.Join(knowledge, "big.bin")
	writeFile(t, path, strings.Repeat("x", maxAttachmentBytes+1))
	if _, err := svc.ReadAttachment(bg, path); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("error = %v, want ErrTooLarge", err)
	}
}

func TestTrashNoteMovesNoteAndKeepsFolders(t *testing.T) {
	svc, knowledge := newTestVault(t)
	path := filepath.Join(knowledge, "sub", "idea.md")
	writeFile(t, path, "keep me")

	trashed, err := svc.TrashNote(bg, path, sha("keep me"))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(".trash", "sub", "idea.md"); trashed != want {
		t.Fatalf("trashed = %q, want %q", trashed, want)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("original still there: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(knowledge, trashed))
	if err != nil || string(got) != "keep me" {
		t.Fatalf("trashed content = %q, err = %v", got, err)
	}
}

func TestTrashNoteNeverOverwritesEarlierTrash(t *testing.T) {
	svc, knowledge := newTestVault(t)
	path := filepath.Join(knowledge, "idea.md")
	var trashed []string
	for _, body := range []string{"first", "second"} {
		writeFile(t, path, body)
		p, err := svc.TrashNote(bg, path, sha(body))
		if err != nil {
			t.Fatal(err)
		}
		trashed = append(trashed, p)
	}
	if trashed[0] == trashed[1] {
		t.Fatalf("both notes trashed to %q", trashed[0])
	}
	for i, body := range []string{"first", "second"} {
		got, err := os.ReadFile(filepath.Join(knowledge, trashed[i]))
		if err != nil || string(got) != body {
			t.Fatalf("trash %d = %q, err = %v, want %q", i, got, err, body)
		}
	}
}

func TestTrashNoteErrors(t *testing.T) {
	svc, knowledge := newTestVault(t)
	path := filepath.Join(knowledge, "idea.md")
	writeFile(t, path, "text")

	if _, err := svc.TrashNote(bg, path, "stale"); !errors.Is(err, ErrConflict) {
		t.Errorf("stale revision: error = %v, want ErrConflict", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("note must stay after a conflict: %v", err)
	}
	if _, err := svc.TrashNote(bg, filepath.Join(knowledge, "missing.md"), "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: error = %v, want ErrNotFound", err)
	}
	if _, err := svc.TrashNote(bg, filepath.Join(filepath.Dir(knowledge), "x.md"), "x"); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("outside: error = %v, want ErrInvalidPath", err)
	}
	trashed, err := svc.TrashNote(bg, path, sha("text"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TrashNote(bg, filepath.Join(knowledge, trashed), sha("text")); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("already trashed: error = %v, want ErrInvalidPath", err)
	}
}
