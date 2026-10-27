#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
NAME=ulysses-obsidian-mcp
LAUNCHER="$ROOT/bin/start-mcp.sh"
APPLYING=0
trap 'if (( APPLYING )); then printf "Setup failed; local build/config or some registrations may already exist. See errors and backup paths above.\n" >&2; fi' ERR

prompt() {
    local label=$1 default=${2:-}
    printf '%s' "$label" >&2
    if [[ -n "$default" ]]; then printf ' [%s]' "$default" >&2; fi
    printf ': ' >&2
    if ! IFS= read -r REPLY; then
        printf '\nInput ended; setup cancelled.\n' >&2
        exit 1
    fi
    REPLY=${REPLY:-$default}
}

confirm() {
    prompt "$1 (y/N)"
    case "$REPLY" in
        y|Y|yes|YES) ;;
        *) printf 'Setup cancelled.\n'; exit 0 ;;
    esac
}

# Keep structured data out of shell evaluation; JSON strings are also valid YAML scalars.
data() {
    python3 - "$@" <<'PY'
import json
import os
import re
import shlex
import shutil
import sys
import tempfile
from pathlib import Path

action, *args = sys.argv[1:]
try:
    if action == "version":
        required = re.search(r"^go (\d+\.\d+(?:\.\d+)?)$", Path(args[0]).read_text(), re.M)[1]
        actual = re.search(r"go(\d+\.\d+(?:\.\d+)?)", args[1])
        def version(value):
            return tuple(int(x) for x in (value.split(".") + ["0", "0"])[:3])
        if actual is None or version(actual[1]) < version(required):
            raise ValueError("Go " + required + " or later is required")
    elif action == "path":
        path = Path(args[1]).expanduser().resolve()
        if args[0] == "directory":
            if not path.is_dir():
                raise ValueError("directory must already exist")
        elif args[0] == "vault":
            if path.exists() and not path.is_dir():
                raise ValueError("vault path must be a directory")
            parent = path
            while not parent.exists():
                parent = parent.parent
            if not parent.is_dir() or not os.access(parent, os.W_OK | os.X_OK):
                raise ValueError("vault directory or its existing parent must be writable and searchable")
        else:
            if path.is_dir():
                raise ValueError("expected a file path, not a directory")
            if path.exists() and not path.is_file():
                raise ValueError("expected a regular file")
            parent = path.parent
            while not parent.exists():
                parent = parent.parent
            if not parent.is_dir() or not os.access(parent, os.W_OK):
                raise ValueError("parent directory must be writable")
            if len(args) > 2:
                knowledge = (Path(args[2]) / "Knowledge").resolve()
                if path == knowledge or knowledge in path.parents:
                    raise ValueError("runtime files must be outside the vault's Knowledge directory")
            if len(args) > 3:
                root = Path(args[3])
                if path in ((root / name).resolve() for name in ("config.yaml", "bin/start-mcp.sh", "bin/ulysses-obsidian-mcp")):
                    raise ValueError("runtime files must not overwrite setup configuration or executables")
        print(path)
    elif action == "size":
        if not re.fullmatch(r"[0-9]+\s*(?:B|KB|MB|GB|TB)", args[0], re.I):
            raise ValueError("use a positive integer with B, KB, MB, GB or TB")
        number = int(re.match(r"[0-9]+", args[0])[0])
        if not 0 < number <= 18446744073709551615:
            raise ValueError("size must be a positive 64-bit integer")
    elif action == "backup":
        path = Path(args[0])
        if path.exists():
            fd, name = tempfile.mkstemp(prefix=path.name + ".bak.", dir=path.parent)
            os.close(fd)
            shutil.copyfile(path, name)
            print("Backup: " + name)
    elif action in ("json-check", "json-write", "claude-check"):
        path = Path(args[0])
        obj = json.loads(path.read_text()) if path.exists() else {}
        key = "servers" if args[1] == "vscode" else "mcpServers"
        if not isinstance(obj, dict) or not isinstance(obj.get(key, {}), dict):
            raise ValueError("configuration and " + key + " must be JSON objects")
        if action != "json-write":
            print("yes" if args[2] in obj.get(key, {}) else "no")
        else:
            entry = {"command": args[3], "args": []}
            if args[1] == "copilot":
                entry.update(type="local", tools=["*"])
            elif args[1] == "vscode":
                entry["type"] = "stdio"
            obj.setdefault(key, {})[args[2]] = entry
            path.parent.mkdir(parents=True, exist_ok=True)
            fd, name = tempfile.mkstemp(prefix=".mcp-", dir=path.parent)
            try:
                with os.fdopen(fd, "w") as out:
                    json.dump(obj, out, indent=2)
                    out.write("\n")
                os.replace(name, path)
            finally:
                if os.path.exists(name):
                    os.unlink(name)
    elif action == "config":
        path, vault, size, log, index = args
        with open(path, "w") as out:
            for key, value in zip(("vault", "search_size", "log_path", "index_path"), (vault, size, log, index)):
                out.write(key + ": " + json.dumps(value, ensure_ascii=False) + "\n")
    elif action == "launcher":
        path, root = args
        Path(path).write_text("#!/bin/sh\nset -eu\ncd -- " + shlex.quote(root) + "\nexec " + shlex.quote(root + "/bin/ulysses-obsidian-mcp") + "\n")
except (OSError, ValueError) as error:
    print("Error: " + str(error), file=sys.stderr)
    sys.exit(1)
PY
}

