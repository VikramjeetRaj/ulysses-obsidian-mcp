package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/VikramjeetRaj/ulysses-obsidian-mcp/lib/index"
	"github.com/VikramjeetRaj/ulysses-obsidian-mcp/lib/vault"
	"github.com/VikramjeetRaj/ulysses-obsidian-mcp/tools/indexer"
)

var bg = context.Background()

type env struct {
	t         *testing.T
	session   *sdk.ClientSession
	knowledge string
	sync      func()
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newEnv wires a real vault, index and indexer in a temp directory to a server, and connects
// an in-memory client to it.
func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	v, err := vault.New(root, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	knowledge := filepath.Join(root, "Knowledge")
	idx, err := index.New(filepath.Join(t.TempDir(), "index.sqlite"), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	ix, err := indexer.New(knowledge, idx, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	srv := New(v, idx, ix, knowledge, testLogger())
	serverT, clientT := sdk.NewInMemoryTransports()
	ss, err := srv.connect(bg, serverT)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test"}, nil).Connect(bg, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return &env{t: t, session: cs, knowledge: knowledge, sync: func() {
		t.Helper()
		if _, err := ix.Sync(bg); err != nil {
			t.Fatal(err)
		}
	}}
}

// call runs a tool and decodes its structured result into out (if non-nil).
// It returns the tool's error text, or "" on success.
func (e *env) call(name string, args map[string]any, out any) string {
	e.t.Helper()
	res, err := e.session.CallTool(bg, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		// Input validation errors can surface as protocol errors.
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
			e.t.Fatal(err)
		}
		if err := json.Unmarshal(data, out); err != nil {
			e.t.Fatalf("decode %s result %s: %v", name, data, err)
		}
	}
	return ""
}

func (e *env) mustCall(name string, args map[string]any, out any) {
	e.t.Helper()
	if msg := e.call(name, args, out); msg != "" {
		e.t.Fatalf("%s failed: %s", name, msg)
	}
}

