package main_test

// End-to-end tests: each test builds a throwaway vault and index in a temp directory, starts the
// real server binary against it, and talks to it over stdio as an MCP client. "Obsidian" is played
// by the test itself writing, renaming and deleting files in the vault's Knowledge folder.
//
// Nothing outside the temp directories is read or written, so the real vault is never touched.
// Cleanup is automatic: every server process is stopped when its test ends, temp directories are
// removed by t.TempDir, and TestMain deletes the compiled binary.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	_ "modernc.org/sqlite"
)

var serverBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ulysses-e2e-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create temp dir:", err)
		os.Exit(1)
	}
	serverBinary = filepath.Join(dir, "ulysses-obsidian-mcp")
	build := exec.Command("go", "build", "-o", serverBinary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build server: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// ---- harness ---------------------------------------------------------------------------------

type site struct {
	t         *testing.T
	root      string // temp directory holding everything below
	cfgDir    string // the server's working directory, contains config.yaml
	knowledge string
	indexPath string
	logPath   string

	mu      sync.Mutex
	session *sdk.ClientSession
	cmd     *exec.Cmd
	stderr  *bytes.Buffer
}

func newSite(t *testing.T) *site {
	t.Helper()
	root := t.TempDir()
	s := &site{
		t:         t,
		root:      root,
		cfgDir:    filepath.Join(root, "cfg"),
		knowledge: filepath.Join(root, "vault", "Knowledge"),
		indexPath: filepath.Join(root, "state", "index.sqlite"),
		logPath:   filepath.Join(root, "logs", "server.log"),
	}
	for _, d := range []string{s.cfgDir, s.knowledge} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := fmt.Sprintf("vault: %s\nsearch_size: 1GB\nlog_path: %s\nindex_path: %s\n",
		filepath.Join(root, "vault"), s.logPath, s.indexPath)
	if err := os.WriteFile(filepath.Join(s.cfgDir, "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.stop()
		if t.Failed() {
			t.Logf("server stderr:\n%s", s.stderr)
			if log, err := os.ReadFile(s.logPath); err == nil {
				t.Logf("server log:\n%s", log)
			}
		}
	})
	return s
}

// start launches the server and connects an MCP client to it.
func (s *site) start() {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil {
		s.t.Fatal("server already running")
	}
	cmd := exec.Command(serverBinary)
	cmd.Dir = s.cfgDir
	s.stderr = &bytes.Buffer{}
	cmd.Stderr = s.stderr
	client := sdk.NewClient(&sdk.Implementation{Name: "e2e", Version: "0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &sdk.CommandTransport{Command: cmd, TerminateDuration: 15 * time.Second}, nil)
	if err != nil {
		s.t.Fatalf("start server: %v\nstderr: %s", err, s.stderr)
	}
	s.session, s.cmd = session, cmd
}

// stop disconnects the client, which makes the server shut down cleanly (flushing its index),
// and waits for the process to exit. Safe to call when nothing is running.
func (s *site) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil {
		return
	}
	s.session.Close()
	s.session = nil
	if s.cmd.ProcessState == nil {
		s.cmd.Process.Kill()
		s.cmd.Wait()
	}
	if !s.cmd.ProcessState.Success() {
		s.t.Errorf("server did not shut down cleanly: %v\nstderr: %s", s.cmd.ProcessState, s.stderr)
	}
}

// call runs a tool. It returns the tool's error text ("" on success) and decodes the structured
// result into out when out is non-nil.
func (s *site) call(name string, args map[string]any, out any) string {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := s.session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err.Error()
	}
	if res.IsError {
		var msg strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*sdk.TextContent); ok {
				msg.WriteString(tc.Text)
			}
		}
		return msg.String()
	}
	if out != nil {
		data, err := json.Marshal(res.StructuredContent)
		if err != nil {
			s.t.Fatal(err)
		}
		if err := json.Unmarshal(data, out); err != nil {
			s.t.Fatalf("decode %s result %s: %v", name, data, err)
		}
	}
	return ""
}

func (s *site) mustCall(name string, args map[string]any, out any) {
	s.t.Helper()
	if msg := s.call(name, args, out); msg != "" {
		s.t.Fatalf("%s failed: %s", name, msg)
	}
}

type hit struct {
	Path    string `json:"path"`
	Title   string `json:"title"`
	Excerpt string `json:"excerpt"`
}

