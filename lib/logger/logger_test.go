package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewWritesAllLevels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "app.log")
	l, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	l.Info("started", "port", 1)
	l.Warn("careful")
	l.Error("failed", "err", os.ErrNotExist)

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"level=INFO msg=started port=1", "level=WARN msg=careful", "level=ERROR msg=failed"} {
		if !strings.Contains(string(contents), want) {
			t.Errorf("log missing %q: %s", want, contents)
		}
	}
}

func TestNewAppendsToExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	for _, msg := range []string{"first", "second"} {
		l, err := New(path)
		if err != nil {
			t.Fatal(err)
		}
		l.Info(msg)
	}
	contents, _ := os.ReadFile(path)
	if !strings.Contains(string(contents), "first") || !strings.Contains(string(contents), "second") {
		t.Fatalf("expected both records, got: %s", contents)
	}
}

func TestNewFailsWhenLogPathIsDirectory(t *testing.T) {
	if _, err := New(t.TempDir()); err == nil {
		t.Fatal("expected an error")
	}
}
