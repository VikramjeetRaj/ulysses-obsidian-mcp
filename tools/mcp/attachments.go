package mcp

import (
	"context"
	"encoding/base64"
	"mime"
	"path/filepath"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type attachmentInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size" jsonschema:"size in bytes"`
}

type searchAttachmentsOutput struct {
	Results []attachmentInfo `json:"results"`
}

type listAttachmentsOutput struct {
	Attachments []attachmentInfo `json:"attachments"`
	Total       int              `json:"total"`
	NextCursor  string           `json:"next_cursor,omitempty" jsonschema:"pass as cursor to get the next page; absent on the last page"`
}

type readAttachmentOutput struct {
	Path       string `json:"path"`
	MimeType   string `json:"mime_type"`
	Size       int    `json:"size" jsonschema:"size in bytes"`
	DataBase64 string `json:"data_base64"`
}

func (s *Server) addAttachmentTools() {
	readOnly := &sdk.ToolAnnotations{ReadOnlyHint: true}

	sdk.AddTool(s.server, &sdk.Tool{
		Name:        "search_attachments",
		Description: "Search attachment file names (images, PDFs and other non-Markdown files). File contents are not searched.",
		Annotations: readOnly,
	}, s.searchAttachments)

	sdk.AddTool(s.server, &sdk.Tool{
		Name:        "list_attachments",
		Description: "List the attachments directly inside a folder, one page at a time.",
		Annotations: readOnly,
	}, s.listAttachments)

	sdk.AddTool(s.server, &sdk.Tool{
		Name:        "read_attachment",
		Description: "Return an attachment's bytes as base64, up to 10 MiB. Attachments cannot be written or deleted through this server.",
		Annotations: readOnly,
	}, s.readAttachment)
}

func (s *Server) searchAttachments(ctx context.Context, _ *sdk.CallToolRequest, in searchInput) (*sdk.CallToolResult, searchAttachmentsOutput, error) {
	found, err := s.index.SearchAttachments(ctx, in.Query, in.Limit)
	if err != nil {
		return nil, searchAttachmentsOutput{}, err
	}
	out := searchAttachmentsOutput{Results: make([]attachmentInfo, 0, len(found))}
	for _, a := range found {
		out.Results = append(out.Results, attachmentInfo{Path: a.Path, Size: a.Size})
	}
	return nil, out, nil
}

func (s *Server) listAttachments(ctx context.Context, _ *sdk.CallToolRequest, in listInput) (*sdk.CallToolResult, listAttachmentsOutput, error) {
	folder, err := s.folder(in.Folder)
	if err != nil {
		return nil, listAttachmentsOutput{}, err
	}
	page, err := s.vault.ListAttachments(ctx, folder, in.Limit, in.Cursor)
	if err != nil {
		return nil, listAttachmentsOutput{}, err
	}
	out := listAttachmentsOutput{
		Attachments: make([]attachmentInfo, 0, len(page.Attachments)),
		Total:       page.Page.Total,
		NextCursor:  nextCursor(page.Page),
	}
	for _, a := range page.Attachments {
		out.Attachments = append(out.Attachments, attachmentInfo{Path: a.Path, Size: a.Size})
	}
	return nil, out, nil
}

func (s *Server) readAttachment(ctx context.Context, _ *sdk.CallToolRequest, in pathInput) (*sdk.CallToolResult, readAttachmentOutput, error) {
	path, err := s.attachmentPath(in.Path)
	if err != nil {
		return nil, readAttachmentOutput{}, err
	}
	data, err := s.vault.ReadAttachment(ctx, path)
	if err != nil {
		return nil, readAttachmentOutput{}, err
	}
	mimeType := mime.TypeByExtension(filepath.Ext(path))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return nil, readAttachmentOutput{
		Path:       in.Path,
		MimeType:   mimeType,
		Size:       len(data),
		DataBase64: base64.StdEncoding.EncodeToString(data),
	}, nil
}