for tool in go python3; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        printf 'Required command missing: %s. Install it and rerun setup.\n' "$tool" >&2
        exit 1
    fi
done
data version "$ROOT/go.mod" "$(go version)"

printf '%s\n' 'Select clients (space-separated numbers):' \
    '  1) Codex' '  2) Claude Code' '  3) Cursor' \
    '  4) GitHub Copilot CLI' '  5) GitHub Copilot in VS Code' '  6) All'
while :; do
    prompt 'Clients'
    if [[ "$REPLY" == 6 ]]; then REPLY='1 2 3 4 5'; fi
    if [[ "$REPLY" =~ ^[1-5]([[:space:]]+[1-5])*$ ]]; then
        read -r -a selections <<< "$REPLY"
        CLIENTS=" ${selections[*]} "
        break
    fi
    printf 'Choose 1–5 separated by spaces, or 6 for all.\n' >&2
done

CODEX_CONFIG="${CODEX_HOME:-$HOME/.codex}/config.toml"
CLAUDE_CONFIG="$HOME/.claude.json"
# Claude uses .claude.json inside a custom configuration directory.
if [[ -n "${CLAUDE_CONFIG_DIR:-}" ]]; then CLAUDE_CONFIG="$CLAUDE_CONFIG_DIR/.claude.json"; fi
CURSOR_CONFIG="$HOME/.cursor/mcp.json"
COPILOT_CONFIG="${COPILOT_HOME:-$HOME/.copilot}/mcp-config.json"
for client in codex claude; do
    number=1
    if [[ "$client" == claude ]]; then number=2; fi
    if [[ "$CLIENTS" == *" $number "* ]] && ! command -v "$client" >/dev/null 2>&1; then
        printf 'Required client command missing: %s. Install it and rerun setup.\n' "$client" >&2
        exit 1
    fi
done

printf '\nNotes are accessed under <vault>/Knowledge/ (created by the server).\nSelected clients can read, create and update notes there.\n'
while :; do
    prompt 'Vault folder (created after confirmation if missing)' '~/Knowledge'
    if VAULT=$(data path vault "$REPLY"); then break; fi
done
while :; do
    prompt 'Search size' '1GB'
    if data size "$REPLY"; then SIZE=$REPLY; break; fi
done
while :; do
    prompt 'Log file' '~/.ulysses/ulysses_obsidian_mcp.log'
    if LOG=$(data path file "$REPLY" "$VAULT" "$ROOT"); then break; fi
done
while :; do
    prompt 'SQLite index file' '~/.ulysses/ulysses_index.sqlite'
    if INDEX=$(data path file "$REPLY" "$VAULT" "$ROOT"); then
        if [[ "$INDEX" != "$LOG" ]]; then break; fi
        printf 'Index and log must be different files.\n' >&2
    fi
done
if [[ "$CLIENTS" == *' 5 '* ]]; then
    while :; do
        prompt 'VS Code workspace folder' "$ROOT"
        if WORKSPACE=$(data path directory "$REPLY"); then break; fi
    done
    VSCODE_CONFIG="$WORKSPACE/.vscode/mcp.json"
fi

