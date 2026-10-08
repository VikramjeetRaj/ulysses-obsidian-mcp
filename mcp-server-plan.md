# Implementation plan: personal Obsidian MCP server

## Goal

Build a Go MCP server that lets local Codex and ChatGPT search and maintain Markdown notes under `<vault>/Knowledge`. Codex connects directly over stdio; ChatGPT connects to the same executable through a personal MCP plugin and Secure MCP Tunnel.

The vault's Markdown files remain the source of truth. The search index is rebuildable and is never treated as a note.

## Current state

- A starter `config.yaml`, Go entry point, and daily file logger exist in this directory.
- The starter has not yet completed build and behavior verification.
- MCP tools, indexing, and client connections have not been implemented.

## Phase 1 — Configuration and daily logging

1. Keep `config.yaml` limited to the requested keys: `vault`, `search_size`, and `log_path`.
2. Resolve relative `log_path` values beside the config file. Treat it as a base filename and write `name-YYYY-MM-DD.log` using the Mac's local date.
3. Validate that `vault` is an absolute directory and `search_size` is a positive size. Default the supplied file to `1GB`; use it as the maximum search response size.
4. Rotate logs at local midnight and on the first write after a date change. Keep standard output exclusively for MCP messages; write diagnostics to the dated file or standard error before logging is initialized.
5. Verify a normal start and stop, configuration errors, file permissions, and a simulated day change.

**Done when:** the Go program builds, reads the three keys, and writes to the correct dated log files without writing logs to standard output.

## Phase 2 — Vault access and note consistency

1. Create `Knowledge/` if missing. Limit all note and attachment paths to that subtree.
2. Reject absolute input paths, `..` traversal, non-Markdown write targets, and symlinks that escape the allowed subtree.
3. Implement note reads with a content-derived revision, such as SHA-256.
4. Implement create-only writes and conditional updates using `expected_revision`. Write through a temporary file and replace the destination atomically where supported.
5. Preserve existing note content unless an explicit update supplies replacement content. Report conflicts so the client can reread and reconcile user edits.

**Done when:** reads and writes stay inside `Knowledge/`, creates never overwrite, and stale updates leave the note untouched.

## Phase 3 — Persistent search index

1. Use a local SQLite database with FTS5 for note titles and content. Index attachment filenames separately; do not index attachment contents.
2. Store the database outside `Knowledge/`, so it does not appear as an Obsidian note or index itself.
3. Split large Markdown content into bounded chunks before indexing. Return short excerpts and note paths from searches; read full content only through `read_note`.
4. Enable SQLite WAL mode and a busy timeout so the Codex and tunnel server processes can read and update the same database.
5. On first start, build the index by walking `Knowledge/`. On later starts, reconcile indexed file metadata with the filesystem.
6. Watch all directories under `Knowledge/` for Obsidian edits and new files. Add watches for new directories, debounce event bursts, and recheck the actual file state before updating the index. Reconcile periodically or on restart to recover missed events.
7. Update the index after successful MCP note writes. Make repeated indexing of the same file revision harmless.
8. Implement a full reindex using a new index generation. Account for edits made during rebuilding, validate the new generation, then switch searches to it. Keep the previous generation if rebuilding fails.

**Done when:** new, changed, renamed, and removed notes and attachments are reflected in search, including changes made while the server was stopped; failed reindexing preserves usable search.

## Phase 4 — MCP tools

Expose these tools with explicit input schemas and structured results:

| Tool | Behavior |
| --- | --- |
| `search_notes` | Search titles, content, or both under `Knowledge/`; support folder, limit, and cursor. Rank title hits above content hits. |
| `search_attachments` | Search attachment filenames under `Knowledge/`; support folder, limit, and cursor. |
| `list_notes` | List Markdown notes in a folder with pagination. |
| `read_note` | Return Markdown content and revision. |
| `create_note` | Create a note only when absent. |
| `update_note` | Replace a note only when `expected_revision` matches. |
| `reindex_knowledge` | Rebuild note and attachment indexes; return counts, completion state, and errors. |

The default maximum search response size is the configured `search_size` (`1GB` in the supplied config). Keep individual pages and excerpts small by default. Return a continuation cursor or a clear size error instead of silently truncating data. An individual note may be up to 1GB as specified in the PRD; process indexing in chunks so that limit does not imply loading an entire note into memory.

Do not expose a shell or delete tool. Search, list, and read are read-only; note create and update modify vault files; reindex modifies only the database.

**Done when:** every tool works through an MCP test client and returns useful errors for invalid paths, stale revisions, and size limits.

## Phase 5 — Connect both clients

1. Build the Go executable for macOS and register it as a local stdio MCP server in Codex.
2. Test discovery, searching, reading, creation, updating, and reindexing from Codex.
3. Configure Secure MCP Tunnel on the Mac to launch the same executable, then create and install a personal ChatGPT MCP plugin using that tunnel.
4. Test the same workflows from ChatGPT. Confirm the Mac and tunnel must be running for cloud access.
5. Test simultaneous Codex and ChatGPT updates to the same note and verify that one receives a revision conflict rather than overwriting the other's work.

**Done when:** both clients access the same vault through the MCP tools and conflicting writes are safely rejected.

## Verification

- Test with a dedicated folder under `Knowledge/` before using existing notes.
- Cover title and content search, attachment filename search, new-note indexing, Obsidian edits, restart reconciliation, full reindex, and index corruption recovery.
- Test a note near the configured size boundary without making the search process load the entire file into memory.
- Check that logs rotate by date and that standard output contains only MCP protocol messages.
- Confirm that no tool can read or write outside `Knowledge/`.

## Dependencies and decisions

- Go MCP SDK for stdio protocol handling.
- SQLite driver with FTS5 support for the shared index.
- Filesystem watcher for prompt updates, backed by reconciliation scans.
- ChatGPT plugin and tunnel setup require access to the user's OpenAI workspace and a running tunnel client. The local Go server and Codex connection can be completed first.
