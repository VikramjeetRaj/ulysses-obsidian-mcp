package indexer

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watch watches every folder under Knowledge/ and updates the index as files change, until ctx
// is cancelled. Bursts of events are collected for the debounce period, and each affected path is
// checked on disk before the index is touched, so the event type never decides the outcome.
// New folders are added to the watch. If the operating system reports lost events, Watch runs a
// full Sync. Watch does not index existing files; run Sync for that.
func (x *Indexer) Watch(ctx context.Context) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create watcher: %w", err)
	}
	defer w.Close()
	if err := x.watchTree(ctx, w, x.knowledge); err != nil {
		return fmt.Errorf("watch %s: %w", x.knowledge, err)
	}
	x.log.Info("watching for changes", "path", x.knowledge)
	if x.onWatching != nil {
		x.onWatching()
	}

	pending := map[string]struct{}{}
	resync := false
	timer := time.NewTimer(x.debounce)
	timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if ev.Op == fsnotify.Chmod || x.hidden(ev.Name) {
				continue
			}
			pending[ev.Name] = struct{}{}
			timer.Reset(x.debounce)
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			x.log.Warn("watcher error, will resync", "err", err)
			resync = true
			timer.Reset(x.debounce)
		case <-timer.C:
			if resync {
				if _, err := x.Sync(ctx); err != nil && ctx.Err() == nil {
					x.log.Error("resync failed", "err", err)
				}
				resync = false
				// Sync covered every pending path as well.
				clear(pending)
				continue
			}
			x.refresh(ctx, w, pending)
			clear(pending)
		}
	}
}

// hidden reports whether path is, or is inside, a dot-file or dot-folder under Knowledge/.
func (x *Indexer) hidden(path string) bool {
	rel, err := filepath.Rel(x.knowledge, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if strings.HasPrefix(part, ".") && part != "." {
			return true
		}
	}
	return false
}

// watchTree adds root and all folders beneath it to the watcher.
func (x *Indexer) watchTree(ctx context.Context, w *fsnotify.Watcher, root string) error {
	return x.walk(ctx, root, func(full string, d fs.DirEntry) error {
		if !d.IsDir() {
			return nil
		}
		return w.Add(full)
	})
}

// refresh brings the index in line with the current state of each pending path.
func (x *Indexer) refresh(ctx context.Context, w *fsnotify.Watcher, paths map[string]struct{}) {
	x.mu.Lock()
	defer x.mu.Unlock()
	for full := range paths {
		if ctx.Err() != nil {
			return
		}
		if err := x.refreshPath(ctx, w, full); err != nil && ctx.Err() == nil {
			x.log.Warn("update index failed", "path", full, "err", err)
		}
	}
}

func (x *Indexer) refreshPath(ctx context.Context, w *fsnotify.Watcher, full string) error {
	rel, err := filepath.Rel(x.knowledge, full)
	if err != nil || rel == "." {
		return err
	}
	info, err := os.Lstat(full)
	switch {
	case os.IsNotExist(err):
		return x.index.DeleteUnder(ctx, rel)
	case err != nil:
		return err
	case info.IsDir():
		// Watch first, then walk, so files created in between are not missed.
		if err := x.watchTree(ctx, w, full); err != nil {
			return err
		}
		return x.walk(ctx, full, func(path string, d fs.DirEntry) error {
			if d.IsDir() {
				return nil
			}
			rel, info, err := x.statFile(path, d)
			if err != nil {
				return err
			}
			return x.putFile(ctx, x.index, path, rel, info)
		})
	case info.Mode().IsRegular():
		return x.putFile(ctx, x.index, full, rel, info)
	default:
		// A symlink or special file now stands where an indexed file used to be.
		return x.index.DeleteUnder(ctx, rel)
	}
}