func TestToolsAreListed(t *testing.T) {
	e := newEnv(t)
	res, err := e.session.ListTools(bg, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{"create_note", "delete_note", "list_attachments", "list_notes", "read_attachment",
		"read_note", "reindex_knowledge", "search_attachments", "search_notes", "update_note"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
}

func TestNoteLifecycle(t *testing.T) {
	e := newEnv(t)

	var created revisionOutput
	e.mustCall("create_note", map[string]any{"path": "Projects/idea.md", "content": "tomatoes need sunlight"}, &created)
	if created.Path != "Projects/idea.md" || created.Revision == "" {
		t.Fatalf("created = %+v", created)
	}
	if _, err := os.Stat(filepath.Join(e.knowledge, "Projects", "idea.md")); err != nil {
		t.Fatalf("note not on disk: %v", err)
	}

	var read readNoteOutput
	e.mustCall("read_note", map[string]any{"path": "Projects/idea.md"}, &read)
	if read.Content != "tomatoes need sunlight" || read.Revision != created.Revision {
		t.Fatalf("read = %+v", read)
	}

	// create never overwrites
	if msg := e.call("create_note", map[string]any{"path": "Projects/idea.md", "content": "other"}, nil); !strings.Contains(msg, "already exists") {
		t.Fatalf("second create: %q", msg)
	}

	var updated revisionOutput
	e.mustCall("update_note", map[string]any{"path": "Projects/idea.md", "content": "tomatoes need water", "expected_revision": created.Revision}, &updated)
	if updated.Revision == created.Revision {
		t.Fatal("revision did not change")
	}
	// the old revision is now stale
	if msg := e.call("update_note", map[string]any{"path": "Projects/idea.md", "content": "lost", "expected_revision": created.Revision}, nil); !strings.Contains(msg, "changed") {
		t.Fatalf("stale update: %q", msg)
	}
	e.mustCall("read_note", map[string]any{"path": "Projects/idea.md"}, &read)
	if read.Content != "tomatoes need water" {
		t.Fatalf("content after rejected update = %q", read.Content)
	}

	// delete needs the current revision and only moves the note
	if msg := e.call("delete_note", map[string]any{"path": "Projects/idea.md", "expected_revision": created.Revision}, nil); !strings.Contains(msg, "changed") {
		t.Fatalf("stale delete: %q", msg)
	}
	var deleted deleteNoteOutput
	e.mustCall("delete_note", map[string]any{"path": "Projects/idea.md", "expected_revision": updated.Revision}, &deleted)
	if deleted.TrashedTo != filepath.Join(".trash", "Projects", "idea.md") {
		t.Fatalf("trashed_to = %q", deleted.TrashedTo)
	}
	if _, err := os.Stat(filepath.Join(e.knowledge, deleted.TrashedTo)); err != nil {
		t.Fatalf("trashed note missing: %v", err)
	}
	if msg := e.call("read_note", map[string]any{"path": "Projects/idea.md"}, nil); !strings.Contains(msg, "not found") {
		t.Fatalf("read after delete: %q", msg)
	}
}

func TestPathsAreConfinedToKnowledge(t *testing.T) {
	e := newEnv(t)
	outside := filepath.Join(filepath.Dir(e.knowledge), "secret.md")
	if err := os.WriteFile(outside, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(e.knowledge, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(e.knowledge), filepath.Join(e.knowledge, "linkdir")); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{
		"absolute":         outside,
		"leading slash":    "/etc/passwd.md",
		"parent traversal": "../secret.md",
		"embedded ..":      "a/../../secret.md",
		"empty":            "",
		"not markdown":     "notes.txt",
		"symlinked file":   "link.md",
		"symlinked folder": "linkdir/secret.md",
	} {
		for _, tool := range []string{"read_note", "create_note", "update_note", "delete_note"} {
			args := map[string]any{"path": path, "content": "x", "expected_revision": "r"}
			if msg := e.call(tool, args, nil); msg == "" {
				t.Errorf("%s accepted %s path %q", tool, name, path)
			}
		}
	}
	if got, _ := os.ReadFile(outside); string(got) != "secret" {
		t.Fatalf("outside file modified: %q", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(e.knowledge), "notes.txt")); err == nil {
		t.Fatal("file created outside Knowledge")
	}
	if msg := e.call("list_notes", map[string]any{"folder": "../"}, nil); msg == "" {
		t.Error("list_notes accepted ..")
	}
}

func TestSearchFindsNotesAfterSync(t *testing.T) {
	e := newEnv(t)
	e.mustCall("create_note", map[string]any{"path": "Garden.md", "content": "all about gardening"}, nil)
	e.mustCall("create_note", map[string]any{"path": "Cooking.md", "content": "gardening herbs for the kitchen"}, nil)
	e.sync()

	var out searchNotesOutput
	e.mustCall("search_notes", map[string]any{"query": "gardening"}, &out)
	if len(out.Results) != 2 {
		t.Fatalf("results = %+v", out.Results)
	}
	e.mustCall("search_notes", map[string]any{"query": "gardening", "limit": 1}, &out)
	if len(out.Results) != 1 {
		t.Fatalf("limit ignored: %+v", out.Results)
	}
	e.mustCall("search_notes", map[string]any{"query": "nothing matches this"}, &out)
	if out.Results == nil || len(out.Results) != 0 {
		t.Fatalf("results = %#v, want empty list", out.Results)
	}
}

func TestListNotesPaginates(t *testing.T) {
	e := newEnv(t)
	for _, name := range []string{"a", "b", "c"} {
		e.mustCall("create_note", map[string]any{"path": "dir/" + name + ".md", "content": name}, nil)
	}
	var page listNotesOutput
	e.mustCall("list_notes", map[string]any{"folder": "dir", "limit": 2}, &page)
	if len(page.Notes) != 2 || page.Total != 3 || page.NextCursor != "2" || page.Notes[0].Revision == "" {
		t.Fatalf("page 1 = %+v", page)
	}
	var last listNotesOutput
	e.mustCall("list_notes", map[string]any{"folder": "dir", "limit": 2, "cursor": page.NextCursor}, &last)
	if len(last.Notes) != 1 || last.NextCursor != "" || last.Notes[0].Path != filepath.Join("dir", "c.md") {
		t.Fatalf("page 2 = %+v", last)
	}
}

func TestAttachmentTools(t *testing.T) {
	e := newEnv(t)
	img := []byte("\x89PNG\r\n\x1a\nfake")
	if err := os.MkdirAll(filepath.Join(e.knowledge, "img"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.knowledge, "img", "garden-plan.png"), img, 0644); err != nil {
		t.Fatal(err)
	}
	e.mustCall("create_note", map[string]any{"path": "note.md", "content": "x"}, nil)
	e.sync()

	var found searchAttachmentsOutput
	e.mustCall("search_attachments", map[string]any{"query": "plan"}, &found)
	if len(found.Results) != 1 || found.Results[0].Path != filepath.Join("img", "garden-plan.png") || found.Results[0].Size != int64(len(img)) {
		t.Fatalf("search = %+v", found)
	}

	var listed listAttachmentsOutput
	e.mustCall("list_attachments", map[string]any{"folder": "img"}, &listed)
	if len(listed.Attachments) != 1 || listed.Total != 1 || listed.NextCursor != "" {
		t.Fatalf("list = %+v", listed)
	}
	var top listAttachmentsOutput
	e.mustCall("list_attachments", nil, &top)
	if len(top.Attachments) != 0 {
		t.Fatalf("top level lists notes or folders as attachments: %+v", top)
	}

	var read readAttachmentOutput
	e.mustCall("read_attachment", map[string]any{"path": "img/garden-plan.png"}, &read)
	data, err := base64.StdEncoding.DecodeString(read.DataBase64)
	if err != nil || string(data) != string(img) || read.MimeType != "image/png" || read.Size != len(img) {
		t.Fatalf("read = %+v (decode err %v)", read, err)
	}
	if msg := e.call("read_attachment", map[string]any{"path": "note.md"}, nil); !strings.Contains(msg, "read_note") {
		t.Fatalf("read_attachment on a note: %q", msg)
	}
	if msg := e.call("read_attachment", map[string]any{"path": "img/missing.png"}, nil); !strings.Contains(msg, "not found") {
		t.Fatalf("missing attachment: %q", msg)
	}
}

func TestReindexKnowledge(t *testing.T) {
	e := newEnv(t)
	e.mustCall("create_note", map[string]any{"path": "a.md", "content": "alpha words"}, nil)
	e.mustCall("create_note", map[string]any{"path": "dir/b.md", "content": "bravo words"}, nil)
	if err := os.WriteFile(filepath.Join(e.knowledge, "pic.png"), []byte("png"), 0644); err != nil {
		t.Fatal(err)
	}

	var out reindexOutput
	e.mustCall("reindex_knowledge", map[string]any{}, &out)
	if !out.Complete || out.Notes != 2 || out.Attachments != 1 {
		t.Fatalf("reindex = %+v, want complete with 2 notes and 1 attachment", out)
	}
	var found searchNotesOutput
	e.mustCall("search_notes", map[string]any{"query": "bravo"}, &found)
	if len(found.Results) != 1 {
		t.Fatalf("search after reindex: %+v", found)
	}
	var atts searchAttachmentsOutput
	e.mustCall("search_attachments", map[string]any{"query": "pic"}, &atts)
	if len(atts.Results) != 1 {
		t.Fatalf("attachment search after reindex: %+v", atts)
	}
}
