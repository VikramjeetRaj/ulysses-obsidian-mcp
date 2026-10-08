package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type searchInput struct {
	Query string `json:"query" jsonschema:"words to find; every word must match"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum results, default 20, at most 100"`
}

type noteHit struct {
	Path    string `json:"path"`
	Title   string `json:"title"`
	Excerpt string `json:"excerpt" jsonschema:"short piece of the matching text"`
}

type searchNotesOutput struct {
	Results []noteHit `json:"results" jsonschema:"title matches first, then content matches"`
}

type listInput struct {
	Folder string `json:"folder,omitempty" jsonschema:"folder relative to Knowledge, empty for the top level; not recursive"`
	Limit  int    `json:"limit,omitempty" jsonschema:"page size, default 50, at most 200"`
	Cursor string `json:"cursor,omitempty" jsonschema:"next_cursor from the previous page"`
}

type noteInfo struct {
	Path     string `json:"path"`
	Revision string `json:"revision"`
}

type listNotesOutput struct {
	Notes      []noteInfo `json:"notes"`
	Total      int        `json:"total"`
	NextCursor string     `json:"next_cursor,omitempty" jsonschema:"pass as cursor to get the next page; absent on the last page"`
}

type pathInput struct {
	Path string `json:"path" jsonschema:"path relative to Knowledge, for example Projects/idea.md"`
}

type readNoteOutput struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Revision string `json:"revision"`
}

type createNoteInput struct {
	Path    string `json:"path" jsonschema:"new note path relative to Knowledge, ending in .md; missing folders are created"`
	Content string `json:"content" jsonschema:"Markdown text"`
}

type updateNoteInput struct {
	Path             string `json:"path" jsonschema:"path relative to Knowledge"`
	Content          string `json:"content" jsonschema:"the complete new Markdown text; it replaces the note"`
	ExpectedRevision string `json:"expected_revision" jsonschema:"the revision from your last read_note; the update fails if the note changed since"`
}

type deleteNoteInput struct {
	Path             string `json:"path" jsonschema:"path relative to Knowledge"`
	ExpectedRevision string `json:"expected_revision" jsonschema:"the revision from your last read_note; the delete fails if the note changed since"`
}

type revisionOutput struct {
	Path     string `json:"path"`
	Revision string `json:"revision" jsonschema:"revision of the note as written; use it as expected_revision next time"`
}

type deleteNoteOutput struct {
	Path      string `json:"path"`
	TrashedTo string `json:"trashed_to" jsonschema:"where the note now lives, relative to Knowledge; move it back to restore"`
}

func (s *Server) addNoteTools() {
	readOnly := &sdk.ToolAnnotations{ReadOnlyHint: true}

	sdk.AddTool(s.server, &sdk.Tool{
		Name:        "search_notes",
		Description: "Search note titles and content. Returns paths with short excerpts; use read_note for full text.",
		Annotations: readOnly,
	}, s.searchNotes)

	sdk.AddTool(s.server, &sdk.Tool{
		Name:        "list_notes",
		Description: "List the notes directly inside a folder, with their revisions, one page at a time.",
		Annotations: readOnly,
	}, s.listNotes)

	sdk.AddTool(s.server, &sdk.Tool{
		Name:        "read_note",
		Description: "Return a note's Markdown content and its revision.",
		Annotations: readOnly,
	}, s.readNote)

	sdk.AddTool(s.server, &sdk.Tool{
		Name:        "create_note",
		Description: "Create a note. Fails if the path already exists; it never overwrites.",
		Annotations: &sdk.ToolAnnotations{DestructiveHint: boolPtr(false)},
	}, s.createNote)

	sdk.AddTool(s.server, &sdk.Tool{
		Name:        "update_note",
		Description: "Replace a note's whole content, only if expected_revision is still current. On a conflict, read the note again and retry.",
		Annotations: &sdk.ToolAnnotations{IdempotentHint: true},
	}, s.updateNote)

	sdk.AddTool(s.server, &sdk.Tool{
		Name:        "delete_note",
		Description: "Move a note to Knowledge/.trash, only if expected_revision is still current. The note is not erased and can be moved back.",
	}, s.deleteNote)
}

func (s *Server) searchNotes(ctx context.Context, _ *sdk.CallToolRequest, in searchInput) (*sdk.CallToolResult, searchNotesOutput, error) {
	hits, err := s.index.SearchNotes(ctx, in.Query, in.Limit)
	if err != nil {
		return nil, searchNotesOutput{}, err
	}
	out := searchNotesOutput{Results: make([]noteHit, 0, len(hits))}
	for _, h := range hits {
		out.Results = append(out.Results, noteHit{Path: h.Path, Title: h.Title, Excerpt: h.Excerpt})
	}
	return nil, out, nil
}

func (s *Server) listNotes(ctx context.Context, _ *sdk.CallToolRequest, in listInput) (*sdk.CallToolResult, listNotesOutput, error) {
	folder, err := s.folder(in.Folder)
	if err != nil {
		return nil, listNotesOutput{}, err
	}
	page, err := s.vault.ListNotes(ctx, folder, in.Limit, in.Cursor)
	if err != nil {
		return nil, listNotesOutput{}, err
	}
	out := listNotesOutput{Notes: make([]noteInfo, 0, len(page.Notes)), Total: page.Page.Total, NextCursor: nextCursor(page.Page)}
	for _, n := range page.Notes {
		out.Notes = append(out.Notes, noteInfo{Path: n.Path, Revision: n.HashVal})
	}
	return nil, out, nil
}

func (s *Server) readNote(ctx context.Context, _ *sdk.CallToolRequest, in pathInput) (*sdk.CallToolResult, readNoteOutput, error) {
	path, err := s.notePath(in.Path)
	if err != nil {
		return nil, readNoteOutput{}, err
	}
	note, err := s.vault.ReadNote(ctx, path)
	if err != nil {
		return nil, readNoteOutput{}, err
	}
	return nil, readNoteOutput{Path: note.Path, Content: note.Content, Revision: note.HashVal}, nil
}

func (s *Server) createNote(ctx context.Context, _ *sdk.CallToolRequest, in createNoteInput) (*sdk.CallToolResult, revisionOutput, error) {
	path, err := s.notePath(in.Path)
	if err != nil {
		return nil, revisionOutput{}, err
	}
	revision, err := s.vault.CreateNote(ctx, path, in.Content)
	if err != nil {
		return nil, revisionOutput{}, err
	}
	return nil, revisionOutput{Path: in.Path, Revision: revision}, nil
}

func (s *Server) updateNote(ctx context.Context, _ *sdk.CallToolRequest, in updateNoteInput) (*sdk.CallToolResult, revisionOutput, error) {
	path, err := s.notePath(in.Path)
	if err != nil {
		return nil, revisionOutput{}, err
	}
	revision, err := s.vault.UpdateNote(ctx, path, in.Content, in.ExpectedRevision)
	if err != nil {
		return nil, revisionOutput{}, err
	}
	return nil, revisionOutput{Path: in.Path, Revision: revision}, nil
}

func (s *Server) deleteNote(ctx context.Context, _ *sdk.CallToolRequest, in deleteNoteInput) (*sdk.CallToolResult, deleteNoteOutput, error) {
	path, err := s.notePath(in.Path)
	if err != nil {
		return nil, deleteNoteOutput{}, err
	}
	trashed, err := s.vault.TrashNote(ctx, path, in.ExpectedRevision)
	if err != nil {
		return nil, deleteNoteOutput{}, err
	}
	return nil, deleteNoteOutput{Path: in.Path, TrashedTo: trashed}, nil
}
