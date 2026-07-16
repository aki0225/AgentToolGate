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
            self.assertEqual(preview["decisionPreview"], "would_block_in_live")
            self.assertNotIn("ssh-rsa", json.dumps(preview, ensure_ascii=False))

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
