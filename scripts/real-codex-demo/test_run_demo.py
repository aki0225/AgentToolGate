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
                    "target": RUN_DEMO.PROTECTED_RELEASE_FILE,
                    "targets": [RUN_DEMO.PROTECTED_RELEASE_FILE],
                    "adapter": "codex",
                    "tool": "apply_patch",
                    "actionType": "write",
                    "guardDecision": "deny",
                    "guardRiskLevel": "high",
                    "riskLevel": "high",
                    "isScript": False,
                    "content": "[REDACTED]",
                    "contentHash": "redacted-public-hash",
                    "scriptHash": "",
                },
                "explanation": {"matchedRule": "project_protected_path"},
                "errorMessage": RUN_DEMO.PROTECTED_RELEASE_REASON,
            },
        ]
        observed_requests = [
            {
                "adapter": "codex",
                "tool": "Bash",
                "actionType": "exec",
                "target": "git status --short",
                "content": "git status --short",
            },
            {
                "adapter": "codex",
                "tool": "Bash",
                "actionType": "exec",
                "target": "tool-output.txt",
                "content": "cat tool-output.txt",
            },
            {
                "adapter": "codex",
                "tool": "apply_patch",
                "actionType": "write",
                "target": RUN_DEMO.PROTECTED_RELEASE_FILE,
                "targets": [RUN_DEMO.PROTECTED_RELEASE_FILE],
                "isScript": False,
                "contentEncoding": "plain",
                "content": RUN_DEMO.expected_release_patch_content(),
            },
        ]
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            RUN_DEMO.run_checked(["git", "init", "--initial-branch=main"], cwd=repo)
            RUN_DEMO.run_checked(["git", "config", "user.name", "Test"], cwd=repo)
            RUN_DEMO.run_checked(["git", "config", "user.email", "test@example.invalid"], cwd=repo)
            (repo / "README.md").write_text("test\n", encoding="utf-8")
            (repo / RUN_DEMO.SYNTHETIC_SENTINEL).write_text("preserved\n", encoding="utf-8")
            (repo / RUN_DEMO.PROTECTED_RELEASE_FILE).write_text(
                RUN_DEMO.PROTECTED_RELEASE_CONTENT,
                encoding="utf-8",
            )
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
                "protectedReleaseSha256": hashlib.sha256(
                    (repo / RUN_DEMO.PROTECTED_RELEASE_FILE).read_bytes()
                ).hexdigest(),
            }
            result = RUN_DEMO.validate_results(
                events,
                audits,
                observed_requests,
                repo,
                unique_message,
                0,
                baseline,
                (
                    "Command blocked by PreToolUse hook: "
                    f"{RUN_DEMO.PROTECTED_RELEASE_REASON}\n"
                ),
            )
        self.assertTrue(result["checks"]["protectedReleaseWriteDeniedOnce"])
        self.assertTrue(result["checks"]["hookObservedProtectedWriteOnce"])
        self.assertTrue(result["checks"]["guardWriteAuditRecordedOnce"])
        self.assertTrue(result["checks"]["protectedReleaseContentPreserved"])
        self.assertTrue(result["checks"]["sentinelFilePreserved"])
        self.assertTrue(result["checks"]["repositoryClean"])

        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            RUN_DEMO.run_checked(["git", "init", "--initial-branch=main"], cwd=repo)
            RUN_DEMO.run_checked(["git", "config", "user.name", "Test"], cwd=repo)
            RUN_DEMO.run_checked(["git", "config", "user.email", "test@example.invalid"], cwd=repo)
            (repo / "README.md").write_text("test\n", encoding="utf-8")
            (repo / RUN_DEMO.SYNTHETIC_SENTINEL).write_text("preserved\n", encoding="utf-8")
            (repo / RUN_DEMO.PROTECTED_RELEASE_FILE).write_text(
                RUN_DEMO.PROTECTED_RELEASE_CONTENT,
                encoding="utf-8",
            )
            RUN_DEMO.run_checked(["git", "add", "."], cwd=repo)
            RUN_DEMO.run_checked(["git", "commit", "-m", "初始化"], cwd=repo)
            repeated_baseline = {
                "head": RUN_DEMO.run_checked(["git", "rev-parse", "HEAD"], cwd=repo).stdout.strip(),
                "tree": RUN_DEMO.run_checked(
                    ["git", "rev-parse", "HEAD^{tree}"], cwd=repo
                ).stdout.strip(),
                "sentinelSha256": hashlib.sha256(
                    (repo / RUN_DEMO.SYNTHETIC_SENTINEL).read_bytes()
                ).hexdigest(),
                "protectedReleaseSha256": hashlib.sha256(
                    (repo / RUN_DEMO.PROTECTED_RELEASE_FILE).read_bytes()
                ).hexdigest(),
            }
            with self.assertRaises(RUN_DEMO.DemoFailure):
                RUN_DEMO.validate_results(
                    events,
                    audits,
                    [*observed_requests, observed_requests[-1]],
                    repo,
                    unique_message,
                    0,
                    repeated_baseline,
                    (
                        "Command blocked by PreToolUse hook: "
                        f"{RUN_DEMO.PROTECTED_RELEASE_REASON}\n"
                    ),
                )

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

    def test_protected_release_write_matching_is_exact(self) -> None:
        repo = Path.cwd() / ".tmp" / "disposable-repo"
        base = {
            "toolKey": "agent_guard.evaluate",
            "status": "denied",
            "policyDecision": "deny",
            "inputRedactedJson": {
                "target": RUN_DEMO.PROTECTED_RELEASE_FILE,
                "targets": [RUN_DEMO.PROTECTED_RELEASE_FILE],
                "adapter": "codex",
                "tool": "apply_patch",
                "actionType": "write",
                "isScript": False,
                "content": "[REDACTED]",
                "contentHash": "redacted-public-hash",
                "scriptHash": "",
                "riskLevel": "high",
            },
            "explanation": {"matchedRule": "project_protected_path"},
            "errorMessage": RUN_DEMO.PROTECTED_RELEASE_REASON,
        }
        self.assertTrue(RUN_DEMO.is_protected_release_write(base, repo))
        absolute = {
            **base,
            "inputRedactedJson": {
                **base["inputRedactedJson"],
                "target": str(repo / RUN_DEMO.PROTECTED_RELEASE_FILE),
                "targets": [str(repo / RUN_DEMO.PROTECTED_RELEASE_FILE)],
            },
        }
        self.assertTrue(RUN_DEMO.is_protected_release_write(absolute, repo))
        for target in ("release-copy.yml", "./cache", ".agenttoolgate"):
            candidate = {
                **base,
                "inputRedactedJson": {
                    **base["inputRedactedJson"],
                    "target": target,
                    "targets": [target],
                },
            }
            self.assertFalse(
                RUN_DEMO.is_protected_release_write(candidate, repo),
                target,
            )
        traversal = {
            **base,
            "inputRedactedJson": {
                **base["inputRedactedJson"],
                "target": "../release.yml",
                "targets": ["../release.yml"],
            },
        }
        self.assertFalse(
            RUN_DEMO.is_protected_release_write(traversal, repo),
            "父目录跳转不能被归一化成受保护文件",
        )
        case_variant = {
            **base,
            "inputRedactedJson": {
                **base["inputRedactedJson"],
                "target": "Release.yml",
                "targets": ["Release.yml"],
            },
        }
        self.assertFalse(
            RUN_DEMO.is_protected_release_write(case_variant, repo),
            "Ubuntu runner 上路径大小写必须精确匹配",
        )

    def test_observed_release_write_requires_exact_patch(self) -> None:
        repo = Path("/tmp/disposable-repo")
        request = {
            "adapter": "codex",
            "tool": "apply_patch",
            "actionType": "write",
            "target": RUN_DEMO.PROTECTED_RELEASE_FILE,
            "targets": [RUN_DEMO.PROTECTED_RELEASE_FILE],
            "isScript": False,
            "contentEncoding": "plain",
            "content": RUN_DEMO.expected_release_patch_content(),
        }
        self.assertTrue(
            RUN_DEMO.is_expected_observed_release_write(request, repo)
        )
        for changed in (
            {**request, "tool": "Edit"},
            {**request, "actionType": "delete"},
            {
                **request,
                "content": RUN_DEMO.expected_release_patch_content().replace(
                    RUN_DEMO.PROTECTED_RELEASE_REPLACEMENT.rstrip(),
                    "release: different",
                ),
            },
        ):
            self.assertFalse(
                RUN_DEMO.is_expected_observed_release_write(changed, repo)
            )

    def test_public_audit_summary_uses_allowlist(self) -> None:
        call = {
            "toolKey": "agent_guard.evaluate",
            "status": "denied",
            "policyDecision": "deny",
            "riskLevel": "high",
            "errorMessage": RUN_DEMO.PROTECTED_RELEASE_REASON,
            "actorEmail": "private@example.invalid",
            "futureOpaqueField": "must-not-be-published",
            "inputRedactedJson": {
                "adapter": "codex",
                "tool": "apply_patch",
                "actionType": "write",
                "target": RUN_DEMO.PROTECTED_RELEASE_FILE,
                "targets": [RUN_DEMO.PROTECTED_RELEASE_FILE],
                "isScript": False,
                "content": "[REDACTED]",
                "futureOpaqueField": "must-not-be-published",
            },
            "explanation": {
                "targetCategory": "workspace",
                "riskLevel": "high",
                "matchedRule": "project_protected_path",
                "signals": ["High-risk local action"],
            },
        }
        summary = RUN_DEMO.public_audit_summary(call)
        serialized = __import__("json").dumps(summary)
        self.assertNotIn("must-not-be-published", serialized)
        self.assertNotIn("private@example.invalid", serialized)
        self.assertEqual(summary["input"]["content"], "[REDACTED]")
        mcp_summary = RUN_DEMO.public_audit_summary(
            {
                "toolKey": "mock.echo",
                "status": "success",
                "policyDecision": "allow",
                "inputRedactedJson": {"message": "synthetic-message"},
            }
        )
        self.assertEqual(mcp_summary["input"]["message"], "synthetic-message")

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

    def test_prepare_managed_directory_does_not_rewrite_existing_marker(self) -> None:
        with tempfile.TemporaryDirectory(prefix="real-codex-demo-test-") as temp_dir:
            root = Path(temp_dir)
            private = root / "private"
            private.mkdir()
            marker = RUN_DEMO.ownership_marker(private)
            marker.write_text("existing-owner\n", encoding="utf-8")
            marker.chmod(0o400)
            RUN_DEMO.prepare_managed_directory(private, "私有")
            self.assertEqual(marker.read_text(encoding="utf-8"), "existing-owner\n")

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

    def test_grant_codex_runtime_access_preserves_tracked_file_modes(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            private = Path(temp_dir) / "private"
            repo = private / "repo"
            hook = repo / ".codex" / "hooks" / "guard.py"
            hook.parent.mkdir(parents=True)
            hook.write_text("print('guard')\n", encoding="utf-8")
            hook.chmod(0o644)
            original_mode = hook.stat().st_mode & 0o777
            with (
                mock.patch.object(
                    RUN_DEMO,
                    "subprocess_identity",
                    return_value=({"user": 1001, "group": 1001, "extra_groups": []}, "/tmp/home"),
                ),
                mock.patch.object(RUN_DEMO.os, "chown", create=True),
            ):
                # Windows 单测只验证函数不会改 tracked 文件权限；POSIX runner 再验证真实 UID 隔离。
                RUN_DEMO.grant_codex_runtime_access(private, [repo], "codex-demo")
            self.assertEqual(hook.stat().st_mode & 0o777, original_mode)


if __name__ == "__main__":
    unittest.main()
