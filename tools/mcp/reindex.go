package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type reindexInput struct{}

type reindexOutput struct {
	Complete           bool `json:"complete" jsonschema:"true when the new index was built, validated and switched in"`
	Notes              int  `json:"notes" jsonschema:"notes in the new index"`
	Attachments        int  `json:"attachments" jsonschema:"attachments in the new index"`
	NotesRemoved       int  `json:"notes_removed" jsonschema:"notes that disappeared while the index was being built"`
	AttachmentsRemoved int  `json:"attachments_removed" jsonschema:"attachments that disappeared while the index was being built"`
}

func (s *Server) addReindexTool() {
	sdk.AddTool(s.server, &sdk.Tool{
		Name: "reindex_knowledge",
		Description: "Rebuild the search index from the files on disk. The new index is built beside the old one, " +
			"checked, and only then switched in; if anything fails the old index stays. Notes and attachments " +
			"themselves are never changed.",
		Annotations: &sdk.ToolAnnotations{DestructiveHint: boolPtr(false), IdempotentHint: true},
	}, s.reindexKnowledge)
}

func (s *Server) reindexKnowledge(ctx context.Context, _ *sdk.CallToolRequest, _ reindexInput) (*sdk.CallToolResult, reindexOutput, error) {
	stats, err := s.indexer.Reindex(ctx)
	if err != nil {
		return nil, reindexOutput{}, err
	}
	return nil, reindexOutput{
		Complete:           true,
		Notes:              stats.NotesIndexed,
		Attachments:        stats.AttachmentsIndexed,
		NotesRemoved:       stats.NotesRemoved,
		AttachmentsRemoved: stats.AttachmentsRemoved,
	}, nil
}
