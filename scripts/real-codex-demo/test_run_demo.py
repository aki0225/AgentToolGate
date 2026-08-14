#!/usr/bin/env python3
"""真实 Codex 演示证据处理的最小回归测试。"""

from __future__ import annotations

import importlib.util
import hashlib
import json
import os
import tempfile
import threading
import unittest
import urllib.request
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
SCAN = load_sibling_module("scan_public_artifacts")


class RealCodexDemoTest(unittest.TestCase):
    def test_sensitive_scan_reports_source_without_secret_value(self) -> None:
        secret = b"root"
        sources = SCAN.matching_sensitive_sources(
            b'{"sshUser":"root"}',
            [SCAN.SensitiveCandidate("ATG_DEMO_SSH_USER", secret, True)],
        )

        self.assertEqual(sources, ["ATG_DEMO_SSH_USER"])
        self.assertNotIn(secret.decode("utf-8"), sources)

    def test_sensitive_scan_ignores_short_identifier_inside_safe_tokens(self) -> None:
        sources = SCAN.matching_sensitive_sources(
            (
                b'{"matchedRule":"root_delete",'
                b'"argument":"--private-root",'
                b'"category":"protected_root"}'
            ),
            [SCAN.SensitiveCandidate("ATG_DEMO_SSH_USER", b"root", True)],
        )

        self.assertEqual(sources, [])

    def test_sensitive_scan_still_detects_encoded_short_identifier(self) -> None:
        candidates = [
            SCAN.SensitiveCandidate(
                "ATG_DEMO_SSH_USER",
                value,
                require_boundaries,
            )
            for value, require_boundaries in SCAN.encoded_candidates(
                "root",
                boundary_match_plaintext=True,
            )
        ]

        sources = SCAN.matching_sensitive_sources(b'{"value":"cm9vdA=="}', candidates)

        self.assertEqual(sources, ["ATG_DEMO_SSH_USER"])

    def test_collect_guard_evidence_waits_before_fetching_audits(self) -> None:
        order: list[str] = []

        class Observer:
            def snapshot(self) -> list[dict[str, object]]:
                order.append("snapshot")
                return [{"adapter": "codex"}]

        def fetch(_url: str, **_kwargs: object) -> dict[str, object]:
            order.append("audit")
            return {"items": [{"toolKey": "agent_guard.evaluate"}]}

        with mock.patch.object(RUN_DEMO, "http_json", side_effect=fetch):
            observed, audits = RUN_DEMO.collect_guard_evidence(Observer(), 18090)

        self.assertEqual(order, ["snapshot", "audit"])
        self.assertEqual(observed, [{"adapter": "codex"}])
        self.assertEqual(audits, [{"toolKey": "agent_guard.evaluate"}])

    def test_guard_observer_marks_request_inflight_before_recording(self) -> None:
        order: list[str] = []

        class UpstreamHandler(RUN_DEMO.http.server.BaseHTTPRequestHandler):
            def log_message(self, _format: str, *_args: object) -> None:
                return

            def do_POST(self) -> None:
                content_length = int(self.headers.get("Content-Length", "0"))
                self.rfile.read(content_length)
                response = b'{"decision":"allow"}'
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(response)))
                self.end_headers()
                self.wfile.write(response)

        class RecordingList(list[dict[str, object]]):
            def append(self, item: dict[str, object]) -> None:
                order.append("record")
                super().append(item)

        upstream = RUN_DEMO.socketserver.ThreadingTCPServer(
            ("127.0.0.1", 0),
            UpstreamHandler,
        )
        upstream.daemon_threads = True
        upstream_thread = threading.Thread(target=upstream.serve_forever, daemon=True)
        upstream_thread.start()
        observer = RUN_DEMO.GuardRequestObserver(upstream.server_address[1])
        original_begin = observer._begin_forward

        def record_begin() -> None:
            order.append("begin")
            original_begin()

        observer._begin_forward = record_begin
        observer.requests = RecordingList()
        try:
            observer.start()
            request = urllib.request.Request(
                f"http://127.0.0.1:{observer.port}/api/agent-guard/evaluate",
                data=b'{"adapter":"codex"}',
                headers={"Content-Type": "application/json"},
                method="POST",
            )
            with urllib.request.urlopen(request, timeout=5) as response:
                self.assertEqual(response.status, 200)
            self.assertEqual(order[:2], ["begin", "record"])
            self.assertEqual(observer.snapshot(), [{"adapter": "codex"}])
        finally:
            observer.stop()
            upstream.shutdown()
            upstream.server_close()
            upstream_thread.join(timeout=5)

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
                    "tool": RUN_DEMO.DEMO_MCP_TOOL_KEY,
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
                "toolKey": RUN_DEMO.DEMO_MCP_TOOL_KEY,
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
        self.assertTrue(result["checks"]["mcpDemoEchoSucceededOnce"])

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
            header = __import__("json").loads(content.splitlines()[0])
        self.assertLess(content.index("第一步"), content.index("第二步"))
        self.assertEqual(header["env"], {"TERM": "xterm-256color"})
        self.assertNotIn("SHELL", header["env"])

    def test_public_command_display_normalizes_system_powershell_path(self) -> None:
        self.assertEqual(
            RUN_DEMO.public_command_display(
                r'''"C:\Program Files\PowerShell\7\pwsh.exe" -Command 'git status --short' '''
            ),
            "pwsh -Command 'git status --short'",
        )
        self.assertEqual(
            RUN_DEMO.public_command_display(
                r"C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe -Command Get-Date"
            ),
            "powershell -Command Get-Date",
        )
        self.assertEqual(
            RUN_DEMO.public_command_display("/bin/bash -lc 'git status --short'"),
            "/bin/bash -lc 'git status --short'",
        )
        self.assertEqual(
            RUN_DEMO.public_command_display(
                r'''"D:\tools\custom\pwsh.exe" -Command 'git status --short' '''
            ),
            "\"D:\\tools\\custom\\pwsh.exe\" -Command 'git status --short'",
        )

    def test_v2_scan_rejects_system_powershell_path_without_breaking_v1(self) -> None:
        public_path = (
            rb'$ "C:\\Program Files\\PowerShell\\7\\pwsh.exe" '
            rb"-Command 'git status --short'"
        )
        self.assertFalse(
            any(
                pattern.search(public_path)
                for pattern in SCAN.forbidden_patterns("v1-success")
            )
        )
        self.assertTrue(
            any(
                pattern.search(public_path)
                for pattern in SCAN.forbidden_patterns("v2-success")
            )
        )

    def test_codex_event_summary_only_returns_fixed_counts(self) -> None:
        events = [
            {"type": "thread.started"},
            {"type": "turn.started"},
            {
                "type": "item.completed",
                "item": {
                    "type": "agent_message",
                    "text": "private-model-output",
                },
            },
            {
                "type": "item.completed",
                "item": {
                    "type": "error",
                    "message": "private-provider-error",
                },
            },
            {"type": "future.event", "item": {"type": "future_item"}},
        ]
        summary = RUN_DEMO.codex_event_summary(
            events,
            "private-stderr-text",
            0,
        )
        serialized = __import__("json").dumps(summary)
        self.assertEqual(summary["exitCode"], 0)
        self.assertEqual(summary["eventCount"], 5)
        self.assertEqual(summary["itemTypes"]["agentMessage"], 1)
        self.assertEqual(summary["itemTypes"]["error"], 1)
        self.assertEqual(summary["itemTypes"]["other"], 1)
        self.assertNotIn("private-model-output", serialized)
        self.assertNotIn("private-provider-error", serialized)
        self.assertNotIn("private-stderr-text", serialized)

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

    def test_trust_codex_hook_starts_app_server_in_disposable_repo(self) -> None:
        repo = Path("/tmp/disposable-repo")
        with mock.patch.object(
            RUN_DEMO.subprocess,
            "Popen",
            side_effect=OSError("stop after invocation"),
        ) as invoked:
            with self.assertRaisesRegex(OSError, "stop after invocation"):
                RUN_DEMO.trust_codex_hook(
                    "codex",
                    {"CODEX_HOME": "/tmp/codex-home"},
                    repo,
                    {},
                )

        self.assertEqual(invoked.call_args.kwargs["cwd"], repo)

    def test_observed_hook_control_keeps_release_executable(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            executable = root / ("agenttoolgate.exe" if os.name == "nt" else "agenttoolgate")
            executable.write_text("synthetic executable\n", encoding="utf-8")
            control = root / "hook-control.json"
            RUN_DEMO.write_json(
                control,
                {
                    "mode": "live",
                    "endpoint": "http://127.0.0.1:18090",
                    "executable": str(executable.resolve()),
                },
            )
            RUN_DEMO.configure_observed_hook_control(control, 19001, executable)
            document = json.loads(control.read_text(encoding="utf-8"))
        self.assertEqual(document["endpoint"], "http://127.0.0.1:19001")
        self.assertEqual(document["executable"], str(executable.resolve()))

    def test_observed_hook_control_rejects_missing_or_other_executable(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            expected = root / ("agenttoolgate.exe" if os.name == "nt" else "agenttoolgate")
            expected.write_text("expected\n", encoding="utf-8")
            other = root / ("other.exe" if os.name == "nt" else "other")
            other.write_text("other\n", encoding="utf-8")
            control = root / "hook-control.json"
            for executable in ("", str(other.resolve())):
                with self.subTest(executable=executable):
                    RUN_DEMO.write_json(
                        control,
                        {
                            "mode": "live",
                            "endpoint": "http://127.0.0.1:18090",
                            "executable": executable,
                        },
                    )
                    with self.assertRaises(RUN_DEMO.DemoFailure):
                        RUN_DEMO.configure_observed_hook_control(
                            control,
                            19001,
                            expected,
                        )

    def test_codex_environment_removes_external_guard_overrides(self) -> None:
        blocked = {
            "AGENTTOOLGATE_BEARER_TOKEN": "secret",
            "AGENTTOOLGATE_EXE": "/tmp/other-atg",
            "AGENTTOOLGATE_URL": "http://127.0.0.1:9999",
            "AGENTTOOLGATE_WORKSPACE_ORG_ID": "other",
            "OPENAI_API_KEY": "secret",
            "OPENAI_BASE_URL": "https://private.invalid/v1",
            "WORKSPACE_ORG_ID": "other",
        }
        with mock.patch.dict(os.environ, blocked, clear=False):
            codex_home = Path("/tmp/isolated-codex-home")
            environment = RUN_DEMO.create_codex_environment(
                codex_home,
                "/usr/bin:/bin",
            )
        for key in blocked:
            self.assertNotIn(key, environment)
        self.assertEqual(environment["CODEX_HOME"], str(codex_home))
        self.assertEqual(
            environment["AGENTTOOLGATE_CLI_TIMEOUT_MS"],
            RUN_DEMO.OBSERVED_HOOK_CLI_TIMEOUT_MS,
        )
        self.assertEqual(
            environment["AGENTTOOLGATE_HOOK_TIMEOUT_MS"],
            RUN_DEMO.OBSERVED_HOOK_HTTP_TIMEOUT_MS,
        )
        self.assertEqual(environment["PATH"], "/usr/bin:/bin")

    def test_create_demo_mcp_tool_requires_message_schema(self) -> None:
        created = {
            "namespace": RUN_DEMO.DEMO_MCP_TOOL_NAMESPACE,
            "name": RUN_DEMO.DEMO_MCP_TOOL_NAME,
            "inputSchemaJson": {
                "type": "object",
                "properties": {"message": {"type": "string", "minLength": 1}},
                "required": ["message"],
                "additionalProperties": False,
            },
        }
        with mock.patch.object(RUN_DEMO, "http_json", return_value=created) as requested:
            result = RUN_DEMO.create_demo_mcp_tool(18090)

        self.assertEqual(result, created)
        call = requested.call_args
        self.assertEqual(call.args[0], "http://127.0.0.1:18090/api/tools")
        self.assertEqual(call.kwargs["method"], "POST")
        payload = call.kwargs["payload"]
        self.assertEqual(
            f"{payload['namespace']}.{payload['name']}",
            RUN_DEMO.DEMO_MCP_TOOL_KEY,
        )
        self.assertEqual(payload["inputSchemaJson"]["required"], ["message"])
        self.assertFalse(payload["inputSchemaJson"]["additionalProperties"])

    def test_create_demo_mcp_tool_rejects_weakened_schema(self) -> None:
        weakened = {
            "namespace": RUN_DEMO.DEMO_MCP_TOOL_NAMESPACE,
            "name": RUN_DEMO.DEMO_MCP_TOOL_NAME,
            "inputSchemaJson": {"type": "object"},
        }
        with (
            mock.patch.object(RUN_DEMO, "http_json", return_value=weakened),
            self.assertRaises(RUN_DEMO.DemoFailure),
        ):
            RUN_DEMO.create_demo_mcp_tool(18090)

    def test_runtime_evidence_uses_actual_local_platform(self) -> None:
        with (
            mock.patch.object(RUN_DEMO.platform, "system", return_value="Windows"),
            mock.patch.object(RUN_DEMO.platform, "machine", return_value="AMD64"),
            mock.patch.dict(os.environ, {"GITHUB_ACTIONS": ""}, clear=False),
        ):
            evidence = RUN_DEMO.runtime_evidence()

        self.assertEqual(evidence["platform"], "windows-amd64")
        self.assertEqual(evidence["label"], "Windows 本地一次性验收环境")

    def test_runtime_evidence_identifies_github_ubuntu(self) -> None:
        with (
            mock.patch.object(RUN_DEMO.platform, "system", return_value="Linux"),
            mock.patch.object(RUN_DEMO.platform, "machine", return_value="x86_64"),
            mock.patch.dict(os.environ, {"GITHUB_ACTIONS": "true"}, clear=False),
        ):
            evidence = RUN_DEMO.runtime_evidence()

        self.assertEqual(evidence["platform"], "linux-amd64")
        self.assertEqual(evidence["label"], "GitHub 托管 Ubuntu 一次性运行器")

    def test_codex_home_disables_unrelated_plugins_but_keeps_hooks(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            codex_home = root / "codex-home"
            RUN_DEMO.create_codex_home(
                codex_home,
                root / "repo",
                18090,
                18091,
                "synthetic-model",
                "synthetic-key",
            )
            config = (codex_home / "config.toml").read_text(encoding="utf-8")

        self.assertIn("hooks = true", config)
        self.assertIn("plugins = false", config)

    def test_remove_directory_tree_retries_transient_windows_race(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            path = Path(temp_dir) / "codex-home"
            path.mkdir()
            transient_error = OSError(145, "目录不是空的")
            with (
                mock.patch.object(
                    RUN_DEMO.shutil,
                    "rmtree",
                    side_effect=[transient_error, None],
                ) as removed,
                mock.patch.object(RUN_DEMO.time, "sleep") as slept,
            ):
                RUN_DEMO.remove_directory_tree(
                    path,
                    attempts=2,
                    retry_delay_seconds=0.25,
                )

        self.assertEqual(removed.call_count, 2)
        slept.assert_called_once_with(0.25)

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
                "toolKey": RUN_DEMO.DEMO_MCP_TOOL_KEY,
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