# Check JSON before building or changing anything. Never replace malformed JSON/JSONC.
JSON_PATHS=()
JSON_KINDS=()
JSON_COUNT=0
for number in 3 4 5; do
    if [[ "$CLIENTS" != *" $number "* ]]; then continue; fi
    case "$number" in
        3) path=$CURSOR_CONFIG; kind=cursor ;;
        4) path=$COPILOT_CONFIG; kind=copilot ;;
        5) path=$VSCODE_CONFIG; kind=vscode ;;
    esac
    exists=$(data json-check "$path" "$kind" "$NAME")
    if [[ "$exists" == yes ]]; then confirm "Replace '$NAME' in $path?"; fi
    JSON_PATHS+=("$path")
    JSON_KINDS+=("$kind")
    JSON_COUNT=$((JSON_COUNT + 1))
done

CLAUDE_EXISTS=no
if [[ "$CLIENTS" == *' 2 '* ]]; then
    CLAUDE_EXISTS=$(data claude-check "$CLAUDE_CONFIG" claude "$NAME")
    if [[ "$CLAUDE_EXISTS" == yes ]]; then confirm "Replace user-level Claude Code registration '$NAME'?"; fi
fi
if [[ "$CLIENTS" == *' 1 '* ]]; then
    confirm "Add or replace user-level Codex registration '$NAME'?"
fi
for path in "$ROOT/config.yaml" "$LAUNCHER" "$ROOT/bin/$NAME"; do
    if [[ -e "$path" ]]; then confirm "Back up and replace $path?"; fi
done

printf '\nVault: %s\nSearch size: %s\nLog: %s\nIndex: %s\nConfig: %s/config.yaml\nLauncher: %s\n' \
    "$VAULT" "$SIZE" "$LOG" "$INDEX" "$ROOT" "$LAUNCHER"
if [[ "$CLIENTS" == *' 1 '* ]]; then printf 'Codex: %s\n' "$CODEX_CONFIG"; fi
if [[ "$CLIENTS" == *' 2 '* ]]; then printf 'Claude Code: %s\n' "$CLAUDE_CONFIG"; fi
for ((i = 0; i < JSON_COUNT; i++)); do printf 'Client config: %s\n' "${JSON_PATHS[i]}"; done
confirm 'Build and apply this setup?'

APPLYING=1
mkdir -p "$ROOT/bin"
for path in "$ROOT/config.yaml" "$LAUNCHER" "$ROOT/bin/$NAME"; do data backup "$path"; done
printf 'Building server (Go may download dependencies)...\n'
cd -- "$ROOT"
go build -o "bin/$NAME" .
mkdir -p -- "$VAULT"
data config "$ROOT/config.yaml" "$VAULT" "$SIZE" "$LOG" "$INDEX"
data launcher "$LAUNCHER" "$ROOT"
chmod 700 "$LAUNCHER"

if [[ "$CLIENTS" == *' 1 '* ]]; then
    data backup "$CODEX_CONFIG"
    codex mcp add "$NAME" -- "$LAUNCHER"
fi
if [[ "$CLIENTS" == *' 2 '* ]]; then
    data backup "$CLAUDE_CONFIG"
    if [[ "$CLAUDE_EXISTS" == yes ]]; then claude mcp remove --scope user "$NAME"; fi
    claude mcp add --transport stdio --scope user "$NAME" -- "$LAUNCHER"
fi
for ((i = 0; i < JSON_COUNT; i++)); do
    data backup "${JSON_PATHS[i]}"
    data json-write "${JSON_PATHS[i]}" "${JSON_KINDS[i]}" "$NAME" "$LAUNCHER"
done
APPLYING=0
printf '\nSetup complete. Keep this checkout in place; rerun setup after moving it.\nRegistration is configured; a live client connection has not been checked.\n'
if [[ "$CLIENTS" == *' 1 '* ]]; then printf 'Codex: run codex mcp get %s, then open a new session.\n' "$NAME"; fi
if [[ "$CLIENTS" == *' 2 '* ]]; then printf 'Claude Code: run claude mcp get %s or /mcp.\n' "$NAME"; fi
if [[ "$CLIENTS" == *' 3 '* ]]; then printf 'Cursor: reload and check Settings > Tools & MCP.\n'; fi
if [[ "$CLIENTS" == *' 4 '* ]]; then printf 'Copilot CLI: restart and check /mcp.\n'; fi
if [[ "$CLIENTS" == *' 5 '* ]]; then printf 'VS Code: open the chosen workspace and run MCP: List Servers; review the trust prompt.\n'; fi
