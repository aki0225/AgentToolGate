#!/usr/bin/env python3
"""真实 Codex 演示证据处理的最小回归测试。"""

from __future__ import annotations

import importlib.util
import hashlib
import tempfile
import unittest
from unittest import mock
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("run_demo.py")
SPEC = importlib.util.spec_from_file_location("run_demo", MODULE_PATH)
assert SPEC and SPEC.loader
RUN_DEMO = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RUN_DEMO)


def load_sibling_module(name: str):
    path = Path(__file__).with_name(f"{name}.py")
    spec = importlib.util.spec_from_file_location(name, path)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


FINALIZE = load_sibling_module("finalize_public_artifacts")


class RealCodexDemoTest(unittest.TestCase):
    def test_sanitize_value_removes_paths_and_credentials(self) -> None:
        value = {
            "path": "/tmp/private/repo/.ssh/authorized_keys",
            "authorization": "Bearer secret",
            "nested": ["sk-example-secret-value-123456"],
        }
        sanitized = RUN_DEMO.sanitize_value(
            value,
            {
                "/tmp/private/repo": "<disposable-repo>",
                "sk-example-secret-value-123456": "<REDACTED_SECRET>",
            },
        )
        self.assertEqual(sanitized["path"], "<disposable-repo>/.ssh/authorized_keys")
        self.assertEqual(sanitized["authorization"], "<REDACTED_SECRET>")
        self.assertEqual(sanitized["nested"], ["<REDACTED_SECRET>"])

    def test_validate_results_requires_real_mcp_audit_and_denied_side_effect(self) -> None:
        unique_message = "synthetic-real-codex-test"
        events = [
            {
                "type": "item.completed",
                "item": {
                    "type": "command_execution",
                    "command": "git status --short",
                    "exit_code": 0,
                },
            },
            {
                "type": "item.completed",
                "item": {
                    "type": "mcp_tool_call",
                    "server": "agenttoolgate",
                    "tool": "mock.echo",
                    "arguments": {"message": unique_message},
                    "status": "completed",
                },
            },
            {
                "type": "item.completed",
                "item": {
                    "type": "command_execution",
                    "command": "cat tool-output.txt",
                    "exit_code": 0,
                },
            },
        ]
        audits = [
            {
                "toolKey": "mock.echo",
                "status": "success",
                "inputRedactedJson": {"message": unique_message},
            },
            {
                "toolKey": "agent_guard.evaluate",
                "status": "denied",
                "policyDecision": "deny",
                "inputRedactedJson": {
                    "target": ".",
                    "guardDecision": "deny",
                    "guardRiskLevel": "critical",
                },
            },
        ]
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            RUN_DEMO.run_checked(["git", "init", "--initial-branch=main"], cwd=repo)
            RUN_DEMO.run_checked(["git", "config", "user.name", "Test"], cwd=repo)
            RUN_DEMO.run_checked(["git", "config", "user.email", "test@example.invalid"], cwd=repo)
            (repo / "README.md").write_text("test\n", encoding="utf-8")
            (repo / RUN_DEMO.SYNTHETIC_SENTINEL).write_text("preserved\n", encoding="utf-8")
            RUN_DEMO.run_checked(["git", "add", "."], cwd=repo)
            RUN_DEMO.run_checked(["git", "commit", "-m", "初始化"], cwd=repo)
            baseline = {
                "head": RUN_DEMO.run_checked(["git", "rev-parse", "HEAD"], cwd=repo).stdout.strip(),
                "tree": RUN_DEMO.run_checked(
                    ["git", "rev-parse", "HEAD^{tree}"], cwd=repo
                ).stdout.strip(),
                "sentinelSha256": hashlib.sha256(
                    (repo / RUN_DEMO.SYNTHETIC_SENTINEL).read_bytes()
                ).hexdigest(),
            }
            result = RUN_DEMO.validate_results(
                events,
                audits,
                repo,
                unique_message,
                0,
                baseline,
                "Command blocked by PreToolUse hook: 命中根目录删除\n",
            )
        self.assertTrue(result["checks"]["workspaceRootDeleteDeniedOnce"])
        self.assertTrue(result["checks"]["sentinelFilePreserved"])
        self.assertTrue(result["checks"]["repositoryClean"])

    def test_write_cast_preserves_event_order(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            path = Path(temp_dir) / "demo.cast"
            RUN_DEMO.write_cast(path, [(0.1, "第一步"), (0.3, "第二步")])
            content = path.read_text(encoding="utf-8")
        self.assertLess(content.index("第一步"), content.index("第二步"))

    def test_api_key_file_is_outside_public_evidence_contract(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            private_root = Path(temp_dir) / "private"
            output = Path(temp_dir) / "public"
            private_root.mkdir()
            output.mkdir()
            key_file = private_root / "provider.key"
            key_file.write_text("synthetic-secret", encoding="utf-8")
            key_file.unlink()
            self.assertFalse(key_file.exists())
            self.assertEqual(list(output.iterdir()), [])

    def test_disable_hook_control_uses_repo_as_cwd_without_dir_flag(self) -> None:
        calls = [
            mock.Mock(returncode=0, stdout="", stderr=""),
            mock.Mock(returncode=0, stdout="mode: off\n", stderr=""),
        ]
        with mock.patch.object(RUN_DEMO, "run_checked", side_effect=calls) as invoked:
            RUN_DEMO.disable_hook_control(Path("/opt/agenttoolgate"), Path("/tmp/repo"))
        first_args = invoked.call_args_list[0].args[0]
        second_args = invoked.call_args_list[1].args[0]
        self.assertNotIn("--dir", first_args)
        self.assertNotIn("--dir", second_args)
        self.assertEqual(invoked.call_args_list[0].kwargs["cwd"], Path("/tmp/repo"))
        self.assertEqual(invoked.call_args_list[1].kwargs["cwd"], Path("/tmp/repo"))

    def test_workspace_root_delete_matching_is_exact(self) -> None:
        repo = Path("/tmp/disposable-repo")
        self.assertTrue(RUN_DEMO.is_workspace_root_delete({"target": "."}, repo))
        self.assertTrue(
            RUN_DEMO.is_workspace_root_delete({"target": str(repo)}, repo)
        )
        for target in ("./cache", ".agenttoolgate", "echo rm -rf ."):
            self.assertFalse(
                RUN_DEMO.is_workspace_root_delete({"target": target}, repo),
                target,
            )

    def test_expected_command_matching_rejects_echo_and_extra_commands(self) -> None:
        self.assertTrue(
            RUN_DEMO.is_expected_command(
                r'''"C:\Program Files\PowerShell\7\pwsh.exe" -Command 'git status --short' ''',
                "git-status",
            )
        )
        self.assertTrue(
            RUN_DEMO.is_expected_command(
                "/bin/bash -lc 'cat tool-output.txt'", "fixture-read"
            )
        )
        self.assertFalse(
            RUN_DEMO.is_expected_command("echo git status --short", "git-status")
        )

    def test_sanitize_text_covers_common_secret_formats(self) -> None:
        raw = (
            'sk-proj-example-secret-value '
            '{"Authorization":"Bearer example-bearer-value-123456"}'
        )
        sanitized = RUN_DEMO.sanitize_text(raw, {})
        self.assertNotIn("sk-proj-example-secret-value", sanitized)
        self.assertNotIn("example-bearer-value-123456", sanitized)

    def test_prepare_managed_directory_rejects_nonempty_unowned_path(self) -> None:
        with tempfile.TemporaryDirectory(prefix="real-codex-demo-test-") as temp_dir:
            root = Path(temp_dir)
            private = root / "private"
            private.mkdir()
            (private / "keep.txt").write_text("keep\n", encoding="utf-8")
            with self.assertRaises(RUN_DEMO.DemoFailure):
                RUN_DEMO.prepare_managed_directory(private, "私有")
            self.assertTrue((private / "keep.txt").exists())

    def test_prepare_managed_directory_accepts_owned_path(self) -> None:
        with tempfile.TemporaryDirectory(prefix="real-codex-demo-test-") as temp_dir:
            root = Path(temp_dir)
            private = root / "private"
            private.mkdir()
            marker = RUN_DEMO.ownership_marker(private)
            marker.write_text("owned\n", encoding="utf-8")
            (private / "stale.txt").write_text("stale\n", encoding="utf-8")
            RUN_DEMO.prepare_managed_directory(private, "私有")
            self.assertFalse((private / "stale.txt").exists())
            self.assertTrue(marker.exists())

    def test_finalize_contract_requires_complete_success_or_failure(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            with self.assertRaises(ValueError):
                FINALIZE.validate_public_artifact_contract(root)

            (root / "failure.json").write_text("{}\n", encoding="utf-8")
            FINALIZE.validate_public_artifact_contract(root)

            for name in FINALIZE.SUCCESS_FILES:
                (root / name).write_text("{}\n", encoding="utf-8")
            with self.assertRaises(ValueError):
                FINALIZE.validate_public_artifact_contract(root)

    def test_finalize_contract_returns_artifact_mode(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            (root / "failure.json").write_text("{}\n", encoding="utf-8")
            self.assertEqual(
                FINALIZE.validate_public_artifact_contract(root),
                "failure",
            )

        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            for name in FINALIZE.SUCCESS_FILES:
                (root / name).write_text("{}\n", encoding="utf-8")
            self.assertEqual(
                FINALIZE.validate_public_artifact_contract(root),
                "success",
            )

    def test_subprocess_identity_is_optional_for_local_validation(self) -> None:
        identity, home = RUN_DEMO.subprocess_identity(None)
        self.assertEqual(identity, {})
        self.assertIsNone(home)


if __name__ == "__main__":
    unittest.main()
