#!/usr/bin/env python3
"""覆盖五场景编排器的纯本地契约，不调用真实模型。"""

from __future__ import annotations

import json
import socket
import subprocess
import sys
import tempfile
import unittest
import urllib.request
from pathlib import Path


SCRIPT_DIRECTORY = Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPT_DIRECTORY))

import multi_demo  # noqa: E402


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as server:
        server.bind(("127.0.0.1", 0))
        return int(server.getsockname()[1])


class MultiScenarioDemoTest(unittest.TestCase):
    def test_scenario_contract_is_complete_and_ordered(self) -> None:
        specs = multi_demo.scenario_specs(
            "http://127.0.0.1:18092/collect",
            "synthetic_secret=test",
        )

        self.assertEqual([item.id for item in specs], list(multi_demo.SCENARIO_IDS))
        self.assertEqual(specs[0].decision, "allow")
        self.assertEqual(specs[0].risk_level, "low")
        self.assertEqual(specs[2].risk_level, "critical")
        self.assertTrue(all(item.decision == "deny" for item in specs[1:]))
        self.assertEqual(specs[3].guard_signal, "network_exfil")
        self.assertEqual(specs[4].matched_rule, "project_protected_path")

    def test_root_delete_command_matches_published_guard_contract(self) -> None:
        self.assertEqual(
            multi_demo.root_delete_command("windows"),
            "Remove-Item -Recurse .",
        )
        self.assertEqual(multi_demo.root_delete_command("linux"), "rm -rf .")

    def test_prompts_require_real_tools_without_host_credentials(self) -> None:
        prompts = "\n".join(
            item.prompt for item in multi_demo.scenario_specs(
                "http://127.0.0.1:18092/collect",
                "synthetic_secret=test",
            )
        )

        self.assertIn("必须真实", prompts)
        self.assertIn("apply_patch", prompts)
        self.assertIn("collector", prompts)
        self.assertIn("mcp__collector__post_synthetic", prompts)
        self.assertIn("tool_search", prompts)
        self.assertNotIn("OPENAI_API_KEY", prompts)
        self.assertNotIn("Authorization", prompts)
        self.assertNotRegex(prompts, r"[A-Za-z]:\\Users\\")

    def test_extract_session_id_requires_exactly_one_thread(self) -> None:
        events = [{"type": "thread.started", "thread_id": "thread-1"}]

        self.assertEqual(multi_demo.extract_session_id(events), "thread-1")
        with self.assertRaisesRegex(Exception, "唯一 thread_id"):
            multi_demo.extract_session_id([])
        with self.assertRaisesRegex(Exception, "唯一 thread_id"):
            multi_demo.extract_session_id(
                [
                    {"type": "thread.started", "thread_id": "thread-1"},
                    {"type": "thread.started", "thread_id": "thread-2"},
                ]
            )

    def test_new_audits_uses_stable_identity(self) -> None:
        before = [{"id": "old", "toolKey": "agent_guard.evaluate"}]
        after = [
            {"id": "new", "toolKey": "agent_guard.evaluate"},
            {"id": "old", "toolKey": "agent_guard.evaluate"},
        ]

        self.assertEqual(
            [item["id"] for item in multi_demo.new_audits(before, after)],
            ["new"],
        )

    def test_guard_audit_distinguishes_guard_and_effective_risk(self) -> None:
        call = {
            "toolKey": "agent_guard.evaluate",
            "status": "success",
            "policyDecision": "allow",
            "riskLevel": "medium",
            "inputRedactedJson": {
                "guardDecision": "allow",
                "guardRiskLevel": "low",
                "riskLevel": "medium",
            },
            "explanation": {
                "riskLevel": "medium",
                "matchedRule": "agent-guard-safe-workspace-write-allow",
            },
        }

        self.assertTrue(
            multi_demo.is_guard_audit(
                call,
                decision="allow",
                guard_risk_level="low",
                effective_risk_level="medium",
                matched_rule="agent-guard-safe-workspace-write-allow",
            )
        )
        self.assertFalse(
            multi_demo.is_guard_audit(
                call,
                decision="allow",
                guard_risk_level="low",
                effective_risk_level="low",
            )
        )

    def test_command_matches_wrapped_codex_shell_commands(self) -> None:
        self.assertTrue(
            multi_demo.command_matches(
                '"C:\\Program Files\\PowerShell\\7\\pwsh.exe" -Command '
                "'git status --short'",
                "git status --short",
            )
        )

    def test_hook_denial_lines_distinguish_command_and_mcp_tool(self) -> None:
        stderr = "\n".join(
            [
                "Command blocked by PreToolUse hook: synthetic command",
                "Tool call blocked by PreToolUse hook: mcp__collector__post_synthetic",
            ]
        )

        self.assertEqual(
            multi_demo.command_hook_denial_lines(stderr),
            ["Command blocked by PreToolUse hook: synthetic command"],
        )
        self.assertEqual(
            multi_demo.tool_hook_denial_lines(stderr),
            [
                "Tool call blocked by PreToolUse hook: "
                "mcp__collector__post_synthetic"
            ],
        )

    def test_common_validation_only_checks_codex_lifecycle(self) -> None:
        spec = multi_demo.scenario_specs(
            "http://127.0.0.1:18092/collect",
            "synthetic_secret=test",
        )[3]
        session_id, checks = multi_demo.validate_common(
            0,
            [
                {"type": "thread.started", "thread_id": "thread-network"},
                {"type": "turn.started"},
                {"type": "turn.completed"},
            ],
        )

        self.assertEqual(session_id, "thread-network")
        self.assertEqual(
            set(checks),
            {
                "codexExitCodeZero",
                "threadStartedOnce",
                "turnStartedOnce",
                "turnCompletedOnce",
            },
        )
        self.assertTrue(all(checks.values()))
        self.assertEqual(spec.id, "network-egress")

    def test_collector_counts_requests_without_persisting_body(self) -> None:
        collector = multi_demo.EgressCollector(free_port())
        collector.start()
        try:
            request = urllib.request.Request(
                f"http://127.0.0.1:{collector.port}/collect",
                data=b"synthetic_secret=test",
                method="POST",
            )
            with urllib.request.urlopen(request, timeout=5) as response:
                self.assertEqual(response.status, 200)
            self.assertEqual(collector.snapshot(), 1)
            self.assertFalse(hasattr(collector, "request_bodies"))
        finally:
            collector.stop()

    def test_collector_mcp_server_only_marks_real_tool_execution(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            script = root / "collector_mcp.py"
            marker = root / "executed.txt"
            multi_demo.write_collector_mcp_server(
                script,
                marker,
                "http://127.0.0.1:18092/collect",
                "synthetic_secret=test",
            )

            source = script.read_text(encoding="utf-8")
            self.assertIn('"tools/list"', source)
            self.assertIn('"tools/call"', source)
            self.assertIn("synthetic_secret=test", source)
            self.assertFalse(marker.exists())

            messages = [
                {
                    "jsonrpc": "2.0",
                    "id": 1,
                    "method": "initialize",
                    "params": {
                        "protocolVersion": "2025-06-18",
                        "capabilities": {},
                        "clientInfo": {"name": "test", "version": "1"},
                    },
                },
                {
                    "jsonrpc": "2.0",
                    "method": "notifications/initialized",
                    "params": {},
                },
                {
                    "jsonrpc": "2.0",
                    "id": 2,
                    "method": "tools/list",
                    "params": {},
                },
            ]
            result = subprocess.run(
                [multi_demo.collector_python_command(), str(script)],
                input="".join(
                    json.dumps(message, ensure_ascii=True) + "\n"
                    for message in messages
                ).encode("utf-8"),
                capture_output=True,
                timeout=10,
                check=False,
            )

            self.assertEqual(result.returncode, 0)
            responses = [
                json.loads(line)
                for line in result.stdout.decode("utf-8", errors="strict").splitlines()
            ]
            self.assertEqual(responses[0]["result"]["protocolVersion"], "2025-06-18")
            self.assertEqual(
                responses[1]["result"]["tools"][0]["name"],
                multi_demo.COLLECTOR_TOOL_NAME,
            )
            self.assertFalse(marker.exists())

    def test_collector_python_command_is_resolvable(self) -> None:
        command = multi_demo.collector_python_command()
        self.assertTrue(Path(command).is_file())
        result = subprocess.run(
            [command, "--version"],
            capture_output=True,
            text=True,
            timeout=10,
            check=False,
        )
        self.assertEqual(result.returncode, 0)

    def test_collector_mcp_config_is_required_and_narrow(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            codex_home = Path(directory)
            script = codex_home / "collector_mcp.py"
            (codex_home / "config.toml").write_text(
                "[features]\nhooks = true\n",
                encoding="utf-8",
                newline="\n",
            )

            multi_demo.append_collector_mcp_config(
                codex_home,
                script,
                sys.executable,
            )

            config = (codex_home / "config.toml").read_text(encoding="utf-8")
            self.assertIn("[mcp_servers.collector]", config)
            self.assertIn("enabled = true", config)
            self.assertIn("required = true", config)
            self.assertIn("startup_timeout_sec = 15", config)
            self.assertIn('enabled_tools = ["post_synthetic"]', config)

    def test_public_audit_summary_redacts_content(self) -> None:
        spec = multi_demo.scenario_specs(
            "http://127.0.0.1:18092/collect",
            "synthetic_secret=test",
        )[1]
        call = {
            "toolKey": "agent_guard.evaluate",
            "status": "denied",
            "policyDecision": "deny",
            "riskLevel": "high",
            "inputRedactedJson": {
                "adapter": "codex",
                "tool": "shell_command",
                "actionType": "exec",
                "target": multi_demo.SENSITIVE_FILE,
                "content": "[REDACTED]",
            },
            "explanation": {
                "targetCategory": "sensitive",
                "riskLevel": "high",
                "matchedRule": "guard-core-deny-floor",
            },
        }
        document = multi_demo.public_audit_scenario(
            spec,
            {
                "sessionId": "thread-1",
                "audits": [call],
                "observerRequestCount": 1,
                "backendAuditCount": 1,
                "collectorRequestCount": 0,
            },
            {},
        )

        serialized = json.dumps(document, ensure_ascii=False)
        self.assertIn("[REDACTED]", serialized)
        self.assertNotIn("ATG_SYNTHETIC_SSH_SECRET_", serialized)

    def test_scenario_timeline_labels_verifier_derived_lines(self) -> None:
        spec = multi_demo.scenario_specs(
            "http://127.0.0.1:18092/collect",
            "synthetic_secret=test",
        )[4]
        timeline = multi_demo.scenario_timeline(
            [(1.0, "真实 Codex CLI 会话已启动")],
            spec,
            spec.postcondition_summary,
        )

        self.assertEqual(len(timeline), 3)
        self.assertIn("AgentToolGate 验收关联", timeline[-2][1])
        self.assertIn("独立后置条件", timeline[-1][1])

    def test_render_transcript_keeps_section_spacing_without_blank_eof(self) -> None:
        transcript = multi_demo.render_transcript(
            ["标题", "", "## 场景 1", "完成", ""]
        )

        self.assertEqual(transcript, "标题\n\n## 场景 1\n完成\n")
        self.assertFalse(transcript.endswith("\n\n"))

    def test_recording_metadata_matches_written_cast(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            cast_path = Path(directory) / "scenario.cast"
            multi_demo.legacy.write_cast(
                cast_path,
                [(0.1, "真实事件"), (0.3, "独立后置条件")],
            )

            metadata = multi_demo.recording_metadata(cast_path)

            self.assertEqual(metadata["format"], "asciicast-v2")
            self.assertEqual(metadata["eventCount"], 2)
            self.assertEqual(metadata["durationMs"], 300)
            self.assertRegex(metadata["sha256"], r"^[a-f0-9]{64}$")


if __name__ == "__main__":
    unittest.main()
