// Package mcp exposes the vault and its search index as MCP tools over stdio.
package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/VikramjeetRaj/ulysses-obsidian-mcp/lib/index"
	"github.com/VikramjeetRaj/ulysses-obsidian-mcp/lib/vault"
	"github.com/VikramjeetRaj/ulysses-obsidian-mcp/tools/indexer"
)

const instructions = `Search, read and write Markdown notes in the user's Obsidian-style Knowledge folder.

Paths are relative to the Knowledge folder, for example "Projects/idea.md". Absolute paths and ".." are rejected.
Notes end in .md. Everything else is an attachment, which is read-only here.

Every read_note, create_note and update_note result carries a revision. update_note and delete_note need the
revision you last read as expected_revision, and fail with a conflict if the note changed since; re-read it,
merge, and try again. create_note never overwrites. delete_note moves the note to .trash, it does not erase it.

Search finds titles, content and attachment file names, but results are short excerpts: call read_note for the
full text. The index follows file changes within about a second, so a note you just wrote may need a moment
before search finds it. If search ever seems wrong or stale, reindex_knowledge rebuilds the whole
index from the files on disk; it is safe to run at any time.`

// Server serves the MCP tools.
type Server struct {
	vault     *vault.Vault
	index     *index.Index
	indexer   *indexer.Indexer
	knowledge string // absolute path of <vault>/Knowledge
	log       *slog.Logger
	server    *sdk.Server
}

// New builds a Server whose tools work on the notes under knowledge.
func New(v *vault.Vault, idx *index.Index, ix *indexer.Indexer, knowledge string, log *slog.Logger) *Server {
	s := &Server{vault: v, index: idx, indexer: ix, knowledge: knowledge, log: log}
	s.server = sdk.NewServer(
		&sdk.Implementation{Name: "ulysses-obsidian-mcp", Version: "0.1.0"},
		&sdk.ServerOptions{Instructions: instructions},
	)
	s.addNoteTools()
	s.addAttachmentTools()
	s.addReindexTool()
	return s
}

// Run serves MCP over stdin and stdout until the client disconnects or ctx is cancelled.
// Standard output carries protocol messages only; diagnostics go to the log file.
func (s *Server) Run(ctx context.Context) error {
	return s.server.Run(ctx, &sdk.StdioTransport{})
}

// connect serves MCP over t. It exists so tests can use an in-memory transport.
func (s *Server) connect(ctx context.Context, t sdk.Transport) (*sdk.ServerSession, error) {
	return s.server.Connect(ctx, t, nil)
}

// abs turns a path relative to Knowledge/ into the absolute path the vault expects.
// The vault still checks the result stays inside Knowledge/, symlinks included.
func (s *Server) abs(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("%w: path is required", vault.ErrInvalidPath)
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return "", fmt.Errorf("%w: use a path relative to Knowledge/, not %q", vault.ErrInvalidPath, rel)
	}
	rel = filepath.FromSlash(rel)
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == ".." {
			return "", fmt.Errorf("%w: %q must not contain ..", vault.ErrInvalidPath, rel)
		}
	}
	return filepath.Join(s.knowledge, rel), nil
}

// folder is like abs, but an empty folder means Knowledge/ itself.
func (s *Server) folder(rel string) (string, error) {
	if rel == "" || rel == "." {
		return s.knowledge, nil
	}
	return s.abs(rel)
}

// notePath is like abs, and also requires a Markdown file name.
func (s *Server) notePath(rel string) (string, error) {
	if !isMarkdown(rel) {
		return "", fmt.Errorf("%w: notes must end in .md, got %q", vault.ErrInvalidPath, rel)
	}
	return s.abs(rel)
}

// attachmentPath is like abs, and also rejects Markdown files, which are notes.
func (s *Server) attachmentPath(rel string) (string, error) {
	if isMarkdown(rel) {
		return "", fmt.Errorf("%w: %q is a note, use read_note", vault.ErrInvalidPath, rel)
	}
	return s.abs(rel)
}

func isMarkdown(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".md")
}

// nextCursor returns the cursor for the page after p, or "" on the last page.
func nextCursor(p vault.Page) string {
	if p.Current*p.Size >= p.Total {
		return ""
	}
	return fmt.Sprint(p.Current + 1)
}

func boolPtr(b bool) *bool { return &b }
