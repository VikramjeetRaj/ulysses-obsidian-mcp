# ulysses-obsidian-mcp

[![M8ven Score](https://m8ven.ai/badge/mcp/vikramjeetraj-ulysses-obsidian-mcp-d9dy72?v=37b04b4dc1654efb7681756d475b1e51)](https://m8ven.ai/mcp/vikramjeetraj-ulysses-obsidian-mcp-d9dy72?s=readme)

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

### Interactive setup

From this checkout, run:

```sh
./setup.sh
```

Requires Bash, Go (the version in `go.mod` or later), and Python 3. Setup installs
no prerequisites. Codex and Claude Code also require their respective CLI on
`PATH`; Cursor and Copilot can be configured before installing the client.

Select one or more clients: Codex, Claude Code, Cursor, GitHub Copilot CLI, or
GitHub Copilot in VS Code. Press Enter to accept each default, or enter a replacement:

| Input | Default |
| --- | --- |
| Vault folder | `~/Knowledge` |
| Search size | `1GB` |
| Log file | `~/.ulysses/ulysses_obsidian_mcp.log` |
| SQLite index | `~/.ulysses/ulysses_index.sqlite` |

Paths are expanded to absolute paths. After final confirmation, setup creates the
vault folder and missing parents if needed. Files and unwritable parent paths are
rejected. Notes live under `<vault>/Knowledge/`, so the default notes folder is
`~/Knowledge/Knowledge/`. Runtime files must stay outside
`<vault>/Knowledge/`; the selected clients can read, create and update notes there.
No API keys are needed for this local stdio server.

The script previews settings and destinations before applying changes. It builds
`bin/ulysses-obsidian-mcp`, writes ignored `config.yaml`, and generates
`bin/start-mcp.sh` to launch with the correct working directory. Log and index
defaults are under `~/.ulysses/` and are created when the server starts.
Keep the checkout in place; rerun setup after moving it or to rebuild.

| Client | Registration destination |
| --- | --- |
| Codex | User configuration via `codex mcp add` (`CODEX_HOME` respected) |
| Claude Code | User scope via `claude mcp add` (`CLAUDE_CONFIG_DIR` respected) |
| Cursor | `~/.cursor/mcp.json` |
| Copilot CLI | `~/.copilot/mcp-config.json` (`COPILOT_HOME` respected) |
| Copilot in VS Code | `.vscode/mcp.json` in the workspace you select |

Unrelated JSON entries are preserved. Existing files receive private `.bak.*`
backups; replacement prompts default to cancellation. Client JSON must be strict
JSON: comments, trailing commas, malformed files, or invalid object shapes cause
setup to stop before changes. If using another workspace, keep its generated
`.vscode/mcp.json` out of version control because it contains an absolute path.
Failures can leave a local build/config or earlier registrations in place; use
the printed backups to restore files if necessary.

Restart the selected clients after setup. Check `codex mcp get ulysses-obsidian-mcp`,
Claude Code's `/mcp`, Cursor's **Tools & MCP**, Copilot CLI's `/mcp`, or VS Code's
**MCP: List Servers**. Review client trust prompts. Registration alone does not
verify a live MCP connection.

### Manual configuration

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
bash -n setup.sh tests/setup_test.sh  # shell syntax
bash tests/setup_test.sh             # isolated setup tests (no real client changes)
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