type attachment struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// search returns the paths found by search_notes, in result order.
func (s *site) search(query string, limit int) []hit {
	s.t.Helper()
	var out struct {
		Results []hit `json:"results"`
	}
	args := map[string]any{"query": query}
	if limit > 0 {
		args["limit"] = limit
	}
	s.mustCall("search_notes", args, &out)
	return out.Results
}

func (s *site) searchPaths(query string) []string {
	s.t.Helper()
	var paths []string
	for _, h := range s.search(query, 0) {
		paths = append(paths, h.Path)
	}
	return paths
}

func (s *site) attachmentPaths(query string) []string {
	s.t.Helper()
	var out struct {
		Results []attachment `json:"results"`
	}
	s.mustCall("search_attachments", map[string]any{"query": query}, &out)
	var paths []string
	for _, a := range out.Results {
		paths = append(paths, a.Path)
	}
	return paths
}

// abs returns the on-disk path of a Knowledge-relative path.
func (s *site) abs(rel string) string {
	return filepath.Join(s.knowledge, filepath.FromSlash(rel))
}

// write creates or overwrites a file the way Obsidian would: directly on disk, folders as needed.
func (s *site) write(rel, content string) {
	s.t.Helper()
	if err := os.MkdirAll(filepath.Dir(s.abs(rel)), 0755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(s.abs(rel), []byte(content), 0644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *site) remove(rel string) {
	s.t.Helper()
	if err := os.Remove(s.abs(rel)); err != nil {
		s.t.Fatal(err)
	}
}

func (s *site) rename(from, to string) {
	s.t.Helper()
	if err := os.MkdirAll(filepath.Dir(s.abs(to)), 0755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.Rename(s.abs(from), s.abs(to)); err != nil {
		s.t.Fatal(err)
	}
}

// logLines returns the lines of the server's log file containing substr.
func (s *site) logLines(substr string) []string {
	data, _ := os.ReadFile(s.logPath)
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.Contains(l, substr) {
			lines = append(lines, l)
		}
	}
	return lines
}

// eventually polls cond until it holds. Index updates are asynchronous (a debounced file watcher
// and a background sync), so the tests wait for the outcome instead of sleeping.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// found reports whether a note search returns exactly the wanted paths (in any order).
func (s *site) foundExactly(query string, want ...string) bool {
	got := s.searchPaths(query)
	slices.Sort(got)
	want = slices.Clone(want)
	slices.Sort(want)
	return slices.Equal(got, want)
}

func (s *site) waitFor(what, query string, want ...string) {
	s.t.Helper()
	eventually(s.t, fmt.Sprintf("%s (search %q -> %v)", what, query, want), func() bool {
		return s.foundExactly(query, want...)
	})
}

func (s *site) waitAttachments(what, query string, want ...string) {
	s.t.Helper()
	eventually(s.t, fmt.Sprintf("%s (attachments %q -> %v)", what, query, want), func() bool {
		got := s.attachmentPaths(query)
		slices.Sort(got)
		w := slices.Clone(want)
		slices.Sort(w)
		return slices.Equal(got, w)
	})
}

// ---- 1. title and content search -------------------------------------------------------------

func TestE2ETitleAndContentSearch(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	s.start()

	s.mustCall("create_note", map[string]any{"path": "Projects/Tomato Garden.md", "content": "Plant seedlings in spring.\nWater daily."}, nil)
	s.mustCall("create_note", map[string]any{"path": "Journal/2026-10-08.md", "content": "Today I read about tomato blight and gardening."}, nil)
	s.mustCall("create_note", map[string]any{"path": "Misc/Recipes.md", "content": "pasta with tomato sauce"}, nil)

	// A word in one title and two bodies finds all three notes, title match first, each once.
	s.waitFor("all three notes found by 'tomato'", "tomato",
		"Projects/Tomato Garden.md", "Journal/2026-10-08.md", "Misc/Recipes.md")
	results := s.search("tomato", 0)
	if results[0].Path != "Projects/Tomato Garden.md" {
		t.Errorf("title match should rank first, got order %v", results)
	}
	if results[0].Title != "Tomato Garden" {
		t.Errorf("title = %q, want %q", results[0].Title, "Tomato Garden")
	}

	// Content-only search returns an excerpt around the match.
	got := s.search("blight", 0)
	if len(got) != 1 || got[0].Path != "Journal/2026-10-08.md" || !strings.Contains(got[0].Excerpt, "blight") {
		t.Errorf("content search 'blight' = %+v", got)
	}

	// Every word must match; case does not matter.
	if !s.foundExactly("tomato sauce", "Misc/Recipes.md") {
		t.Errorf("'tomato sauce' = %v", s.searchPaths("tomato sauce"))
	}
	if got := s.searchPaths("tomato quokka"); len(got) != 0 {
		t.Errorf("a word that is nowhere should give no results, got %v", got)
	}
	if !s.foundExactly("TOMATO BLIGHT", "Journal/2026-10-08.md") {
		t.Errorf("case-insensitive search = %v", s.searchPaths("TOMATO BLIGHT"))
	}

	// The limit is honoured, and hostile input is just text.
	if got := s.search("tomato", 2); len(got) != 2 {
		t.Errorf("limit 2 returned %d results", len(got))
	}
	for _, q := range []string{`"tomato" AND (`, `tomato*`, `NEAR(tomato blight)`, `-tomato`, `'`, "   "} {
		if msg := s.call("search_notes", map[string]any{"query": q}, nil); msg != "" {
			t.Errorf("query %q produced an error: %s", q, msg)
		}
	}
}

// ---- 2. attachment filename search -----------------------------------------------------------

func TestE2EAttachmentFilenameSearch(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	s.start()

	s.write("img/garden-plan.png", "content-needle inside the picture")
	s.write("docs/Quarterly Report.pdf", "pdf bytes")
	s.write("note.md", "a note that mentions plan")

	s.waitAttachments("hyphenated name split into words", "plan", "img/garden-plan.png")
	s.waitAttachments("name with a space", "quarterly report", "docs/Quarterly Report.pdf")
	s.waitAttachments("extension is part of the name", "pdf", "docs/Quarterly Report.pdf")

	// Only names are indexed: not the file's bytes, and notes are not attachments.
	if got := s.attachmentPaths("needle"); len(got) != 0 {
		t.Errorf("attachment contents were indexed: %v", got)
	}
	if got := s.attachmentPaths("mentions"); len(got) != 0 {
		t.Errorf("a note leaked into attachment search: %v", got)
	}
	if got := s.searchPaths("needle"); len(got) != 0 {
		t.Errorf("attachment contents leaked into note search: %v", got)
	}

	// The listing matches the disk.
	var listed struct {
		Attachments []attachment `json:"attachments"`
	}
	s.mustCall("list_attachments", map[string]any{"folder": "img"}, &listed)
	if len(listed.Attachments) != 1 || listed.Attachments[0].Path != "img/garden-plan.png" {
		t.Errorf("list_attachments = %+v", listed.Attachments)
	}

	// Rename and delete in Obsidian.
	s.rename("img/garden-plan.png", "img/garden-layout.png")
	s.waitAttachments("old name gone", "plan")
	s.waitAttachments("new name found", "layout", "img/garden-layout.png")

	s.remove("img/garden-layout.png")
	s.waitAttachments("deleted attachment gone", "layout")

	// A file dropped into a folder that did not exist when the server started.
	s.write("later/new-folder/diagram.svg", "<svg/>")
	s.waitAttachments("attachment in a new folder", "diagram", "later/new-folder/diagram.svg")
}

// ---- 3. new-note indexing --------------------------------------------------------------------

func TestE2ENewNoteIndexing(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	s.start()

	// Created through the MCP tool.
	s.mustCall("create_note", map[string]any{"path": "tool.md", "content": "made by the assistant: aardvark"}, nil)
	s.waitFor("tool-created note", "aardvark", "tool.md")

	// Created by Obsidian in folders that do not exist yet, then a second note in the same new
	// folder (the watcher must have started watching it).
	s.write("a/b/c/deep.md", "deep note about platypus")
	s.waitFor("note in new nested folders", "platypus", "a/b/c/deep.md")
	s.write("a/b/c/sibling.md", "sibling note about narwhal")
	s.waitFor("second note in the new folder", "narwhal", "a/b/c/sibling.md")

	// Non-ASCII text.
	s.write("unicode.md", "café 日本語 notes")
	s.waitFor("unicode note", "café", "unicode.md")

	// A large note is chunked: a word near the end is found, and the excerpt is a snippet, not
	// the whole note.
	big := strings.Repeat("filler text for the large note.\n", 6000) + "\nthe last line mentions chinchilla.\n"
	s.write("big.md", big)
	s.waitFor("word at the end of a 190 KB note", "chinchilla", "big.md")
	if got := s.search("chinchilla", 0); len(got) != 1 || len(got[0].Excerpt) > 1000 {
		t.Errorf("excerpt for a large note should be short: %d results", len(got))
	}

	// It is also readable through the tool, with a revision.
	var read struct {
		Content  string `json:"content"`
		Revision string `json:"revision"`
	}
	s.mustCall("read_note", map[string]any{"path": "a/b/c/deep.md"}, &read)
	if read.Content != "deep note about platypus" || read.Revision == "" {
		t.Errorf("read_note = %+v", read)
	}
}

// ---- 4. edits made in Obsidian ---------------------------------------------------------------

func TestE2EObsidianEdits(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	s.start()

	s.write("work/idea.md", "original zebra")
	s.waitFor("initial note", "zebra", "work/idea.md")
	var before struct {
		Revision string `json:"revision"`
	}
	s.mustCall("read_note", map[string]any{"path": "work/idea.md"}, &before)

	// Edit in place.
	s.write("work/idea.md", "revised walrus")
	s.waitFor("new text found", "walrus", "work/idea.md")
	s.waitFor("old text gone", "zebra")

	// The edit changed the revision, so a write based on the old read is refused.
	var after struct {
		Content  string `json:"content"`
		Revision string `json:"revision"`
	}
	s.mustCall("read_note", map[string]any{"path": "work/idea.md"}, &after)
	if after.Content != "revised walrus" || after.Revision == before.Revision {
		t.Errorf("read after edit = %+v (old revision %s)", after, before.Revision)
	}
	msg := s.call("update_note", map[string]any{"path": "work/idea.md", "content": "stale write", "expected_revision": before.Revision}, nil)
	if !strings.Contains(msg, "changed") {
		t.Errorf("stale update should be refused as a conflict, got %q", msg)
	}

	// Append (what a sync tool or an editor plugin might do).
	f, err := os.OpenFile(s.abs("work/idea.md"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\nappended capybara"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	s.waitFor("appended text", "capybara", "work/idea.md")
	s.waitFor("earlier text still there", "walrus", "work/idea.md")

	// Rename the note, then move it into another folder.
	s.rename("work/idea.md", "work/plan.md")
	s.waitFor("renamed note under its new path", "capybara", "work/plan.md")
	s.rename("work/plan.md", "archive/2026/plan.md")
	s.waitFor("moved note under its new path", "capybara", "archive/2026/plan.md")

	// Rename a whole folder.
	s.rename("archive", "old-archive")
	s.waitFor("folder rename", "capybara", "old-archive/2026/plan.md")

	// Hidden folders are Obsidian's own business and are never indexed. The visible note written
	// afterwards acts as a barrier: once it is found, the hidden one had its chance.
	s.write(".obsidian/workspace.md", "hiddenword belongs to obsidian")
	s.write("visible.md", "visibleword")
	s.waitFor("barrier note", "visibleword", "visible.md")
	if got := s.searchPaths("hiddenword"); len(got) != 0 {
		t.Errorf("a hidden folder was indexed: %v", got)
	}

	// Delete.
	if err := os.RemoveAll(s.abs("old-archive")); err != nil {
		t.Fatal(err)
	}
	s.waitFor("deleted folder's notes gone", "capybara")
}

// ---- 5. restart reconciliation ---------------------------------------------------------------

func TestE2ERestartReconciliation(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	s.start()

	s.write("a.md", "alpha original")
	s.write("b.md", "bravo doomed")
	s.write("c.md", "charlie renamed")
	s.write("e.md", "echo untouched")
	s.write("x.png", "x")
	s.write("y.png", "y")
	s.waitFor("a", "alpha", "a.md")
	s.waitFor("b", "bravo", "b.md")
	s.waitFor("c", "charlie", "c.md")
	s.waitFor("e", "echo", "e.md")
	s.waitAttachments("attachments", "png", "x.png", "y.png")
	s.stop()

	if info, err := os.Stat(s.indexPath); err != nil || info.Size() == 0 {
		t.Fatalf("the index should persist on disk while the server is stopped: %v", err)
	}
	firstRunSyncs := len(s.logLines(`msg="index synced"`))

	// Changes made while the server is not running.
	s.write("a.md", "alpha revised") // edit (size changes)
	s.remove("b.md")                 // delete
	s.rename("c.md", "c2.md")        // rename
	s.write("d.md", "delta brand new")
	s.remove("x.png")
	s.write("z.png", "z")

	s.start()
	s.waitFor("edit picked up", "revised", "a.md")
	s.waitFor("old text gone", "original")
	s.waitFor("deleted note gone", "bravo")
	s.waitFor("renamed note moved", "charlie", "c2.md")
	s.waitFor("new note found", "delta", "d.md")
	s.waitFor("untouched note still found", "echo", "e.md")
	s.waitAttachments("attachments reconciled", "png", "y.png", "z.png")

	// The server reused the persisted index instead of starting from scratch: the sync after the
	// restart only re-read what changed (a, c2, d) and kept e, y, and dropped b, c, x.
	eventually(t, "sync log line after restart", func() bool {
		return len(s.logLines(`msg="index synced"`)) > firstRunSyncs
	})
	lines := s.logLines(`msg="index synced"`)
	last := lines[len(lines)-1]
	for _, want := range []string{
		"notes_indexed=3", "notes_unchanged=1", "notes_removed=2",
		"attachments_indexed=1", "attachments_unchanged=1", "attachments_removed=1",
	} {
		if !strings.Contains(last, want) {
			t.Errorf("restart sync log %q does not contain %q", last, want)
		}
	}
}

// ---- 6. full reindex -------------------------------------------------------------------------

// tamper runs SQL directly against the index file of a stopped server.
func tamper(t *testing.T, indexPath string, statements ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+indexPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

func TestE2EFullReindex(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	s.start()
	s.mustCall("create_note", map[string]any{"path": "one.md", "content": "alpha content"}, nil)
	s.mustCall("create_note", map[string]any{"path": "dir/two.md", "content": "bravo content"}, nil)
	s.write("pic.png", "png")
	s.waitFor("one", "alpha", "one.md")
	s.waitFor("two", "bravo", "dir/two.md")
	s.waitAttachments("pic", "pic", "pic.png")
	s.stop()

	// Damage the index in ways a normal sync cannot see: extra text in existing notes (their size
	// and modification time still match the disk) plus a row for a note that does not exist.
	tamper(t, s.indexPath,
		`UPDATE chunks SET content = content || ' ghostword'`,
		`INSERT INTO notes(path, title, revision, size, mtime_ns) VALUES ('ghost.md', 'ghost', 'r', 1, 1)`,
		`INSERT INTO chunks(path, seq, content) VALUES ('ghost.md', 0, 'phantomword')`,
	)

	s.start()
	// The startup sync removes the phantom note, but cannot tell the real notes were altered.
	s.waitFor("startup sync drops the phantom note", "phantomword")
	if got := s.searchPaths("ghostword"); len(got) != 2 {
		t.Fatalf("premise: tampered content should survive the startup sync, got %v", got)
	}

	var out struct {
		Complete           bool `json:"complete"`
		Notes              int  `json:"notes"`
		Attachments        int  `json:"attachments"`
		NotesRemoved       int  `json:"notes_removed"`
		AttachmentsRemoved int  `json:"attachments_removed"`
	}
	s.mustCall("reindex_knowledge", map[string]any{}, &out)
	if !out.Complete || out.Notes != 2 || out.Attachments != 1 {
		t.Fatalf("reindex_knowledge = %+v, want complete with 2 notes and 1 attachment", out)
	}
	if got := s.searchPaths("ghostword"); len(got) != 0 {
		t.Errorf("tampered content survived the reindex: %v", got)
	}
	s.waitFor("real content after reindex", "alpha", "one.md")
	s.waitFor("real content after reindex", "bravo", "dir/two.md")
	s.waitAttachments("attachment after reindex", "pic", "pic.png")
	if _, err := os.Stat(s.indexPath + ".new"); !os.IsNotExist(err) {
		t.Errorf("the temporary index generation was left behind: %v", err)
	}

	// The new generation is the live one: later writes and file changes still reach search.
	s.mustCall("create_note", map[string]any{"path": "three.md", "content": "charlie content"}, nil)
	s.waitFor("tool write after reindex", "charlie", "three.md")
	s.write("one.md", "alpha rewritten delta")
	s.waitFor("watcher after reindex", "delta", "one.md")

	// Edits made while the rebuild is running are not lost.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			s.write(fmt.Sprintf("burst/n%02d.md", i), fmt.Sprintf("burstword%02d", i))
			time.Sleep(10 * time.Millisecond)
		}
	}()
	for i := 0; i < 3; i++ {
		s.mustCall("reindex_knowledge", map[string]any{}, &out)
		if !out.Complete {
			t.Fatalf("reindex during edits: %+v", out)
		}
	}
	wg.Wait()
	for i := 0; i < 40; i++ {
		word := fmt.Sprintf("burstword%02d", i)
		s.waitFor("note written during reindex: "+word, word, fmt.Sprintf("burst/n%02d.md", i))
	}

	// And the rebuilt index survives a restart.
	s.stop()
	s.start()
	s.waitFor("after restart", "charlie", "three.md")
	s.waitFor("after restart", "burstword39", "burst/n39.md")
	if got := s.searchPaths("ghostword"); len(got) != 0 {
		t.Errorf("tampered content came back after restart: %v", got)
	}
}

// ---- 7. index corruption recovery ------------------------------------------------------------

func TestE2EIndexCorruptionRecovery(t *testing.T) {
	t.Parallel()
	damages := map[string]func(t *testing.T, indexPath string){
		"garbage file": func(t *testing.T, indexPath string) {
			if err := os.WriteFile(indexPath, bytes.Repeat([]byte("not a database "), 1000), 0644); err != nil {
				t.Fatal(err)
			}
		},
		"truncated file": func(t *testing.T, indexPath string) {
			info, err := os.Stat(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(indexPath, info.Size()/2); err != nil {
				t.Fatal(err)
			}
		},
		"overwritten header": func(t *testing.T, indexPath string) {
			f, err := os.OpenFile(indexPath, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := f.WriteAt(bytes.Repeat([]byte{0xff}, 200), 0); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, damage := range damages {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newSite(t)
			s.start()
			for i := 0; i < 30; i++ {
				s.write(fmt.Sprintf("n%02d.md", i), fmt.Sprintf("marker%02d %s", i, strings.Repeat("padding words ", 200)))
			}
			s.write("pic.png", "png")
			s.waitFor("last note indexed", "marker29", "n29.md")
			s.waitAttachments("attachment indexed", "pic", "pic.png")
			s.stop()

			damage(t, s.indexPath)
			os.Remove(s.indexPath + "-wal")
			os.Remove(s.indexPath + "-shm")

			// The server must come up anyway and rebuild what it lost from the notes.
			s.start()
			s.waitFor("note found again after recovery", "marker00", "n00.md")
			s.waitFor("last note found again", "marker29", "n29.md")
			s.waitAttachments("attachment found again", "pic", "pic.png")

			// The damaged file was kept for inspection, not deleted, and the event was logged.
			kept, _ := filepath.Glob(s.indexPath + ".corrupt-*")
			if len(kept) != 1 {
				t.Errorf("quarantined index files = %v, want exactly one", kept)
			}
			if len(s.logLines("index corrupt")) == 0 {
				t.Error("recovery was not logged")
			}

			// Fully working afterwards.
			s.mustCall("create_note", map[string]any{"path": "after.md", "content": "written after recovery"}, nil)
			s.waitFor("write after recovery", "recovery", "after.md")
			var out struct {
				Complete bool `json:"complete"`
				Notes    int  `json:"notes"`
			}
			s.mustCall("reindex_knowledge", map[string]any{}, &out)
			if !out.Complete || out.Notes != 31 {
				t.Errorf("reindex after recovery = %+v, want 31 notes", out)
			}
		})
	}
}

// ---- cleanup ---------------------------------------------------------------------------------

// TestE2EShutdownLeavesNothingBehind checks the hygiene the other tests rely on: a clean stop
// exits 0, leaves the index consistent, and leaves no temporary generation or WAL behind.
func TestE2EShutdownLeavesNothingBehind(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	s.start()
	s.mustCall("create_note", map[string]any{"path": "a.md", "content": "one note"}, nil)
	s.waitFor("note indexed", "note", "a.md")
	s.mustCall("reindex_knowledge", map[string]any{}, nil)
	s.stop() // fails the test if the exit code is not 0

	entries, err := os.ReadDir(filepath.Dir(s.indexPath))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	for _, n := range names {
		if n != "index.sqlite" {
			t.Errorf("unexpected leftover next to the index: %q (all: %v)", n, names)
		}
	}
	// A graceful stop checkpoints the WAL, so the main file alone is a sound database.
	db, err := sql.Open("sqlite", "file:"+s.indexPath+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil || result != "ok" {
		t.Errorf("integrity_check = %q, %v", result, err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM notes").Scan(&n); err != nil || n != 1 {
		t.Errorf("notes in the stopped index = %d, %v", n, err)
	}
}
