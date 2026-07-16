#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""离线 Local Action Firewall 精度回归测试。

这些测试固定一个铁律：离线兜底只能砍误报，真实高危 write/exec 落点
仍必须 fail-closed deny。
"""

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
from typing import Any


def load_hook_module(path: Path | None = None, module_name: str = "agent_guard_pretool") -> ModuleType:
    hook_path = path or Path(__file__).with_name("agent-guard-pretool.py")
    if str(hook_path.parent) not in sys.path:
        sys.path.insert(0, str(hook_path.parent))
    spec = importlib.util.spec_from_file_location(module_name, hook_path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load hook module from {hook_path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


HOOK = load_hook_module()
CODEX_HOOK = load_hook_module(Path(__file__).parents[2] / ".codex" / "hooks" / "agent-guard-pretool.py", "codex_agent_guard_pretool")


class OfflineGuardPrecisionTest(unittest.TestCase):
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

    def test_script_target_detection_uses_suffix_only(self) -> None:
        self.assertTrue(HOOK.is_probably_script_target("scripts/demo.ps1"))
        self.assertFalse(HOOK.is_probably_script_target("rg startup ."))
        self.assertFalse(HOOK.is_probably_script_target("powershell -ExecutionPolicy Bypass -Command Get-ChildItem"))

    def test_high_risk_target_uses_path_segments_not_bare_substrings(self) -> None:
        allowed_targets = [
            "examples/agent-demo/evidence/windows-startup-poisoning-output.txt",
            "docs/credentials-review-notes.md",
            "tmp/secrets-report.md",
            "notes/taskschd-analysis.txt",
        ]
        for target in allowed_targets:
            with self.subTest(target=target):
                self.assertFalse(HOOK.is_probably_high_risk_target(target))

        denied_targets = [
            r"C:\Users\me\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup\run.ps1",
            ".ssh/authorized_keys",
            ".git/hooks/pre-commit",
            ".env",
            r"HKCU:\Software\Microsoft\Windows\CurrentVersion\Run\Updater",
            r"C:\Windows\System32\Config\SAM",
            r"C:\Windows\System32\Tasks\Updater",
            r"C:\Windows\System32\taskschd\job.txt",
        ]
        for target in denied_targets:
            with self.subTest(target=target):
                self.assertTrue(HOOK.is_probably_high_risk_target(target))

    def test_claude_and_codex_share_offline_risk_core(self) -> None:
        targets = [
            "examples/agent-demo/evidence/windows-startup-poisoning-output.txt",
            "docs/credentials-review-notes.md",
            r"C:\Users\me\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup\run.ps1",
            ".ssh/authorized_keys",
            ".git/hooks/pre-commit",
            r"HKCU:\Software\Microsoft\Windows\CurrentVersion\Run\Updater",
            r"C:\Windows\System32\Config\SAM",
            r"C:\Windows\System32\Tasks\Updater",
        ]
        for target in targets:
            with self.subTest(target=target):
                self.assertEqual(
                    HOOK.is_probably_high_risk_target(target),
                    CODEX_HOOK.is_probably_high_risk_target(target),
                )

        payloads = [
            {"target": "workspace/notes.md", "content": "普通说明文档里提到 startup 和 credentials"},
            {"target": r"C:\Windows\System32\Config\SAM", "content": ""},
            {"target": "workspace/run.ps1", "content": "powershell -ExecutionPolicy Bypass -WindowStyle Hidden"},
        ]
        for payload in payloads:
            with self.subTest(payload=payload):
                self.assertEqual(HOOK.is_high_risk_offline_target(payload), CODEX_HOOK.is_high_risk_offline_target(payload))

    def test_apply_patch_is_guarded_and_extracts_targets(self) -> None:
        patch = """*** Begin Patch
