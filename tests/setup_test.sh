#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
python3 - "$ROOT" <<'PY'
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SOURCE = Path(sys.argv.pop())
NAME = "ulysses-obsidian-mcp"


class SetupTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="ulysses-setup-")
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name).resolve()
        self.root = self.base / "checkout with 'quotes' and $(touch INJECTED)"
        self.root.mkdir()
        for name in ("setup.sh", "go.mod"):
            shutil.copyfile(SOURCE / name, self.root / name)
        self.home = self.base / "home"
        self.home.mkdir()
        self.vault = self.base / "vault with spaces"
        self.vault.mkdir()
        self.workspace = self.base / "workspace"
        self.workspace.mkdir()
        self.commands = self.base / "commands"
        self.commands.mkdir()
        self.calls = self.base / "calls.jsonl"
        self.env = dict(os.environ)
        for key in ("CODEX_HOME", "CLAUDE_CONFIG_DIR", "COPILOT_HOME"):
            self.env.pop(key, None)
        self.env.update(HOME=str(self.home), PATH=str(self.commands) + os.pathsep + os.environ["PATH"],
                        STUB_CALLS=str(self.calls))
        stub = "#!" + sys.executable + "\n" + '''
import json, os, sys
from pathlib import Path
tool = Path(sys.argv[0]).name
args = sys.argv[1:]
if tool == "go" and args == ["version"]:
    print("go version " + os.environ.get("STUB_GO_VERSION", "go1.27.3") + " test/arch")
    sys.exit(0)
with open(os.environ["STUB_CALLS"], "a") as out:
    out.write(json.dumps([tool] + args) + "\\n")
if os.environ.get("STUB_FAIL") == tool:
    print("simulated " + tool + " failure", file=sys.stderr)
    sys.exit(1)
if tool == "go":
    assert args == ["build", "-o", "bin/ulysses-obsidian-mcp", "."], args
    target = Path(args[2])
    target.write_text('#!/bin/sh\\npwd -P\\n')
    target.chmod(0o700)
'''
        for tool in ("go", "codex", "claude"):
            path = self.commands / tool
            path.write_text(stub)
            path.chmod(0o700)

    def run_setup(self, answers):
        result = subprocess.run(["bash", str(self.root / "setup.sh")],
                                input="\n".join(answers) + "\n", text=True,
                                capture_output=True, env=self.env, cwd=self.base, timeout=20)
        self.output = result.stdout + result.stderr
        return result

    def answers(self, client, tail=None):
        answers = [client, str(self.vault), "", "", ""]
        if client == "6" or "5" in client.split():
            answers.append(str(self.workspace))
        return answers + (tail if tail is not None else (["y", "y"] if client in ("1", "6") else ["y"]))

    def assert_success(self, result):
        self.assertEqual(result.returncode, 0, self.output)
        self.assertIn("Setup complete.", self.output)
        self.assertFalse((self.base / "INJECTED").exists())
        self.assertFalse((self.root / "INJECTED").exists())

    def assert_no_changes(self):
        self.assertFalse((self.root / "config.yaml").exists())
        self.assertFalse((self.root / "bin").exists())
        self.assertFalse(self.calls.exists())

    def test_each_client_and_all(self):
        # Each run gets a new fixture so there are no replacement prompts.
        for client in ("1", "2", "3", "4", "5", "6", "3\t4"):
            with self.subTest(client=client):
                fixture = SetupTest()
                fixture.setUp()
                try:
                    fixture.assert_success(fixture.run_setup(fixture.answers(client)))
                    calls = [json.loads(line) for line in fixture.calls.read_text().splitlines()]
                    launcher = str(fixture.root / "bin/start-mcp.sh")
                    if client in ("1", "6"):
                        self.assertIn(["codex", "mcp", "add", NAME, "--", launcher], calls)
                    if client in ("2", "6"):
                        self.assertIn(["claude", "mcp", "add", "--transport", "stdio", "--scope", "user", NAME, "--", launcher], calls)
                    for number, path, kind in (
                        ("3", fixture.home / ".cursor/mcp.json", "cursor"),
                        ("4", fixture.home / ".copilot/mcp-config.json", "copilot"),
                        ("5", fixture.workspace / ".vscode/mcp.json", "vscode"),
                    ):
                        if client != "6" and number not in client.split():
                            self.assertFalse(path.exists())
                            continue
                        obj = json.loads(path.read_text())
                        entry = obj["servers" if kind == "vscode" else "mcpServers"][NAME]
                        self.assertEqual(entry["command"], launcher)
                        self.assertEqual(entry["args"], [])
                        if kind == "vscode":
                            self.assertEqual(entry["type"], "stdio")
                        if kind == "copilot":
                            self.assertEqual(entry["type"], "local")
                            self.assertEqual(entry["tools"], ["*"])
                finally:
                    fixture.doCleanups()

    def test_launcher_and_yaml_handle_special_paths(self):
        self.assert_success(self.run_setup(self.answers("3")))
        config = dict(line.split(": ", 1) for line in (self.root / "config.yaml").read_text().splitlines())
        self.assertEqual(json.loads(config["vault"]), str(self.vault))
        result = subprocess.run([str(self.root / "bin/start-mcp.sh")], cwd=self.base,
                                text=True, capture_output=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), str(self.root))

    def test_enter_accepts_defaults_and_creates_default_vault(self):
        self.assertFalse((self.home / "Knowledge").exists())
        self.assert_success(self.run_setup(["3", "", "", "", "", "y"]))
        config = dict(line.split(": ", 1) for line in (self.root / "config.yaml").read_text().splitlines())
        self.assertEqual(json.loads(config["vault"]), str(self.home / "Knowledge"))
        self.assertEqual(json.loads(config["search_size"]), "1GB")
        self.assertEqual(json.loads(config["log_path"]), str(self.home / ".ulysses/ulysses_obsidian_mcp.log"))
        self.assertEqual(json.loads(config["index_path"]), str(self.home / ".ulysses/ulysses_index.sqlite"))
        self.assertTrue((self.home / "Knowledge").is_dir())
        self.assertFalse((self.home / ".ulysses").exists())

    def test_missing_nested_vault_is_created(self):
        answers = self.answers("3")
        answers[1] = "~/notes/new/vault"
        self.assert_success(self.run_setup(answers))
        self.assertTrue((self.home / "notes/new/vault").is_dir())

    def test_cancellation_does_not_create_vault(self):
        for vault in ("", "~/notes/new/vault"):
            result = self.run_setup(["3", vault, "", "", "", "n"])
            self.assertEqual(result.returncode, 0, self.output)
            self.assert_no_changes()
            self.assertFalse((self.home / "Knowledge").exists())
            self.assertFalse((self.home / "notes").exists())
            self.assertFalse((self.home / ".ulysses").exists())

    def test_file_and_unwritable_parent_cannot_be_vaults(self):
        file = self.base / "file"
        file.write_text("keep")
        locked = self.base / "locked"
        locked.mkdir(mode=0o500)
        self.addCleanup(locked.chmod, 0o700)
        answers = ["3", str(file), str(file / "child"), str(locked / "new"),
                   str(self.vault), "", "", "", "y"]
        self.assert_success(self.run_setup(answers))
        self.assertIn("vault path must be a directory", self.output)
        self.assertIn("must be writable and searchable", self.output)
        self.assertEqual(file.read_text(), "keep")
        self.assertFalse((locked / "new").exists())

    def test_merge_preserves_other_entries_and_creates_private_backup(self):
        path = self.home / ".cursor/mcp.json"
        path.parent.mkdir()
        original = '{"otherSetting": true, "mcpServers": {"other": {"command": "keep"}, "' + NAME + '": {"command": "old"}}}\n'
        path.write_text(original)
        self.assert_success(self.run_setup(self.answers("3", ["y", "y"])))
        obj = json.loads(path.read_text())
        self.assertTrue(obj["otherSetting"])
        self.assertEqual(obj["mcpServers"]["other"], {"command": "keep"})
        backups = list(path.parent.glob("mcp.json.bak.*"))
        self.assertEqual(len(backups), 1)
        self.assertEqual(backups[0].read_text(), original)
        self.assertEqual(backups[0].stat().st_mode & 0o777, 0o600)

    def test_existing_native_configs_are_backed_up_and_claude_replaced(self):
        codex = self.home / ".codex/config.toml"
        codex.parent.mkdir()
        codex.write_text('model = "keep"\n')
        claude = self.home / ".claude.json"
        claude.write_text(json.dumps({"mcpServers": {NAME: {"command": "old"}}}))
        self.assert_success(self.run_setup(self.answers("1 2", ["y", "y", "y"])))
        self.assertEqual(next(codex.parent.glob("config.toml.bak.*")).read_text(), codex.read_text())
        self.assertEqual(next(self.home.glob(".claude.json.bak.*")).read_text(), claude.read_text())
        calls = [json.loads(line) for line in self.calls.read_text().splitlines()]
        remove = ["claude", "mcp", "remove", "--scope", "user", NAME]
        self.assertIn(remove, calls)
        self.assertLess(calls.index(remove), len(calls) - 1)

    def test_cancel_before_apply_and_eof(self):
        result = self.run_setup(self.answers("3", ["n"]))
        self.assertEqual(result.returncode, 0, self.output)
        self.assertIn("cancelled", self.output)
        self.assert_no_changes()
        result = self.run_setup(["3"])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Input ended", self.output)
        self.assert_no_changes()

    def test_declined_replacement_does_not_build_or_modify(self):
        path = self.root / "config.yaml"
        path.write_text("original\n")
        result = self.run_setup(self.answers("3", [""]))
        self.assertEqual(result.returncode, 0, self.output)
        self.assertEqual(path.read_text(), "original\n")
        self.assertFalse((self.root / "bin").exists())
        self.assertFalse(self.calls.exists())
        self.assertFalse(list(self.root.glob("*.bak.*")))

    def test_rerun_backs_up_local_files(self):
        self.assert_success(self.run_setup(self.answers("3")))
        old = (self.root / "config.yaml").read_text()
        self.assert_success(self.run_setup(self.answers("3", ["y"] * 5)))
        self.assertEqual(next(self.root.glob("config.yaml.bak.*")).read_text(), old)
        self.assertEqual(len(list((self.root / "bin").glob("*.bak.*"))), 2)

    def test_invalid_json_and_wrong_shape_stop_before_changes(self):
        path = self.home / ".cursor/mcp.json"
        path.parent.mkdir()
        for original in ('{"mcpServers": { /* comment */ }}', '{"mcpServers": []}', '[]'):
            path.write_text(original)
            result = self.run_setup(self.answers("3"))
            self.assertNotEqual(result.returncode, 0, self.output)
            self.assertEqual(path.read_text(), original)
            self.assert_no_changes()

    def test_invalid_inputs_retry_and_symlinks_cannot_put_runtime_in_knowledge(self):
        knowledge = self.vault / "Knowledge"
        knowledge.mkdir()
        alias = self.base / "alias"
        alias.symlink_to(knowledge, target_is_directory=True)
        answers = ["bad", "3", str(self.root / "go.mod"), str(self.vault),
                   "0GB", "1.5GB", "18446744073709551616B", "2gb",
                   str(alias / "server.log"), "", str(alias / "index.sqlite"),
                   str(self.home / ".ulysses/ulysses_obsidian_mcp.log"), str(self.root / "config.yaml"), "", "y"]
        self.assert_success(self.run_setup(answers))
        self.assertIn("outside the vault's Knowledge", self.output)
        self.assertIn("Index and log must be different", self.output)
        self.assertIn("must not overwrite setup", self.output)
        self.assertFalse((knowledge / "index.sqlite").exists())
        self.assertIn('search_size: "2gb"', (self.root / "config.yaml").read_text())

    def test_build_and_registration_failures_report_partial_setup(self):
        self.env["STUB_FAIL"] = "go"
        result = self.run_setup(self.answers("1"))
        self.assertNotEqual(result.returncode, 0, self.output)
        self.assertFalse((self.root / "config.yaml").exists())
        self.assertIn("may already exist", self.output)
        self.env["STUB_FAIL"] = "codex"
        result = self.run_setup(self.answers("1"))
        self.assertNotEqual(result.returncode, 0, self.output)
        self.assertTrue((self.root / "config.yaml").exists())
        self.assertIn("simulated codex failure", self.output)
        self.assertNotIn("Setup complete.", self.output)

    def test_old_go_stops_before_prompts(self):
        self.env["STUB_GO_VERSION"] = "go1.26.0"
        result = self.run_setup([])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Go 1.27.0 or later", self.output)
        self.assert_no_changes()

    def test_custom_client_config_directories(self):
        for variable, directory in (("CODEX_HOME", "codex-custom"),
                                    ("CLAUDE_CONFIG_DIR", "claude-custom"),
                                    ("COPILOT_HOME", "copilot-custom")):
            path = self.base / directory
            path.mkdir()
            self.env[variable] = str(path)
        self.assert_success(self.run_setup(self.answers("6")))
        self.assertTrue((self.base / "copilot-custom/mcp-config.json").exists())
        self.assertIn(str(self.base / "codex-custom/config.toml"), self.output)
        self.assertIn(str(self.base / "claude-custom/.claude.json"), self.output)
        self.assertFalse((self.home / ".copilot").exists())


unittest.main(verbosity=2)
PY
