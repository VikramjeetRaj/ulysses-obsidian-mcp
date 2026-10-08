# ulysses-obsidian-mcp

A Go [MCP](https://modelcontextprotocol.io) server that lets local AI clients (Codex over stdio, ChatGPT through a personal MCP plugin and Secure MCP Tunnel) search and maintain the Markdown notes in a Ulysses/Obsidian vault.

The vault's Markdown files are the source of truth. The search index is rebuildable and is never treated as a note.

## Status

Early scaffold. The full roadmap is in [`mcp-server-plan.md`](mcp-server-plan.md).

| Area | State |
| --- | --- |
| Config loading and validation (`lib/config`) | Implemented, with tests |
| File logger (`lib/logger`) | Implemented, with tests |
| Vault (`lib/vault`) | Read, create, update (conditional on revision) and list notes, with path confinement and tests |
| Search index, MCP tools, client connections | Not implemented |

`main.go` currently loads the config, starts the logger and opens the vault; the index and MCP server are not wired in yet.

## Requirements

- Go 1.27 or later (see `go.mod`)

## Configuration

The server reads `config.yaml` from the working directory (copy `config.example.yaml` to start). All keys are required.

```yaml
vault: /absolute/path/to/vault            # existing directory
search_size: 1GB                          # positive size with a B, KB, MB, GB or TB suffix
log_path: /absolute/path/to/obsidian-mcp.log
```

- `vault` and `log_path` must be absolute paths.
- Notes live under `<vault>/Knowledge/`, which is created on start if missing. All note access is confined to that directory, including through symlinks (see [`lib/vault/Readme.md`](lib/vault/Readme.md)).

Startup exits with a descriptive error if the configuration is invalid.

## Development

```sh
go build ./...          # build
go test ./...           # run all tests
go test ./lib/vault/     # run a single package
go vet ./...
```

## Layout

```
main.go          entry point (wiring is in progress)
banner/          startup banner
lib/config/      config.yaml loading and validation
lib/logger/      slog logger that appends to a file
lib/vault/       Vault: note read/create/update/list, Knowledge/ path validation
mcp-server-plan.md   phased implementation plan
```

## Planned MCP tools

`search_notes`, `search_attachments`, `list_notes`, `read_note`, `create_note`, `update_note` (conditional on `expected_revision`) and `reindex_knowledge`. There will be no delete or shell tool. See the plan for details.