*** Add File: Users/aki/AppData/Roaming/Microsoft/Windows/Start Menu/Programs/Startup/run.ps1
+Write-Host owned
*** End Patch
"""
        for module in (HOOK, CODEX_HOOK):
            with self.subTest(module=module.__name__):
                self.assertTrue(module.is_guarded_tool("apply_patch"))
                payload = module.build_agent_guard_request(
                    "codex",
                    {"cwd": os.getcwd()},
                    "apply_patch",
                    {"content": patch},
                )
                self.assertEqual(payload["actionType"], "write")
                self.assertIn("Startup", payload["target"])
                self.assertTrue(module.is_high_risk_offline_target(payload))

    def test_missing_control_file_defaults_to_noop(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            raw = self.invoke_raw(
                {"tool_name": "Write", "tool_input": {"path": ".ssh/authorized_keys", "content": "ssh-rsa AAAA"}, "cwd": str(repo)},
                post_json=lambda *_args, **_kwargs: self.fail("缺失控制文件时不应调用后端"),
                enable_live=False,
            )
            self.assertEqual(raw, "")

    def test_invalid_control_file_defaults_to_noop(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            self.set_hook_control(repo, raw="{bad json")
            raw = self.invoke_raw(
                {"tool_name": "Write", "tool_input": {"path": ".env", "content": "TOKEN=x"}, "cwd": str(repo)},
                post_json=lambda *_args, **_kwargs: self.fail("损坏控制文件时不应调用后端"),
                enable_live=False,
            )
            self.assertEqual(raw, "")

    def test_dry_run_records_preview_without_blocking_or_calling_backend(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            self.set_hook_control(repo, "dry-run")
            raw = self.invoke_raw(
                {"tool_name": "Write", "tool_input": {"path": ".git/hooks/pre-commit", "content": "#!/bin/sh"}, "cwd": str(repo)},
                post_json=lambda *_args, **_kwargs: self.fail("dry-run 模式不应调用后端"),
                enable_live=False,
            )
            self.assertEqual(raw, "")
            preview_path = repo / ".tmp" / "agenttoolgate" / "hook-dry-run.jsonl"
            self.assertTrue(preview_path.is_file())
            preview = json.loads(preview_path.read_text(encoding="utf-8").strip())
            self.assertEqual(preview["mode"], "dry-run")
            self.assertEqual(preview["decisionPreview"], "would_block_in_live")
            self.assertNotIn("#!/bin/sh", json.dumps(preview, ensure_ascii=False))

    def test_dry_run_redacts_sensitive_url_target(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            self.set_hook_control(repo, "dry-run")
            raw = self.invoke_raw(
                {
                    "tool_name": "webfetch",
                    "tool_input": {"url": "https://example.test/path?token=super-secret-token&debug=true"},
                    "cwd": str(repo),
                },
                post_json=lambda *_args, **_kwargs: self.fail("dry-run 模式不应调用后端"),
                enable_live=False,
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

    def test_offline_read_only_and_keyword_mentions_are_allowed_with_pending_audit(self) -> None:
        cases = [
            (
                "bash",
                {"command": "sed -n '1,40p' examples/agent-demo/evidence/windows-startup-poisoning-output.txt"},
            ),
            ("bash", {"command": 'rg "startup" .'}),
            ("bash", {"command": "grep credentials file"}),
            ("bash", {"command": "ls examples/agent-demo/*.ps1"}),
            (
                "Write",
                {
                    "path": "report.md",
                    "content": "这是一段描述自启目录 / credentials 风险的普通说明文档。",
                },
            ),
        ]
        for tool_name, tool_input in cases:
            with self.subTest(tool=tool_name, tool_input=tool_input):
                decision, repo = self.run_offline_hook(tool_name, tool_input)
                self.assertEqual(decision["permissionDecision"], "allow")
                audit_path = repo / ".tmp" / "local-action-firewall" / "pending-audit.jsonl"
                self.assertTrue(audit_path.is_file(), "离线 allow 必须留下 pending audit")

    def test_offline_real_high_risk_targets_still_fail_closed(self) -> None:
        cases = [
            (
                "Write",
                {
                    "path": r"C:\Users\me\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup\x.ps1",
                    "content": "Write-Host owned",
                },
            ),
            ("Write", {"path": ".ssh/authorized_keys", "content": "ssh-rsa AAAA"}),
            ("Write", {"path": ".git/hooks/pre-commit", "content": "#!/bin/sh"}),
            ("Write", {"path": ".env", "content": "TOKEN=demo"}),
            (
                "bash",
                {
                    "command": r'powershell -NoProfile -Command "Set-Content -Path C:\Users\me\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup\run.ps1 -Value x"',
                },
            ),
            (
                "bash",
                {
                    "command": r'echo x > C:\Users\me\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup\run.ps1',
                },
            ),
            ("bash", {"command": r'reg add HKCU\Software\Microsoft\Windows\CurrentVersion\Run /v Updater /d calc.exe'}),
            ("bash", {"command": r'type C:\Windows\System32\Config\SAM'}),
            (
                "bash",
                {
                    "command": "powershell -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -EncodedCommand SQBFAFgA",
                },
            ),
        ]
        for tool_name, tool_input in cases:
            with self.subTest(tool=tool_name, tool_input=tool_input):
                decision, repo = self.run_offline_hook(tool_name, tool_input)
                self.assertEqual(decision["permissionDecision"], "deny")
                self.assertIn("offline", decision["permissionDecisionReason"].lower())
                audit_path = repo / ".tmp" / "local-action-firewall" / "pending-audit.jsonl"
                self.assertFalse(audit_path.exists(), "离线 deny 不应伪造 allow pending audit")

    def run_offline_hook(self, tool_name: str, tool_input: dict[str, Any]) -> tuple[dict[str, Any], Path]:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            input_data = {
                "tool_name": tool_name,
                "tool_input": tool_input,
                "cwd": str(repo),
            }
            output = self.invoke_hook(input_data)
            decision = output["hookSpecificOutput"]
            self.assertEqual(decision["hookEventName"], "PreToolUse")
            # tempdir 会在 with 退出后删除；这里复制到持久目录用于调用方断言。
            persisted = Path(tempfile.mkdtemp())
            for child in repo.iterdir():
                if child.name == ".git":
                    (persisted / ".git").mkdir()
                elif child.name == ".tmp":
                    self.copy_tree(child, persisted / ".tmp")
            return decision, persisted

    def invoke_hook(self, input_data: dict[str, Any]) -> dict[str, Any]:
        raw = self.invoke_raw(input_data)
        self.assertTrue(raw, "hook should emit a decision")
        return json.loads(raw)

    def invoke_raw(
        self,
        input_data: dict[str, Any],
        post_json: Any | None = None,
        enable_live: bool = True,
    ) -> str:
        original_post_json = HOOK.post_json
        original_stdin = sys.stdin
        original_stdout = sys.stdout
        old_disable = os.environ.pop("TRELLIS_DISABLE_HOOKS", None)
        old_hooks = os.environ.pop("TRELLIS_HOOKS", None)
        try:
            repo_root = HOOK.find_repo_root(input_data.get("cwd", os.getcwd()))
            if enable_live and repo_root is not None:
                self.set_hook_control(Path(repo_root), "live")
            HOOK.post_json = post_json or (lambda *args, **kwargs: (0, {}, "offline"))
            sys.stdin = io.StringIO(json.dumps(input_data, ensure_ascii=False))
            captured = io.StringIO()
            sys.stdout = captured
            self.assertEqual(HOOK.main(), 0)
            raw = captured.getvalue().strip()
            return raw
        finally:
            HOOK.post_json = original_post_json
            sys.stdin = original_stdin
            sys.stdout = original_stdout
            if old_disable is not None:
                os.environ["TRELLIS_DISABLE_HOOKS"] = old_disable
            if old_hooks is not None:
                os.environ["TRELLIS_HOOKS"] = old_hooks

    def copy_tree(self, source: Path, target: Path) -> None:
        target.mkdir(parents=True, exist_ok=True)
        for child in source.iterdir():
            destination = target / child.name
            if child.is_dir():
                self.copy_tree(child, destination)
            else:
                destination.write_bytes(child.read_bytes())


if __name__ == "__main__":
    unittest.main()
