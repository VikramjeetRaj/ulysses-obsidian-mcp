# CLAUDE.md

Guidance for Claude Code when working in this repository. See also `AGENTS.md` (repo guidelines) and `mcp-server-plan.md` (the phased plan and the intended end state).

## What this is

A Go MCP server exposing search and read/write access to Markdown notes under `<vault>/Knowledge/`. Module: `github.com/VikramjeetRaj/ulysses-obsidian-mcp`, Go 1.27.

## Commands

```sh
go build ./... && go test ./...   # build and all tests
go test ./lib/vault/   # one package
go vet ./...
gofmt -l .
```

`main.go` loads config, starts the logger, vault, index and indexer, runs the watcher, periodic reconcile and first sync in the background, and serves MCP over stdio until the client disconnects.

## Layout

- `main.go`: entry point; `run()` wires everything and returns errors, `main` prints them and exits 1.
- `banner/`: startup banner.
- `lib/config/`: `config.Load(path)` reads and validates the YAML config.
- `lib/logger/`: `logger.New(path)` returns a `*slog.Logger` that appends to a file.
- `lib/index/`: SQLite FTS5 index (`index.New`, `PutNote`, `SearchNotes`, `SearchAttachments`, ...), `Chunk`/`ChunkAndHash`. Reads happen before the write transaction.
- `tools/indexer/`: `Sync`, `Watch` (fsnotify) and `Reconcile` keep the index in step with `Knowledge/`; `Reindex` rebuilds it as a new generation and swaps it in. A corrupt index file is set aside as `*.corrupt-<time>` and replaced (`index.New`).
- `tools/mcp/`: the MCP server and its tools (`search_notes`, `list_notes`, `read_note`, `create_note`, `update_note`, `delete_note`, `search_attachments`, `list_attachments`, `read_attachment`, `reindex_knowledge`).
- `lib/vault/`: the `Vault` type (`vault.New`, `ReadNote`, `CreateNote`, `UpdateNote`, `ListNotes`) and the `Note`/`Page`/`NotePage` types. `validatePath` confines paths to `<vault>/Knowledge/`, symlink-safe. `lib/vault/Readme.md` explains `resolvePath`.

## Conventions

- Packages expose concrete types and `New...`/`Load` functions that return `(value, error)`. Don't add an interface until a consumer needs one, and define it in the consumer.
- No panics for runtime or setup failures: return errors and let `main` decide. Wrap with `fmt.Errorf("context: %w", err)`.
- Handle each error once: return it with context, and let the top level log it. Don't log an error and also return it.
- Log events, not errors, from libraries: use the injected `*slog.Logger` with key/value attributes (`log.Info("note created", "path", p)`). Never log note content. No `fmt.Print*` outside `main`.
- I/O methods take `context.Context` first and check `ctx.Err()`.
- Sentinel errors (`ErrExists`, `ErrConflict`, `ErrNotFound`, `ErrInvalidPath`) are matched with `errors.Is`.
- Types live in the package that owns them (no shared `models` package).
- Tests sit beside the code (`foo_test.go`), are table-driven where useful, and use `t.TempDir()`.

## Constraints from the plan

- Standard output is reserved for MCP protocol messages once the server runs. Diagnostics go to the log file or stderr.
- Note and attachment access must stay inside `Knowledge/`. Reject absolute input paths, `..` traversal, non-Markdown write targets and escaping symlinks.
- Creates never overwrite. Updates are conditional on `expected_revision`.
- No shell tools. `delete_note` only moves a note to `Knowledge/.trash/` (revision-checked); nothing is erased. Attachments are read-only.
- The search index (SQLite FTS5) lives outside `Knowledge/`.

## Do not commit

Vault contents, credentials, tokens or machine-specific paths. `config.yaml` is git-ignored; commit only `config.example.yaml` with placeholder paths.
