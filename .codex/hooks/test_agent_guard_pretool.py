#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Codex Hook Bridge MVP 回归测试。"""

from __future__ import annotations

import importlib.util
import io
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from types import ModuleType
from typing import Any, Callable


def load_hook_module() -> ModuleType:
    hook_path = Path(__file__).with_name("agent-guard-pretool.py")
    if str(hook_path.parent) not in sys.path:
        sys.path.insert(0, str(hook_path.parent))
    spec = importlib.util.spec_from_file_location("codex_agent_guard_pretool", hook_path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load hook module from {hook_path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


HOOK = load_hook_module()


class CodexHookBridgeTest(unittest.TestCase):
    def write_project_protection(self, repo: Path, body: dict[str, Any] | str) -> None:
        config_path = repo / ".agenttoolgate" / "protected.json"
        config_path.parent.mkdir(parents=True, exist_ok=True)
        if isinstance(body, str):
            config_path.write_text(body, encoding="utf-8")
        else:
            config_path.write_text(json.dumps(body, ensure_ascii=False), encoding="utf-8")

    def set_hook_control(self, repo: Path, mode: str | None = None, raw: str | None = None) -> None:
        control_path = repo / ".tmp" / "agenttoolgate" / "hook-control.json"
        original = control_path.read_bytes() if control_path.exists() else None

        def restore() -> None:
            if original is None:
                try:
                    control_path.unlink()
                except FileNotFoundError:
                    pass
                return
            control_path.parent.mkdir(parents=True, exist_ok=True)
            control_path.write_bytes(original)

        self.addCleanup(restore)
        control_path.parent.mkdir(parents=True, exist_ok=True)
        if raw is not None:
            control_path.write_text(raw, encoding="utf-8")
            return
        control_path.write_text(json.dumps({"mode": mode}, ensure_ascii=False), encoding="utf-8")

    def test_missing_control_file_defaults_to_noop(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            raw = self.invoke_raw(
                json.dumps({"tool_name": "shell", "args": {"command": "git status"}, "cwd": str(repo)}, ensure_ascii=False),
                go_cli=lambda _payload: self.fail("缺失控制文件时不应调用 Go CLI"),
                post_json=lambda *_args, **_kwargs: self.fail("缺失控制文件时不应调用 fallback HTTP"),
                enable_live_control=False,
            )
            self.assertEqual(raw, "")

    def test_agenttoolgate_mcp_tools_do_not_enter_local_guard_twice(self) -> None:
        self.assertFalse(HOOK.is_guarded_tool("mcp__agenttoolgate__mock_echo"))
        self.assertFalse(HOOK.is_guarded_tool("MCP__AGENTTOOLGATE__GITHUB_CREATE_ISSUE"))
        self.assertTrue(HOOK.is_guarded_tool("mcp__external_server__write_file"))
        self.assertTrue(HOOK.is_guarded_tool("Read"))
        self.assertTrue(HOOK.is_guarded_tool("Grep"))
        self.assertTrue(HOOK.is_guarded_tool("Glob"))

    def test_search_tools_map_their_read_scope(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            cases = [
                ("Grep", {"pattern": "TODO", "path": "src"}, "src", False),
                ("Grep", {"pattern": "TODO"}, ".", False),
                ("Grep", {"pattern": ".", "path": "src/client.pem"}, "src/client.pem", True),
                ("Glob", {"pattern": "**/*.py", "path": "src"}, "src/**/*.py", False),
                ("Glob", {"pattern": ".github/workflows/**"}, ".github/workflows/**", True),
                ("Glob", {"pattern": "**/*.pem", "path": "src"}, "src/**/*.pem", True),
            ]
            for tool_name, tool_input, expected_target, high_risk in cases:
                with self.subTest(tool=tool_name, tool_input=tool_input):
                    payload = HOOK.build_agent_guard_request(
                        "codex",
                        {"cwd": str(repo)},
                        tool_name,
                        tool_input,
                        str(repo),
                    )
                    self.assertEqual(payload["actionType"], "read")
                    self.assertEqual(payload["target"].replace("\\", "/"), expected_target)
                    self.assertEqual(HOOK.is_high_risk_offline_target(payload), high_risk)
                    self.assertEqual(
                        HOOK.is_explicitly_low_risk_offline_action(str(repo), payload),
                        not high_risk or HOOK.is_project_metadata_read_target(payload["target"]),
                    )

    def test_canonical_apply_patch_command_extracts_every_target(self) -> None:
        patch = """*** Begin Patch
*** Update File: src/ui.go
*** Update File: src/core/algorithm.go
*** End Patch
"""
        payload = HOOK.build_agent_guard_request(
            "codex",
            {"cwd": os.getcwd()},
            "apply_patch",
            {"command": patch},
        )
        self.assertEqual(payload["actionType"], "write")
        self.assertEqual(payload["targets"], ["src/ui.go", "src/core/algorithm.go"])
        self.assertEqual(payload["content"], patch.strip())

    def test_project_protection_tightens_python_fallback(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            self.write_project_protection(
                repo,
                {
                    "version": 1,
                    "localActionFirewall": {
                        "enabled": True,
                        "protectedPaths": [
                            {"pattern": "src/core/**", "read": "require_approval", "write": "deny"}
                        ],
                        "egress": {"enabled": False},
                    },
                },
            )
            read_payload = {
                "tool": "Read",
                "actionType": "read",
                "target": "src/core/algorithm.go",
                "workingDirectory": str(repo),
            }
            floor = HOOK.attach_python_guard_floor(str(repo), read_payload)
            self.assertEqual(floor["guardDecision"], "ask")
            self.assertEqual(floor["guardRiskLevel"], "high")
            self.assertFalse(HOOK.is_explicitly_low_risk_offline_action(str(repo), read_payload))

            write_payload = dict(read_payload, tool="Write", actionType="write")
            floor = HOOK.attach_python_guard_floor(str(repo), write_payload)
            self.assertEqual(floor["guardDecision"], "deny")

    def test_project_protection_parses_real_shell_payload_without_target(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            self.write_project_protection(
                repo,
                {
                    "version": 1,
                    "localActionFirewall": {
                        "enabled": True,
                        "protectedPaths": [
                            {"pattern": "src/protected/**", "read": "deny"},
                            {"pattern": "src/core/first.txt", "write": "require_approval"},
                            {"pattern": "deploy/production/app.yaml", "delete": "deny"},
                        ],
                        "egress": {"enabled": False},
                    },
                },
            )
            cases = (
                ("Bash", "rg -n TODO src/protected", "deny"),
                (
                    "PowerShell",
                    "Set-Content src/core/first.txt one; Remove-Item deploy/production/app.yaml",
                    "deny",
                ),
            )
            for tool_name, command, expected in cases:
                with self.subTest(command=command):
                    tool_input = {"command": command}
                    payload = HOOK.build_agent_guard_request(
                        "codex",
                        {"cwd": str(repo)},
                        tool_name,
                        tool_input,
                        str(repo),
                    )
                    self.assertNotIn("target", tool_input)
                    floor = HOOK.project_protection_floor(str(repo), payload)
                    self.assertIsNotNone(floor)
                    self.assertEqual(floor["decision"], expected)

            unknown_payload = HOOK.build_agent_guard_request(
                "codex",
                {"cwd": str(repo)},
                "Bash",
                {"command": "rg --mystery TODO src/protected"},
                str(repo),
            )
            self.assertIsNone(HOOK.project_protection_floor(str(repo), unknown_payload))

    def test_invalid_project_protection_denies_python_fallback(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            self.write_project_protection(repo, '{"version":1,"localActionFirewall":{"enabled":true,"unknown":true}}')
            floor = HOOK.attach_python_guard_floor(
                str(repo),
                {
                    "tool": "Read",
                    "actionType": "read",
                    "target": "README.md",
                    "workingDirectory": str(repo),
                },
            )
            self.assertEqual(floor["guardDecision"], "deny")
            self.assertEqual(floor["guardRiskLevel"], "high")

    def test_powershell_positional_write_targets_are_extracted(self) -> None:
        cases = [
            ("Set-Content '.ssh/id_rsa' 'synthetic'", ".ssh/id_rsa"),
            ("New-Item '.git/hooks/pre-commit' -ItemType File", ".git/hooks/pre-commit"),
            ("Copy-Item 'safe.txt' '.ssh/authorized_keys'", ".ssh/authorized_keys"),
        ]
        for command, expected in cases:
            with self.subTest(command=command):
                payload = HOOK.build_agent_guard_request(
                    "codex",
                    {"cwd": os.getcwd()},
                    "PowerShell",
                    {"command": command},
                )
                self.assertEqual(payload["target"], expected)
                self.assertTrue(HOOK.is_high_risk_offline_target(payload))

    def test_offline_exec_fallback_only_allows_exact_commands(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()

            def eligible(command: str) -> bool:
                return HOOK.is_explicitly_low_risk_offline_action(
                    str(repo),
                    {
                        "actionType": "exec",
                        "target": command,
                        "content": command,
                        "workingDirectory": str(repo),
                    },
                )

            for command in (
                "git status",
                "pwd",
                "Get-Location",
            ):
                with self.subTest(command=command):
                    self.assertTrue(eligible(command))

            for command in (
                'rg "foo|bar" .',
                'rg "powershell|curl" docs',
                "rg -n TODO src",
                "rg --hidden TODO .",
                "grep -r TODO .",
                "grep -n TODO README.md",
                "Select-String -LiteralP ..\\outside.txt TODO",
                "git diff --stat",
                "git diff -O..\\outside",
                "Get-Content README.md -Wai",
                r'rg foo . x\"; touch owned #"',
                "sed -n '1,40p' README.md",
                "ls",
                'rg --pre "powershell -Command Get-Content" .',
                "rg secret (Get-Location)",
                r"rg secret C:\Users\demo",
                r"rg secret ..\outside",
                "rg secret $env:USERPROFILE",
                "git diff --output=.ssh/id_rsa",
                "git log --ext-diff",
                r"git diff --no-index ..\a ..\b",
                "Select-String -Pattern TODO -Path Env:",
                r"Select-String -Pattern TODO -Path ..\outside.txt",
                r"Select-String -Pattern TODO -Path README.md,..\outside.txt",
                "sed -i 's/a/b/' README.md",
                "sed -n '1w .ssh/id_rsa' README.md",
                "sed -n '1e touch owned' README.md",
                "git status\nSet-Content report.md changed",
                "git status\r\nSet-Content report.md changed",
                r"git status \; Set-Content report.md changed",
                r"sed -n 1\,40p README.md",
                "Get-ChildItem Env:",
                "Get-ChildItem -Path:Env:",
                "Get-ChildItem -Path:([System.IO.Directory]::GetCurrentDirectory())",
                r"Get-ChildItem C:\Users",
                r"Get-ChildItem ..\outside",
                "Get-Content Env:API_TOKEN",
                r"Get-Content C:\Users\demo\notes.txt",
                r"Get-Content ..\outside.txt",
                r"Get-Content README.md,..\outside.txt",
                "Get-Content $env:USERPROFILE",
                "Get-Content (Invoke-WebRequest https://example.test)",
                "Get-Content -Wait README.md",
                "cat /etc/passwd",
                "cat ~/notes.txt",
                r"sed -n '1,20p' ..\outside.txt",
            ):
                with self.subTest(command=command):
                    self.assertFalse(eligible(command))

    def test_request_includes_workspace_root_and_ticket_digest_binds_it(self) -> None:
        first = HOOK.build_agent_guard_request(
            "codex",
            {"cwd": "E:/repo-a"},
            "Read",
            {"file_path": "README.md"},
            "E:/repo-a",
        )
        second = dict(first)
        second["workspaceRoot"] = "E:/repo-b"
        third = dict(first)
        third["workingDirectory"] = "E:/repo-a/nested"
        self.assertEqual(first["workspaceRoot"], "E:/repo-a")
        self.assertNotEqual(HOOK.hook_request_digest(first), HOOK.hook_request_digest(second))
        self.assertNotEqual(HOOK.hook_request_digest(first), HOOK.hook_request_digest(third))

    def test_initialized_non_git_project_root_is_detected(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            nested = repo / "src" / "feature"
            (repo / ".agenttoolgate").mkdir()
            nested.mkdir(parents=True)
            self.assertEqual(HOOK.find_repo_root(str(nested)), str(repo))

    def test_live_ordinary_repo_read_delegates_to_go_cli(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            (repo / "README.md").write_text("hello", encoding="utf-8")
            captured: list[dict[str, Any]] = []

            def go_cli(payload: dict[str, Any]) -> dict[str, Any]:
                captured.append(payload)
                return {}

            raw = self.invoke_raw(
                json.dumps(
                    {"tool_name": "Read", "tool_input": {"file_path": "README.md"}, "cwd": str(repo)},
                    ensure_ascii=False,
                ),
                go_cli=go_cli,
                post_json=lambda *_args, **_kwargs: self.fail("普通仓库读取不应调用后端"),
            )
            self.assertEqual(raw, "")
            self.assertEqual(len(captured), 1)
            self.assertEqual(captured[0]["tool_input"]["file_path"], "README.md")

    def test_live_sensitive_read_delegates_to_go_cli(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            captured: list[dict[str, Any]] = []
            expected = {
                "hookSpecificOutput": {
                    "hookEventName": "PreToolUse",
                    "permissionDecision": "deny",
                    "permissionDecisionReason": "AgentToolGate 已阻止：命中敏感读取",
                }
            }

            def go_cli(payload: dict[str, Any]) -> dict[str, Any]:
                captured.append(payload)
                return expected

            output = self.invoke_hook(
                {"tool_name": "Read", "tool_input": {"file_path": "src/client.pem"}, "cwd": str(repo)},
                go_cli=go_cli,
                post_json=lambda *_args, **_kwargs: self.fail("Go CLI 成功时不应调用后端"),
            )
            self.assertEqual(output, expected)
            self.assertEqual(len(captured), 1)
            self.assertEqual(captured[0]["tool_input"]["file_path"], "src/client.pem")

    def test_dry_run_ordinary_repo_read_remains_low_noise(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            (repo / "README.md").write_text("hello", encoding="utf-8")
            self.set_hook_control(repo, "dry-run")
            raw = self.invoke_raw(
                json.dumps(
                    {"tool_name": "Read", "tool_input": {"file_path": "README.md"}, "cwd": str(repo)},
                    ensure_ascii=False,
                ),
                go_cli=lambda _payload: self.fail("dry-run 普通读取不应调用 Go CLI"),
                post_json=lambda *_args, **_kwargs: self.fail("dry-run 普通读取不应调用后端"),
                enable_live_control=False,
            )
            self.assertEqual(raw, "")
            self.assertFalse((repo / ".tmp" / "agenttoolgate" / "hook-dry-run.jsonl").exists())

    def test_dry_run_repo_read_fast_path_requires_explicit_local_read_tool(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            local_read = HOOK.build_agent_guard_request(
                "codex",
                {"cwd": str(repo)},
                "Read",
                {"file_path": "README.md"},
                str(repo),
            )
            self.assertTrue(HOOK.is_fast_path_repo_read(str(repo), local_read))
            self.assertTrue(HOOK.is_explicitly_low_risk_offline_action(str(repo), local_read))

            for tool_name, tool_input in (
                ("WebSearch", {"query": "AgentToolGate"}),
                ("mcp__github__merge_pull_request", {"repository_full_name": "example/repo", "pr_number": 1}),
            ):
                with self.subTest(tool=tool_name):
                    payload = HOOK.build_agent_guard_request(
                        "codex",
                        {"cwd": str(repo)},
                        tool_name,
                        tool_input,
                        str(repo),
                    )
                    self.assertFalse(HOOK.is_fast_path_repo_read(str(repo), payload))
                    self.assertFalse(HOOK.is_explicitly_low_risk_offline_action(str(repo), payload))

    def test_explicit_off_control_noops(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            self.set_hook_control(repo, "off")
            raw = self.invoke_raw(
                json.dumps({"tool_name": "Write", "tool_input": {"path": ".ssh/authorized_keys", "content": "ssh-rsa AAAA"}, "cwd": str(repo)}, ensure_ascii=False),
                go_cli=lambda _payload: self.fail("off 模式不应调用 Go CLI"),
                post_json=lambda *_args, **_kwargs: self.fail("off 模式不应调用 fallback HTTP"),
                enable_live_control=False,
            )
            self.assertEqual(raw, "")
            self.assertFalse((repo / ".tmp" / "local-action-firewall" / "pending-audit.jsonl").exists())

    def test_invalid_control_file_defaults_to_noop(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            self.set_hook_control(repo, raw="{bad json")
            raw = self.invoke_raw(
                json.dumps({"tool_name": "Write", "tool_input": {"path": ".env", "content": "SECRET=x"}, "cwd": str(repo)}, ensure_ascii=False),
                go_cli=lambda _payload: self.fail("损坏控制文件时不应调用 Go CLI"),
                post_json=lambda *_args, **_kwargs: self.fail("损坏控制文件时不应调用 fallback HTTP"),
                enable_live_control=False,
            )
            self.assertEqual(raw, "")

    def test_dry_run_records_preview_without_blocking_or_calling_backend(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            self.set_hook_control(repo, "dry-run")
            raw = self.invoke_raw(
                json.dumps({"tool_name": "Write", "tool_input": {"path": ".ssh/authorized_keys", "content": "ssh-rsa AAAA"}, "cwd": str(repo)}, ensure_ascii=False),
                go_cli=lambda _payload: self.fail("dry-run 模式不应调用 Go CLI"),
                post_json=lambda *_args, **_kwargs: self.fail("dry-run 模式不应调用 fallback HTTP"),
                enable_live_control=False,
            )
            self.assertEqual(raw, "")
            preview_path = repo / ".tmp" / "agenttoolgate" / "hook-dry-run.jsonl"
            self.assertTrue(preview_path.is_file())
            preview = json.loads(preview_path.read_text(encoding="utf-8").strip())
            self.assertEqual(preview["mode"], "dry-run")
            self.assertEqual(preview["decisionPreview"], "deny")
            self.assertNotIn("ssh-rsa", json.dumps(preview, ensure_ascii=False))

    def test_dry_run_previews_project_code_execution_as_approval(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            self.set_hook_control(repo, "dry-run")
            raw = self.invoke_raw(
                json.dumps(
                    {
                        "tool_name": "shell",
                        "tool_input": {"command": "go test ./..."},
                        "cwd": str(repo),
                    },
                    ensure_ascii=False,
                ),
                go_cli=lambda _payload: self.fail("dry-run 模式不应调用 Go CLI"),
                post_json=lambda *_args, **_kwargs: self.fail("dry-run 模式不应调用 fallback HTTP"),
                enable_live_control=False,
            )
            self.assertEqual(raw, "")
            preview_path = repo / ".tmp" / "agenttoolgate" / "hook-dry-run.jsonl"
            preview = json.loads(preview_path.read_text(encoding="utf-8").strip())
            self.assertEqual(preview["decisionPreview"], "ask")
            self.assertEqual(preview["riskLevel"], "medium")
            self.assertIn("project_code_execution", preview["signals"])

    def test_dry_run_redacts_sensitive_url_target(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            self.set_hook_control(repo, "dry-run")
            raw = self.invoke_raw(
                json.dumps(
                    {
                        "tool_name": "webfetch",
                        "tool_input": {"url": "https://example.test/path?token=super-secret-token&debug=true"},
                        "cwd": str(repo),
                    },
                    ensure_ascii=False,
                ),
                go_cli=lambda _payload: self.fail("dry-run 模式不应调用 Go CLI"),
                post_json=lambda *_args, **_kwargs: self.fail("dry-run 模式不应调用 fallback HTTP"),
                enable_live_control=False,
            )
            self.assertEqual(raw, "")
            preview_path = repo / ".tmp" / "agenttoolgate" / "hook-dry-run.jsonl"
            self.assertTrue(preview_path.is_file())
            preview_text = preview_path.read_text(encoding="utf-8")
            self.assertNotIn("super-secret-token", preview_text)
            preview = json.loads(preview_text.strip())
            self.assertEqual(preview["mode"], "dry-run")
            self.assertIn("decisionPreview", preview)
            self.assertTrue(preview["signals"])
            self.assertEqual(preview["target"], "https://example.test/path?token=[REDACTED]&debug=true")

    def test_go_cli_success_is_forwarded(self) -> None:
        expected = {
            "hookSpecificOutput": {
                "hookEventName": "PreToolUse",
                "permissionDecision": "deny",
                "permissionDecisionReason": "AgentToolGate 已阻止：命中根目录删除",
            }
        }
        output = self.invoke_hook(
            {"tool_name": "shell", "tool_input": {"command": "Remove-Item -Recurse ."}, "cwd": os.getcwd()},
            go_cli=lambda _payload: expected,
            post_json=lambda *_args, **_kwargs: self.fail("Go CLI 成功时不应调用 fallback HTTP"),
        )
        self.assertEqual(output, expected)

    def test_go_cli_allow_is_noop(self) -> None:
        output = self.invoke_raw(
            json.dumps({"tool_name": "shell", "args": {"command": "git status"}, "cwd": os.getcwd()}, ensure_ascii=False),
            go_cli=lambda _payload: {"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision": "allow"}},
            post_json=lambda *_args, **_kwargs: self.fail("allow no-op 不应调用 fallback HTTP"),
        )
        self.assertEqual(output, "")

    def test_go_cli_missing_falls_back_to_existing_offline_logic(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            output = self.invoke_hook(
                {"tool_name": "Write", "tool_input": {"path": ".ssh/authorized_keys", "content": "ssh-rsa AAAA"}, "cwd": str(repo)},
                go_cli=lambda _payload: None,
                post_json=lambda *_args, **_kwargs: (0, {}, "offline"),
            )
        decision = output["hookSpecificOutput"]
        self.assertEqual(decision["permissionDecision"], "deny")
        self.assertIn("offline", decision["permissionDecisionReason"].lower())

    def test_go_cli_missing_denies_sensitive_read_search_targets(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            cases = [
                ("Read", {"file_path": "src/client.pem"}),
                ("Grep", {"pattern": ".", "path": "src/client.pem"}),
            ]
            for tool_name, tool_input in cases:
                with self.subTest(tool=tool_name, tool_input=tool_input):
                    output = self.invoke_hook(
                        {"tool_name": tool_name, "tool_input": tool_input, "cwd": str(repo)},
                        go_cli=lambda _payload: None,
                        post_json=lambda *_args, **_kwargs: (0, {}, "offline"),
                    )
                    decision = output["hookSpecificOutput"]
                    self.assertEqual(decision["permissionDecision"], "deny")
                    self.assertIn("offline", decision["permissionDecisionReason"].lower())

    def test_go_cli_missing_allows_project_metadata_reads(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            cases = [
                ("Read", {"file_path": "AGENTS.md"}),
                ("Read", {"file_path": "package-lock.json"}),
                ("Read", {"file_path": ".claude/settings.local.json"}),
                ("Read", {"file_path": ".codex/config.toml"}),
                ("Grep", {"pattern": "timeout", "path": ".github/workflows"}),
                ("Glob", {"pattern": ".agents/**/*.md"}),
            ]
            for tool_name, tool_input in cases:
                with self.subTest(tool=tool_name, tool_input=tool_input):
                    output = self.invoke_raw(
                        json.dumps({"tool_name": tool_name, "tool_input": tool_input, "cwd": str(repo)}, ensure_ascii=False),
                        go_cli=lambda _payload: None,
                        post_json=lambda *_args, **_kwargs: (0, {}, "offline"),
                    )
                    self.assertEqual(output, "")

    def test_fallback_output_does_not_leak_secret_or_updated_input(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            output = self.invoke_hook(
                {
                    "tool_name": "Write",
                    "tool_input": {"path": ".env", "content": "ATG_TOKEN=super-secret-token"},
                    "cwd": str(repo),
                },
                go_cli=lambda _payload: None,
                post_json=lambda *_args, **_kwargs: (0, {}, "offline"),
            )
        raw = json.dumps(output, ensure_ascii=False).lower()
        self.assertNotIn("super-secret-token", raw)
        self.assertNotIn("atg_token", raw)
        self.assertNotIn("updatedinput", raw)

    def test_http_fallback_allow_is_noop(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            output = self.invoke_raw(
                json.dumps({"tool_name": "shell", "args": {"command": "git status"}, "cwd": str(repo)}, ensure_ascii=False),
                go_cli=lambda _payload: None,
                post_json=lambda *_args, **_kwargs: (200, {"decision": "allow", "reason": "safe"}, ""),
            )
        self.assertEqual(output, "")

    def test_http_fallback_cannot_allow_unapproved_workspace_write(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            output = self.invoke_hook(
                {"tool_name": "Write", "tool_input": {"path": "src/demo.txt", "content": "hello"}, "cwd": str(repo)},
                go_cli=lambda _payload: None,
                post_json=lambda *_args, **_kwargs: (200, {"decision": "allow", "reason": "stale backend allow"}, ""),
            )
        self.assertEqual(output["hookSpecificOutput"]["permissionDecision"], "deny")

    def test_http_fallback_deny_output_does_not_include_updated_input(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            output = self.invoke_hook(
                {"tool_name": "Write", "tool_input": {"path": ".env.local", "content": "ATG_TOKEN=super-secret-token"}, "cwd": str(repo)},
                go_cli=lambda _payload: None,
                post_json=lambda *_args, **_kwargs: (200, {"decision": "deny", "reason": "blocked"}, ""),
            )
        raw = json.dumps(output, ensure_ascii=False).lower()
        self.assertNotIn("updatedinput", raw)
        self.assertNotIn("super-secret-token", raw)

    def test_http_non_2xx_and_unknown_decision_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            input_data = {
                "tool_name": "Write",
                "tool_input": {"path": "src/demo.txt", "content": "hello"},
                "cwd": str(repo),
            }
            for response in (
                (403, {"error": "forbidden"}, ""),
                (200, {"decision": "unexpected", "reason": "bad contract"}, ""),
            ):
                with self.subTest(response=response):
                    output = self.invoke_hook(
                        input_data,
                        go_cli=lambda _payload: None,
                        post_json=lambda *_args, response=response, **_kwargs: response,
                    )
                    self.assertEqual(output["hookSpecificOutput"]["permissionDecision"], "deny")

    def test_pending_audit_redacts_target(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            HOOK.record_local_pending_audit(
                str(repo),
                {
                    "tool": "webfetch",
                    "actionType": "read",
                    "target": "https://example.test/path?token=super-secret-token&debug=true",
                },
                "offline",
                True,
            )
            audit_path = repo / ".tmp" / "local-action-firewall" / "pending-audit.jsonl"
            raw = audit_path.read_text(encoding="utf-8")
            self.assertNotIn("super-secret-token", raw)
            record = json.loads(raw.strip())
            self.assertEqual(record["target"], "https://example.test/path?token=[REDACTED]&debug=true")

    def test_offline_allow_denies_when_pending_audit_cannot_be_written(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            blocker = repo / ".tmp" / "local-action-firewall"
            blocker.parent.mkdir(parents=True)
            blocker.write_text("not-a-directory", encoding="utf-8")
            output = self.invoke_hook(
                {"tool_name": "Read", "tool_input": {"file_path": "README.md"}, "cwd": str(repo)},
                go_cli=lambda _payload: None,
                post_json=lambda *_args, **_kwargs: (0, {}, "offline"),
            )
            decision = output["hookSpecificOutput"]
            self.assertEqual(decision["permissionDecision"], "deny")
            self.assertIn("pending audit unavailable", decision["permissionDecisionReason"])

    def test_ticket_persistence_failure_denies(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()

            def post_json(*_args: Any, **_kwargs: Any) -> tuple[int, dict[str, Any], str]:
                blocker = repo / ".tmp" / "agenttoolgate" / "hook-tickets"
                blocker.parent.mkdir(parents=True, exist_ok=True)
                blocker.write_text("not-a-directory", encoding="utf-8")
                return (
                    200,
                    {
                        "decision": "deny_with_ticket",
                        "approvalId": "approval-persist-failure",
                        "fingerprint": "fingerprint-persist-failure",
                    },
                    "",
                )

            output = self.invoke_hook(
                {"tool_name": "Write", "tool_input": {"path": "src/demo.txt", "content": "hello"}, "cwd": str(repo)},
                go_cli=lambda _payload: None,
                post_json=post_json,
            )
            decision = output["hookSpecificOutput"]
            self.assertEqual(decision["permissionDecision"], "deny")
            self.assertIn("hook ticket persistence failed", decision["permissionDecisionReason"])

    def test_ticket_is_reused_only_for_matching_retry(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            captured: list[dict[str, Any]] = []
            responses = iter(
                [
                    (
                        200,
                        {
                            "decision": "deny_with_ticket",
                            "reason": "approval required",
                            "approvalId": "approval-test-1",
                            "approvalStatus": "pending",
                            "fingerprint": "fingerprint-test-1",
                        },
                        "",
                    ),
                    (200, {"decision": "allow", "reason": "ticket consumed"}, ""),
                ]
            )

            def post_json(_url: str, payload: dict[str, Any], **_kwargs: Any) -> tuple[int, dict[str, Any], str]:
                captured.append(dict(payload))
                return next(responses)

            input_data = {
                "tool_name": "Write",
                "tool_input": {"path": "src/demo.txt", "content": "ordinary workspace update"},
                "cwd": str(repo),
            }
            first = self.invoke_hook(input_data, go_cli=lambda _payload: None, post_json=post_json)
            self.assertEqual(first["hookSpecificOutput"]["permissionDecision"], "deny")
            second = self.invoke_raw(
                json.dumps(input_data, ensure_ascii=False),
                go_cli=lambda _payload: None,
                post_json=post_json,
            )
            self.assertEqual(second, "")
            self.assertNotIn("ticketId", captured[0])
            self.assertEqual(captured[1]["ticketId"], "approval-test-1")
            ticket_dir = repo / ".tmp" / "agenttoolgate" / "hook-tickets"
            self.assertFalse(any(ticket_dir.glob("*.json")))

    def test_invalid_json_does_not_crash(self) -> None:
        raw = self.invoke_raw("{bad json", go_cli=lambda _payload: self.fail("非法 JSON 不应调用 Go CLI"))
        self.assertEqual(raw, "")

    def test_go_cli_invalid_json_output_is_ignored(self) -> None:
        original_run = HOOK.subprocess.run

        class Completed:
            returncode = 0
            stdout = "not-json"

        try:
            HOOK.subprocess.run = lambda *_args, **_kwargs: Completed()
            self.assertIsNone(HOOK.call_agenttoolgate_guard_hook_codex({"tool_name": "shell"}))
        finally:
            HOOK.subprocess.run = original_run

    def test_go_cli_unavailable_noops_and_records_pending_audit(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            output = self.invoke_raw(
                json.dumps({"tool_name": "bash", "args": {"command": "git status"}, "cwd": str(repo)}, ensure_ascii=False),
                go_cli=lambda _payload: None,
                post_json=lambda *_args, **_kwargs: (0, {}, "offline"),
            )
            audit_path = repo / ".tmp" / "local-action-firewall" / "pending-audit.jsonl"
            self.assertEqual(output, "")
            self.assertTrue(audit_path.is_file(), "allow no-op 应记录 pending audit")

    def invoke_hook(
        self,
        input_data: dict[str, Any],
        go_cli: Callable[[dict[str, Any]], dict[str, Any] | None] | None = None,
        post_json: Callable[..., tuple[int, dict[str, Any], str]] | None = None,
    ) -> dict[str, Any]:
        raw = self.invoke_raw(json.dumps(input_data, ensure_ascii=False), go_cli=go_cli, post_json=post_json)
        self.assertTrue(raw, "hook should emit a decision")
        return json.loads(raw)

    def invoke_raw(
        self,
        stdin_text: str,
        go_cli: Callable[[dict[str, Any]], dict[str, Any] | None] | None = None,
        post_json: Callable[..., tuple[int, dict[str, Any], str]] | None = None,
        enable_live_control: bool = True,
    ) -> str:
        original_stdin = sys.stdin
        original_stdout = sys.stdout
        original_go_cli = HOOK.call_agenttoolgate_guard_hook_codex
        original_post_json = HOOK.post_json
        old_disable = os.environ.pop("TRELLIS_DISABLE_HOOKS", None)
        old_hooks = os.environ.pop("TRELLIS_HOOKS", None)
        if enable_live_control:
            try:
                input_data = json.loads(stdin_text)
                repo_root = HOOK.find_repo_root(input_data.get("cwd", os.getcwd())) if isinstance(input_data, dict) else None
                if repo_root is not None:
                    self.set_hook_control(Path(repo_root), "live")
            except Exception:
                pass
        try:
            if go_cli is not None:
                HOOK.call_agenttoolgate_guard_hook_codex = go_cli
            if post_json is not None:
                HOOK.post_json = post_json
            sys.stdin = io.StringIO(stdin_text)
            captured = io.StringIO()
            sys.stdout = captured
            self.assertEqual(HOOK.main(), 0)
            return captured.getvalue().strip()
        finally:
            HOOK.call_agenttoolgate_guard_hook_codex = original_go_cli
            HOOK.post_json = original_post_json
            sys.stdin = original_stdin
            sys.stdout = original_stdout
            if old_disable is not None:
                os.environ["TRELLIS_DISABLE_HOOKS"] = old_disable
            if old_hooks is not None:
                os.environ["TRELLIS_HOOKS"] = old_hooks


if __name__ == "__main__":
    unittest.main()
