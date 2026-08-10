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
    def write_project_protection(self, repo: Path, body: dict[str, Any] | str) -> None:
        config_path = repo / ".agenttoolgate" / "protected.json"
        config_path.parent.mkdir(parents=True, exist_ok=True)
        if isinstance(body, str):
            config_path.write_text(body, encoding="utf-8")
        else:
            config_path.write_text(json.dumps(body, ensure_ascii=False), encoding="utf-8")

    def test_default_hook_timeout_balances_local_latency(self) -> None:
        original = os.environ.pop("AGENTTOOLGATE_HOOK_TIMEOUT_MS", None)
        try:
            for module in (HOOK, CODEX_HOOK):
                with self.subTest(module=module.__name__):
                    self.assertEqual(module.hook_timeout_seconds(), 1.0)
                    self.assertEqual(module.go_cli_timeout_seconds(), 1.5)
        finally:
            if original is not None:
                os.environ["AGENTTOOLGATE_HOOK_TIMEOUT_MS"] = original

    def test_python_floor_allows_remembered_approval(self) -> None:
        request = {"guardDecision": "ask"}
        decision = {
            "decision": "allow",
            "approvalId": "approval-remembered",
            "approvalStatus": "approved",
            "fingerprint": "fingerprint-remembered",
        }

        self.assertEqual(HOOK.enforce_python_guard_floor(request, decision)["decision"], "allow")

    def test_python_floor_rejects_unbacked_allow(self) -> None:
        request = {"guardDecision": "ask"}
        decision = {"decision": "allow"}

        self.assertEqual(HOOK.enforce_python_guard_floor(request, decision)["decision"], "deny")

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

    def test_exec_target_prefers_sensitive_shell_write_target(self) -> None:
        dangerous = "mkdir -p .ssh && printf synthetic-stage5-only > .ssh/id_rsa"
        ordinary = "printf '.ssh is documented here' > docs/security-note.txt"
        powershell = "'synthetic-stage5-only' | Out-File -FilePath .ssh/authorized_keys"

        for module in (HOOK, CODEX_HOOK):
            with self.subTest(module=module.__name__, command="dangerous"):
                payload = module.build_agent_guard_request(
                    module.detect_adapter(),
                    {"cwd": os.getcwd()},
                    "Bash",
                    {"command": dangerous},
                )
                self.assertEqual(payload["target"], ".ssh/id_rsa")
                self.assertTrue(module.is_high_risk_offline_target(payload))

            with self.subTest(module=module.__name__, command="ordinary"):
                payload = module.build_agent_guard_request(
                    module.detect_adapter(),
                    {"cwd": os.getcwd()},
                    "Bash",
                    {"command": ordinary},
                )
                self.assertEqual(payload["target"], "docs/security-note.txt")
                self.assertFalse(module.is_high_risk_offline_target(payload))

            with self.subTest(module=module.__name__, command="powershell"):
                payload = module.build_agent_guard_request(
                    module.detect_adapter(),
                    {"cwd": os.getcwd()},
                    "PowerShell",
                    {"command": powershell},
                )
                self.assertEqual(payload["target"], ".ssh/authorized_keys")
                self.assertTrue(module.is_high_risk_offline_target(payload))

    def test_offline_exec_fallback_only_allows_exact_commands(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            for module in (HOOK, CODEX_HOOK):
                def eligible(command: str) -> bool:
                    return module.is_explicitly_low_risk_offline_action(
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
                    with self.subTest(module=module.__name__, command=command):
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
                    with self.subTest(module=module.__name__, command=command):
                        self.assertFalse(eligible(command))

    def test_agenttoolgate_mcp_tools_do_not_enter_local_guard_twice(self) -> None:
        for module in (HOOK, CODEX_HOOK):
            with self.subTest(module=module.__name__):
                self.assertFalse(module.is_guarded_tool("mcp__agenttoolgate__mock_echo"))
                self.assertTrue(module.is_guarded_tool("mcp__external_server__write_file"))
                self.assertTrue(module.is_guarded_tool("Read"))
                self.assertTrue(module.is_guarded_tool("Grep"))
                self.assertTrue(module.is_guarded_tool("Glob"))
                self.assertTrue(module.is_guarded_tool("shell_command"))
                self.assertTrue(module.is_guarded_tool("sh"))

    def test_shell_command_and_sh_searches_skip_hidden_script_detection(self) -> None:
        for module in (HOOK, CODEX_HOOK):
            for tool_name in ("shell_command", "sh"):
                with self.subTest(module=module.__name__, tool=tool_name):
                    payload = {
                        "tool": tool_name,
                        "actionType": "exec",
                        "target": "docs",
                        "content": 'rg "executionpolicy bypass" docs',
                    }
                    self.assertFalse(module.is_high_risk_offline_target(payload))

    def test_claude_tool_input_supports_go_adapter_aliases(self) -> None:
        for key in ("args", "arguments", "params", "input"):
            with self.subTest(key=key):
                tool_input = HOOK.get_tool_input({key: {"file_path": "src/client.pem"}})
                self.assertEqual(tool_input["file_path"], "src/client.pem")

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
            for module in (HOOK, CODEX_HOOK):
                for tool_name, tool_input, expected_target, high_risk in cases:
                    with self.subTest(module=module.__name__, tool=tool_name, tool_input=tool_input):
                        payload = module.build_agent_guard_request(
                            module.detect_adapter(),
                            {"cwd": str(repo)},
                            tool_name,
                            tool_input,
                            str(repo),
                        )
                        self.assertEqual(payload["actionType"], "read")
                        self.assertEqual(payload["target"].replace("\\", "/"), expected_target)
                        self.assertEqual(module.is_high_risk_offline_target(payload), high_risk)
                        self.assertEqual(
                            module.is_explicitly_low_risk_offline_action(str(repo), payload),
                            not high_risk or module.is_project_metadata_read_target(payload["target"]),
                        )

    def test_http_request_fallback_maps_network_and_enforces_project_egress(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            self.write_project_protection(
                repo,
                {
                    "version": 1,
                    "localActionFirewall": {
                        "enabled": True,
                        "egress": {
                            "enabled": True,
                            "allowedHosts": ["api.github.com"],
                            "unlistedWrite": "deny",
                        },
                    },
                },
            )
            for module in (HOOK, CODEX_HOOK):
                with self.subTest(module=module.__name__):
                    self.assertTrue(module.is_guarded_tool("http.request"))
                    self.assertTrue(module.is_guarded_tool("network.request"))
                    payload = module.build_agent_guard_request(
                        module.detect_adapter(),
                        {"cwd": str(repo)},
                        "http.request",
                        {
                            "method": "POST",
                            "url": "https://uploads.example.test/data",
                            "body": {"message": "synthetic"},
                        },
                        str(repo),
                    )
                    self.assertEqual(payload["actionType"], "network")
                    self.assertEqual(payload["target"], "https://uploads.example.test/data")
                    self.assertEqual(payload["networkMethod"], "POST")
                    self.assertEqual(payload["networkUrl"], "https://uploads.example.test/data")
                    floor = module.project_protection_floor(str(repo), payload)
                    self.assertIsNotNone(floor)
                    self.assertEqual(floor["decision"], "deny")
                    read_payload = module.build_agent_guard_request(
                        module.detect_adapter(),
                        {"cwd": str(repo)},
                        "http.request",
                        {"method": "GET", "url": "https://uploads.example.test/data"},
                        str(repo),
                    )
                    self.assertIsNone(module.project_protection_floor(str(repo), read_payload))

    def test_project_protection_merges_patch_targets_and_recognizes_shell_delete(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            self.write_project_protection(
                repo,
                {
                    "version": 1,
                    "localActionFirewall": {
                        "enabled": True,
                        "protectedPaths": [
                            {"pattern": "src/core/**", "write": "deny"},
                            {"pattern": "deploy/production/**", "delete": "deny"},
                        ],
                        "egress": {"enabled": False},
                    },
                },
            )
            patch_payload = {
                "tool": "apply_patch",
                "actionType": "read",
                "target": "src/ui.go",
                "targets": ["src/ui.go"],
                "content": "*** Begin Patch\n*** Update File: src/ui.go\n*** Update File: src/core/algorithm.go\n*** End Patch\n",
                "workingDirectory": str(repo),
            }
            delete_payload = {
                "tool": "PowerShell",
                "actionType": "exec",
                "target": "deploy/production/app.yaml",
                "content": "Remove-Item deploy/production/app.yaml",
                "workingDirectory": str(repo),
            }
            write_payload = {
                "tool": "PowerShell",
                "actionType": "exec",
                "target": "src/core/algorithm.go",
                "content": "Set-Content src/core/algorithm.go changed",
                "workingDirectory": str(repo),
            }
            for module in (HOOK, CODEX_HOOK):
                with self.subTest(module=module.__name__, action="patch"):
                    floor = module.project_protection_floor(str(repo), patch_payload)
                    self.assertIsNotNone(floor)
                    self.assertEqual(floor["decision"], "deny")
                with self.subTest(module=module.__name__, action="delete"):
                    floor = module.project_protection_floor(str(repo), delete_payload)
                    self.assertIsNotNone(floor)
                    self.assertEqual(floor["decision"], "deny")
                with self.subTest(module=module.__name__, action="write"):
                    floor = module.project_protection_floor(str(repo), write_payload)
                    self.assertIsNotNone(floor)
                    self.assertEqual(floor["decision"], "deny")

    def test_project_protection_extracts_real_shell_read_targets(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            self.write_project_protection(
                repo,
                {
                    "version": 1,
                    "localActionFirewall": {
                        "enabled": True,
                        "protectedPaths": [
                            {"pattern": "src/protected/**", "read": "deny"}
                        ],
                        "egress": {"enabled": False},
                    },
                },
            )
            cases = (
                ("PowerShell", "Get-Content src/protected/config.json"),
                ("Bash", "cat src/protected/config.json"),
                ("PowerShell", "type src/protected/config.json"),
                ("Bash", "more src/protected/config.json"),
                ("Bash", "head -n 5 src/protected/config.json"),
                ("Bash", "tail --lines 5 src/protected/config.json"),
                ("Bash", "rg -n TODO src/protected"),
                ("Bash", "grep -r TODO src/protected"),
            )
            for module in (HOOK, CODEX_HOOK):
                for tool_name, command in cases:
                    with self.subTest(module=module.__name__, command=command):
                        tool_input = {"command": command}
                        payload = module.build_agent_guard_request(
                            module.detect_adapter(),
                            {"cwd": str(repo)},
                            tool_name,
                            tool_input,
                            str(repo),
                        )
                        self.assertNotIn("target", tool_input)
                        floor = module.project_protection_floor(str(repo), payload)
                        self.assertIsNotNone(floor)
                        self.assertEqual(floor["decision"], "deny")

    def test_project_protection_keeps_strictest_compound_shell_operation(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            self.write_project_protection(
                repo,
                {
                    "version": 1,
                    "localActionFirewall": {
                        "enabled": True,
                        "protectedPaths": [
                            {"pattern": "src/core/first.txt", "write": "require_approval"},
                            {"pattern": "src/core/second.txt", "write": "deny"},
                            {"pattern": "deploy/production/first.yaml", "delete": "require_approval"},
                            {"pattern": "deploy/production/second.yaml", "delete": "deny"},
                        ],
                        "egress": {"enabled": False},
                    },
                },
            )
            commands = (
                "Set-Content src/core/first.txt one && Set-Content src/core/second.txt two",
                "Set-Content src/core/first.txt one; Remove-Item deploy/production/second.yaml",
                "Remove-Item deploy/production/first.yaml; Remove-Item deploy/production/second.yaml",
            )
            for module in (HOOK, CODEX_HOOK):
                for command in commands:
                    with self.subTest(module=module.__name__, command=command):
                        tool_input = {"command": command}
                        payload = module.build_agent_guard_request(
                            module.detect_adapter(),
                            {"cwd": str(repo)},
                            "PowerShell",
                            tool_input,
                            str(repo),
                        )
                        self.assertNotIn("target", tool_input)
                        floor = module.project_protection_floor(str(repo), payload)
                        self.assertIsNotNone(floor)
                        self.assertEqual(floor["decision"], "deny")

    def test_project_protection_does_not_treat_search_syntax_as_targets(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            self.write_project_protection(
                repo,
                {
                    "version": 1,
                    "localActionFirewall": {
                        "enabled": True,
                        "protectedPaths": [
                            {"pattern": "TODO", "read": "deny"},
                            {"pattern": "--hidden", "read": "deny"},
                            {"pattern": "src/protected/**", "read": "deny"},
                        ],
                        "egress": {"enabled": False},
                    },
                },
            )
            commands = (
                "rg TODO docs",
                "rg --hidden TODO docs",
                "rg --glob '*.py' TODO docs",
                "rg --mystery TODO src/protected",
                "unknown-reader src/protected/config.json",
            )
            for module in (HOOK, CODEX_HOOK):
                for command in commands:
                    with self.subTest(module=module.__name__, command=command):
                        payload = module.build_agent_guard_request(
                            module.detect_adapter(),
                            {"cwd": str(repo)},
                            "Bash",
                            {"command": command},
                            str(repo),
                        )
                        self.assertIsNone(module.project_protection_floor(str(repo), payload))

    def test_project_protection_uses_delete_rule_for_patch_delete(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            cases = (
                ("add uses write", "*** Add File: deploy/production/app.yaml", {"write": "deny"}, "deny"),
                ("add does not use delete", "*** Add File: deploy/production/app.yaml", {"delete": "deny"}, None),
                ("update uses write", "*** Update File: deploy/production/app.yaml", {"write": "deny"}, "deny"),
                ("update does not use delete", "*** Update File: deploy/production/app.yaml", {"delete": "deny"}, None),
                ("move uses write", "*** Move to: deploy/production/app.yaml", {"write": "deny"}, "deny"),
                ("move does not use delete", "*** Move to: deploy/production/app.yaml", {"delete": "deny"}, None),
                ("delete uses delete", "*** Delete File: deploy/production/app.yaml", {"delete": "deny"}, "deny"),
                ("delete does not use write", "*** Delete File: deploy/production/app.yaml", {"write": "deny"}, None),
            )
            for name, line, effect, expected in cases:
                self.write_project_protection(
                    repo,
                    {
                        "version": 1,
                        "localActionFirewall": {
                            "enabled": True,
                            "protectedPaths": [
                                {"pattern": "deploy/production/**", **effect}
                            ],
                            "egress": {"enabled": False},
                        },
                    },
                )
                payload = {
                    "tool": "apply_patch",
                    "actionType": "write",
                    "target": "deploy/production/app.yaml",
                    "targets": ["deploy/production/app.yaml"],
                    "content": f"*** Begin Patch\n{line}\n*** End Patch\n",
                    "workingDirectory": str(repo),
                }
                for module in (HOOK, CODEX_HOOK):
                    with self.subTest(name=name, module=module.__name__):
                        floor = module.project_protection_floor(str(repo), payload)
                        if expected is None:
                            self.assertIsNone(floor)
                        else:
                            self.assertIsNotNone(floor)
                            self.assertEqual(floor["decision"], expected)

    def test_project_protection_uses_delete_rule_for_patch_move_source(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            patch = (
                "*** Begin Patch\n"
                "*** Update File: src/core/algorithm.py\n"
                "*** Move to: docs/algorithm.py\n"
                "*** End Patch\n"
            )
            for effect, expected in (({"delete": "deny"}, "deny"), ({"write": "deny"}, None)):
                self.write_project_protection(
                    repo,
                    {
                        "version": 1,
                        "localActionFirewall": {
                            "enabled": True,
                            "protectedPaths": [{"pattern": "src/core/**", **effect}],
                            "egress": {"enabled": False},
                        },
                    },
                )
                payload = {
                    "tool": "apply_patch",
                    "actionType": "write",
                    "content": patch,
                    "workingDirectory": str(repo),
                }
                for module in (HOOK, CODEX_HOOK):
                    with self.subTest(module=module.__name__, effect=effect):
                        floor = module.project_protection_floor(str(repo), payload)
                        if expected is None:
                            self.assertIsNone(floor)
                        else:
                            self.assertIsNotNone(floor)
                            self.assertEqual(floor["decision"], expected)

    def test_project_protection_keeps_strictest_mixed_patch_operation(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            self.write_project_protection(
                repo,
                {
                    "version": 1,
                    "localActionFirewall": {
                        "enabled": True,
                        "protectedPaths": [
                            {"pattern": "src/core/**", "write": "require_approval"},
                            {"pattern": "deploy/production/**", "delete": "deny"},
                        ],
                        "egress": {"enabled": False},
                    },
                },
            )
            for lines in (
                (
                    "*** Update File: src/core/algorithm.go\n"
                    "*** Delete File: deploy/production/app.yaml"
                ),
                (
                    "*** Delete File: deploy/production/app.yaml\n"
                    "*** Update File: src/core/algorithm.go"
                ),
            ):
                payload = {
                    "tool": "apply_patch",
                    "actionType": "write",
                    "targets": ["src/core/algorithm.go", "deploy/production/app.yaml"],
                    "content": f"*** Begin Patch\n{lines}\n*** End Patch\n",
                    "workingDirectory": str(repo),
                }
                for module in (HOOK, CODEX_HOOK):
                    with self.subTest(module=module.__name__, lines=lines):
                        floor = module.project_protection_floor(str(repo), payload)
                        self.assertIsNotNone(floor)
                        self.assertEqual(floor["decision"], "deny")

    def test_powershell_positional_write_targets_are_extracted(self) -> None:
        cases = [
            ("Set-Content '.ssh/id_rsa' 'synthetic'", ".ssh/id_rsa"),
            ("New-Item '.git/hooks/pre-commit' -ItemType File", ".git/hooks/pre-commit"),
            ("Copy-Item 'safe.txt' '.ssh/authorized_keys'", ".ssh/authorized_keys"),
        ]
        for module in (HOOK, CODEX_HOOK):
            for command, expected in cases:
                with self.subTest(module=module.__name__, command=command):
                    payload = module.build_agent_guard_request(
                        module.detect_adapter(),
                        {"cwd": os.getcwd()},
                        "PowerShell",
                        {"command": command},
                    )
                    self.assertEqual(payload["target"], expected)
                    self.assertTrue(module.is_high_risk_offline_target(payload))

    def test_project_protection_maps_extended_shell_operations(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            self.write_project_protection(
                repo,
                {
                    "version": 1,
                    "localActionFirewall": {
                        "enabled": True,
                        "protectedPaths": [
                            {
                                "pattern": "src/core/**",
                                "read": "deny",
                                "write": "deny",
                                "delete": "deny",
                                "exec": "deny",
                            }
                        ],
                        "egress": {"enabled": False},
                    },
                },
            )
            commands = (
                "echo ok\nrm src/core/algorithm.py",
                "Select-String -Pattern TODO -Path src/core/algorithm.py",
                "Select-String TODO src/core/algorithm.py",
                "Select-String -Pattern TODO src/core/algorithm.py",
                "sls TODO src/core/algorithm.py",
                "echo generated > src/core/generated.py",
                "echo generated >> src/core/generated.py",
                "echo generated >| src/core/generated.py",
                "echo generated | tee src/core/generated.py",
                "Get-Content input.txt | Tee-Object -FilePath src/core/generated.py",
                "truncate -s 0 src/core/generated.py",
                "python src/core/tool.py",
                "py -3 bootstrap.py src/core/tool.py",
                "python3.12 bootstrap.py src/core/tool.py",
                "./src/core/tool.sh",
                "mv src/core/algorithm.py docs/algorithm.py",
            )
            for module in (HOOK, CODEX_HOOK):
                for command in commands:
                    with self.subTest(module=module.__name__, command=command):
                        payload = module.build_agent_guard_request(
                            module.detect_adapter(),
                            {"cwd": str(repo)},
                            "Bash",
                            {"command": command},
                            str(repo),
                        )
                        floor = module.project_protection_floor(str(repo), payload)
                        self.assertIsNotNone(floor)
                        self.assertEqual(floor["decision"], "deny")
                for tool_name in ("shell_command", "sh"):
                    with self.subTest(module=module.__name__, tool_name=tool_name):
                        payload = module.build_agent_guard_request(
                            module.detect_adapter(),
                            {"cwd": str(repo)},
                            tool_name,
                            {"command": "cat src/core/algorithm.py"},
                            str(repo),
                        )
                        self.assertEqual(payload["actionType"], "exec")
                        floor = module.project_protection_floor(str(repo), payload)
                        self.assertIsNotNone(floor)
                        self.assertEqual(floor["decision"], "deny")

    def test_project_protection_accepts_static_absolute_glob_and_path_lists(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            protected = repo / "src" / "protected" / "config.json"
            self.write_project_protection(
                repo,
                {
                    "version": 1,
                    "localActionFirewall": {
                        "enabled": True,
                        "protectedPaths": [{"pattern": "src/protected/**", "read": "deny"}],
                        "egress": {"enabled": False},
                    },
                },
            )
            commands = (
                f'Get-Content "{protected}"',
                "Get-Content docs/readme.md,src/protected/config.json",
                "Get-Content src/protected/*.json",
            )
            for module in (HOOK, CODEX_HOOK):
                for command in commands:
                    with self.subTest(module=module.__name__, command=command):
                        payload = {
                            "tool": "powershell",
                            "actionType": "exec",
                            "content": command,
                            "workingDirectory": str(repo),
                        }
                        floor = module.project_protection_floor(str(repo), payload)
                        self.assertIsNotNone(floor)
                        self.assertEqual(floor["decision"], "deny")

    def test_project_protection_ignores_quoted_redirection(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            self.write_project_protection(
                repo,
                {
                    "version": 1,
                    "localActionFirewall": {
                        "enabled": True,
                        "protectedPaths": [{"pattern": "src/core/**", "write": "deny"}],
                        "egress": {"enabled": False},
                    },
                },
            )
            payload = {
                "tool": "shell",
                "actionType": "exec",
                "content": 'echo "> src/core/generated.py"',
                "workingDirectory": str(repo),
            }
            for module in (HOOK, CODEX_HOOK):
                with self.subTest(module=module.__name__):
                    self.assertIsNone(module.project_protection_floor(str(repo), payload))
                    escaped_payload = dict(payload, content=r"echo \> src/core/generated.py")
                    self.assertIsNone(module.project_protection_floor(str(repo), escaped_payload))

    def test_project_protection_keeps_commands_after_escaped_quotes(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            self.write_project_protection(
                repo,
                {
                    "version": 1,
                    "localActionFirewall": {
                        "enabled": True,
                        "protectedPaths": [{"pattern": "src/core/**", "delete": "deny"}],
                        "egress": {"enabled": False},
                    },
                },
            )
            commands = (
                r'printf "x\""; rm src/core/algorithm.py',
                r'printf "x\\"; rm src/core/algorithm.py',
                'Write-Output "x`""; Remove-Item src/core/algorithm.py',
                'Write-Output "x``"; Remove-Item src/core/algorithm.py',
            )
            for module in (HOOK, CODEX_HOOK):
                for command in commands:
                    with self.subTest(module=module.__name__, command=command):
                        payload = {
                            "tool": "shell",
                            "actionType": "exec",
                            "content": command,
                            "workingDirectory": str(repo),
                        }
                        floor = module.project_protection_floor(str(repo), payload)
                        self.assertIsNotNone(floor)
                        self.assertEqual(floor["decision"], "deny")

    def test_project_protection_classifies_copy_move_arguments(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            cases = (
                ("Copy-Item -Destination deploy/production/app.py -Path docs/app.py", "deploy/production/**", "write", True),
                ("Copy-Item -Destination docs/app.py -Path src/core/app.py", "src/core/**", "read", True),
                ("Move-Item -Destination docs/app.py -Path src/core/app.py", "src/core/**", "delete", True),
                ("Move-Item -Path docs/app.py -Destination deploy/production/app.py", "deploy/production/**", "write", True),
                ("cp -Destination deploy/production/app.py -Path docs/app.py", "deploy/production/**", "write", True),
                ("mv -Destination docs/app.py -Path src/core/app.py", "src/core/**", "delete", True),
                ("Rename-Item -NewName replacement.py -Path src/core/algorithm.py", "src/core/replacement.py", "write", True),
                ("truncate --reference src/core/reference.py docs/output.py", "src/core/reference.py", "read", True),
                ("truncate -rsrc/core/reference.py docs/output.py", "src/core/reference.py", "read", True),
                ("truncate --reference src/core/reference.py docs/output.py", "src/core/reference.py", "write", False),
            )
            for module in (HOOK, CODEX_HOOK):
                for command, pattern, operation, expected in cases:
                    with self.subTest(module=module.__name__, command=command, operation=operation):
                        self.write_project_protection(
                            repo,
                            {
                                "version": 1,
                                "localActionFirewall": {
                                    "enabled": True,
                                    "protectedPaths": [{"pattern": pattern, operation: "deny"}],
                                    "egress": {"enabled": False},
                                },
                            },
                        )
                        payload = {
                            "tool": "shell",
                            "actionType": "exec",
                            "content": command,
                            "workingDirectory": str(repo),
                        }
                        floor = module.project_protection_floor(str(repo), payload)
                        self.assertEqual(floor is not None, expected)

    def test_project_protection_covers_network_output_files(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            self.write_project_protection(
                repo,
                {
                    "version": 1,
                    "localActionFirewall": {
                        "enabled": True,
                        "protectedPaths": [{"pattern": "deploy/production/**", "write": "deny"}],
                        "egress": {"enabled": False},
                    },
                },
            )
            commands = (
                "curl -o deploy/production/app.yaml https://api.github.com/data",
                "curl -so deploy/production/app.yaml https://api.github.com/data",
                "curl -c deploy/production/cookies.txt https://api.github.com/data",
                "curl -Ddeploy/production/headers.txt https://api.github.com/data",
                "Invoke-WebRequest -Uri https://api.github.com/data -OutF deploy/production/app.yaml",
                "iwr https://api.github.com/data -OutFile:deploy/production/app.yaml",
            )
            for module in (HOOK, CODEX_HOOK):
                for command in commands:
                    with self.subTest(module=module.__name__, command=command):
                        payload = {
                            "tool": "shell",
                            "actionType": "exec",
                            "content": command,
                            "workingDirectory": str(repo),
                        }
                        floor = module.project_protection_floor(str(repo), payload)
                        self.assertIsNotNone(floor)
                        self.assertEqual(floor["decision"], "deny")

    def test_project_protection_covers_static_network_input_files(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            self.write_project_protection(
                repo,
                {
                    "version": 1,
                    "localActionFirewall": {
                        "enabled": True,
                        "protectedPaths": [{"pattern": "src/core/**", "read": "deny"}],
                        "egress": {
                            "enabled": True,
                            "allowedHosts": ["api.github.com"],
                            "unlistedWrite": "deny",
                        },
                    },
                },
            )
            commands = (
                "curl -T src/core/archive.bin https://api.github.com/upload",
                "curl --upload-file=src/core/archive.bin https://api.github.com/upload",
                "curl -d @src/core/payload.json https://api.github.com/upload",
                "curl --data-binary @src/core/payload.json https://api.github.com/upload",
                "curl -F file=@src/core/payload.json https://api.github.com/upload",
                "Invoke-RestMethod -InFile src/core/payload.json -Method Post -Uri https://api.github.com/upload",
                "curl -d @- https://api.github.com/upload < src/core/payload.json",
            )
            for module in (HOOK, CODEX_HOOK):
                for command in commands:
                    with self.subTest(module=module.__name__, command=command):
                        payload = {
                            "tool": "shell",
                            "actionType": "exec",
                            "content": command,
                            "workingDirectory": str(repo),
                        }
                        floor = module.project_protection_floor(str(repo), payload)
                        self.assertIsNotNone(floor)
                        self.assertEqual(floor["decision"], "deny")
                        self.assertEqual(floor["reason"], "project protected path")

    def test_project_protection_covers_static_shell_egress_writes(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            self.write_project_protection(
                repo,
                {
                    "version": 1,
                    "localActionFirewall": {
                        "enabled": True,
                        "protectedPaths": [],
                        "egress": {
                            "enabled": True,
                            "allowedHosts": ["api.github.com"],
                            "unlistedWrite": "deny",
                        },
                    },
                },
            )
            writes = (
                "curl -X POST https://uploads.example.test/data",
                "curl -iXPOST https://uploads.example.test/data",
                "curl --data synthetic https://uploads.example.test/data",
                "curl --data-ascii synthetic https://uploads.example.test/data",
                "curl --form-string message=synthetic https://uploads.example.test/data",
                "curl --json '{\"message\":\"synthetic\"}' https://uploads.example.test/data",
                "curl -dsynthetic -X GET https://uploads.example.test/data",
                "curl -sdsynthetic https://uploads.example.test/data",
                "curl -sFmessage=synthetic https://uploads.example.test/data",
                "curl -sTpayload.txt https://uploads.example.test/data",
                "curl -d synthetic uploads.example.test/data",
                "curl -i -X POST https://uploads.example.test/data",
                "Invoke-RestMethod -Method Post -Uri https://uploads.example.test/data",
                "Invoke-RestMethod -Me Post -Uri https://uploads.example.test/data",
                "Invoke-RestMethod -Method:Get -Body:synthetic -Uri:https://uploads.example.test/data",
                "Invoke-RestMethod https://uploads.example.test/data -Body synthetic",
                "powershell -Command Invoke-RestMethod -Method Post -Uri https://uploads.example.test/data",
            )
            allowed = (
                "curl https://uploads.example.test/data",
                "curl -I https://uploads.example.test/data",
                "curl -i https://uploads.example.test/data",
                "curl -f https://uploads.example.test/data",
                "curl -x https://proxy.example.test https://uploads.example.test/data",
                "curl -m 5 https://uploads.example.test/data",
                "curl --connect-timeout 5 https://uploads.example.test/data",
                'curl -H "Referer: https://docs.example.test" https://uploads.example.test/data',
                "curl -d synthetic https://api.github.com/data --next https://uploads.example.test/data",
                "Invoke-RestMethod -Method Get -Uri https://uploads.example.test/data",
                "curl -X POST https://api.github.com/repos/example/project/issues",
                "curl -m 5 -d synthetic https://api.github.com/data",
                "curl --connect-timeout 5 -d synthetic https://api.github.com/data",
            )
            for module in (HOOK, CODEX_HOOK):
                for command in writes:
                    with self.subTest(module=module.__name__, command=command):
                        payload = {
                            "tool": "shell",
                            "actionType": "exec",
                            "content": command,
                            "workingDirectory": str(repo),
                        }
                        floor = module.project_protection_floor(str(repo), payload)
                        self.assertIsNotNone(floor)
                        self.assertEqual(floor["decision"], "deny")
                for command in allowed:
                    with self.subTest(module=module.__name__, command=command):
                        payload = {
                            "tool": "shell",
                            "actionType": "exec",
                            "content": command,
                            "workingDirectory": str(repo),
                        }
                        self.assertIsNone(module.project_protection_floor(str(repo), payload))

    def test_request_includes_workspace_root_and_ticket_digest_binds_it(self) -> None:
        for module in (HOOK, CODEX_HOOK):
            with self.subTest(module=module.__name__):
                first = module.build_agent_guard_request(
                    module.detect_adapter(),
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
                self.assertNotEqual(module.hook_request_digest(first), module.hook_request_digest(second))
                self.assertNotEqual(module.hook_request_digest(first), module.hook_request_digest(third))

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
                {
                    "tool_name": "Read",
                    "tool_input": {"file_path": "README.md"},
                    "cwd": str(repo),
                },
                go_cli=go_cli,
                post_json=lambda *_args, **_kwargs: self.fail("普通仓库读取不应调用后端"),
            )
            self.assertEqual(raw, "")
            self.assertEqual(len(captured), 1)
            self.assertEqual(captured[0]["tool_input"]["file_path"], "README.md")

    def test_live_alias_sensitive_read_delegates_to_go_cli(self) -> None:
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
                {"tool_name": "Read", "args": {"file_path": "src/client.pem"}, "cwd": str(repo)},
                go_cli=go_cli,
                post_json=lambda *_args, **_kwargs: self.fail("Go CLI 成功时不应调用后端"),
            )
            self.assertEqual(output, expected)
            self.assertEqual(len(captured), 1)
            self.assertEqual(captured[0]["args"]["file_path"], "src/client.pem")

    def test_dry_run_ordinary_repo_read_remains_low_noise(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            (repo / "README.md").write_text("hello", encoding="utf-8")
            self.set_hook_control(repo, "dry-run")
            raw = self.invoke_raw(
                {
                    "tool_name": "Read",
                    "tool_input": {"file_path": "README.md"},
                    "cwd": str(repo),
                },
                go_cli=lambda _payload: self.fail("dry-run 普通读取不应调用 Go CLI"),
                post_json=lambda *_args, **_kwargs: self.fail("dry-run 普通读取不应调用后端"),
                enable_live=False,
            )
            self.assertEqual(raw, "")
            self.assertFalse((repo / ".tmp" / "agenttoolgate" / "hook-dry-run.jsonl").exists())

    def test_dry_run_repo_read_fast_path_requires_explicit_local_read_tool(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            for module in (HOOK, CODEX_HOOK):
                local_read = module.build_agent_guard_request(
                    module.detect_adapter(),
                    {"cwd": str(repo)},
                    "Read",
                    {"file_path": "README.md"},
                    str(repo),
                )
                with self.subTest(module=module.__name__, tool="Read"):
                    self.assertTrue(module.is_fast_path_repo_read(str(repo), local_read))
                    self.assertTrue(module.is_explicitly_low_risk_offline_action(str(repo), local_read))

                for tool_name, tool_input in (
                    ("WebSearch", {"query": "AgentToolGate"}),
                    ("mcp__github__merge_pull_request", {"repository_full_name": "example/repo", "pr_number": 1}),
                ):
                    with self.subTest(module=module.__name__, tool=tool_name):
                        payload = module.build_agent_guard_request(
                            module.detect_adapter(),
                            {"cwd": str(repo)},
                            tool_name,
                            tool_input,
                            str(repo),
                        )
                        self.assertFalse(module.is_fast_path_repo_read(str(repo), payload))
                        self.assertFalse(module.is_explicitly_low_risk_offline_action(str(repo), payload))

    def test_high_risk_target_uses_path_segments_not_bare_substrings(self) -> None:
        allowed_targets = [
            "examples/agent-demo/evidence/windows-startup-poisoning-output.txt",
            "docs/credentials-review-notes.md",
            "tmp/secrets-report.md",
            "notes/taskschd-analysis.txt",
            "internal/secrets/parser.go",
            "docs/credentials/guide.md",
            "examples/startup/readme.md",
            ".agenttoolgate/clients/claude-hook.json",
            ".tmp/agenttoolgate/hook-dry-run.jsonl",
            r"C:\Windows\System32\taskschd\job.txt",
            "cmd/server/main.go",
        ]
        for target in allowed_targets:
            with self.subTest(target=target):
                self.assertFalse(HOOK.is_probably_high_risk_target(target))

        denied_targets = [
            r"C:\Users\me\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup\run.ps1",
            ".ssh/authorized_keys",
            ".git/hooks/pre-commit",
            ".env",
            ".env.production",
            ".npmrc",
            ".agenttoolgate/config.json",
            ".tmp/agenttoolgate/hook-control.json",
            ".claude/settings.local.json",
            ".codex/config.toml",
            ".agents/skills/reviewer.md",
            ".github/workflows/ci.yml",
            "AGENTS.md",
            "package.json",
            "src/client.pem",
            r"C:\Users\me\AppData\Local\BraveSoftware\Brave-Browser\User Data\Default\Login Data",
            r"C:\Users\me\AppData\Local\Vivaldi\User Data\Default\Cookies",
            r"C:\Users\me\AppData\Roaming\Opera Software\Opera Stable\History",
            r"HKCU:\Software\Microsoft\Windows\CurrentVersion\Run\Updater",
            r"C:\Windows\System32\Config\SAM",
            r"C:\Windows\System32\Tasks\Updater",
        ]
        for target in denied_targets:
            with self.subTest(target=target):
                self.assertTrue(HOOK.is_probably_high_risk_target(target))

    def test_agenttoolgate_control_directories_are_mutation_sensitive(self) -> None:
        read_targets = (
            ".agenttoolgate/clients/claude-hook.json",
            ".tmp/agenttoolgate/hook-dry-run.jsonl",
        )
        mutation_payloads = (
            {
                "tool": "Write",
                "actionType": "write",
                "target": ".agenttoolgate/clients/claude-hook.json",
                "content": "{}",
            },
            {
                "tool": "shell",
                "actionType": "exec",
                "target": ".tmp/agenttoolgate",
                "content": "rm -rf .tmp/agenttoolgate",
            },
            {
                "tool": "shell",
                "actionType": "exec",
                "target": ".agenttoolgate",
                "content": "mv .agenttoolgate docs/agenttoolgate-backup",
            },
        )
        for module in (HOOK, CODEX_HOOK):
            for target in read_targets:
                with self.subTest(module=module.__name__, target=target, operation="read"):
                    payload = {"tool": "Read", "actionType": "read", "target": target, "content": ""}
                    self.assertFalse(module.is_high_risk_offline_target(payload))
            for command in (
                "cat .agenttoolgate/clients/claude-hook.json",
                "Get-Content .tmp/agenttoolgate/hook-dry-run.jsonl",
            ):
                with self.subTest(module=module.__name__, command=command, operation="shell-read"):
                    payload = {"tool": "shell", "actionType": "exec", "target": command, "content": command}
                    self.assertFalse(module.is_high_risk_offline_target(payload))
            for payload in mutation_payloads:
                with self.subTest(module=module.__name__, payload=payload):
                    self.assertTrue(module.is_high_risk_offline_target(payload))
            for target in (".agenttoolgate/config.json", ".tmp/agenttoolgate/hook-control.json"):
                with self.subTest(module=module.__name__, target=target, operation="strict-read"):
                    payload = {"tool": "Read", "actionType": "read", "target": target, "content": ""}
                    self.assertTrue(module.is_high_risk_offline_target(payload))

    def test_claude_and_codex_share_offline_risk_core(self) -> None:
        targets = [
            "examples/agent-demo/evidence/windows-startup-poisoning-output.txt",
            "docs/credentials-review-notes.md",
            ".agenttoolgate/config.json",
            ".agenttoolgate/protected.json",
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
                    {"command": patch},
                )
                self.assertEqual(payload["actionType"], "write")
                self.assertIn("Startup", payload["target"])
                self.assertEqual(
                    payload["targets"],
                    ["Users/aki/AppData/Roaming/Microsoft/Windows/Start Menu/Programs/Startup/run.ps1"],
                )
                self.assertTrue(module.is_high_risk_offline_target(payload))

    def test_project_protection_floor_is_shared_by_claude_and_codex(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            self.write_project_protection(
                repo,
                {
                    "version": 1,
                    "localActionFirewall": {
                        "enabled": True,
                        "protectedPaths": [
                            {"pattern": "deploy/production/**", "write": "deny"}
                        ],
                        "egress": {"enabled": False},
                    },
                },
            )
            payload = {
                "tool": "apply_patch",
                "actionType": "write",
                "target": "src/ui.go;deploy/production/app.yaml",
                "targets": ["src/ui.go", "deploy/production/app.yaml"],
                "content": "",
                "workingDirectory": str(repo),
            }
            for module in (HOOK, CODEX_HOOK):
                with self.subTest(module=module.__name__):
                    floor = module.attach_python_guard_floor(str(repo), payload)
                    self.assertEqual(floor["guardDecision"], "deny")
                    self.assertEqual(floor["guardRiskLevel"], "high")

    def test_project_protection_keeps_read_payload_text_read_only(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            self.write_project_protection(
                repo,
                {
                    "version": 1,
                    "localActionFirewall": {
                        "enabled": True,
                        "protectedPaths": [
                            {
                                "pattern": "src/core/**",
                                "read": "require_approval",
                                "write": "deny",
                                "delete": "deny",
                            }
                        ],
                        "egress": {"enabled": False},
                    },
                },
            )
            payloads = [
                {
                    "tool": "Grep",
                    "actionType": "read",
                    "target": "docs",
                    "content": "*** Begin Patch\n*** Update File: src/core/algorithm.go\n*** End Patch",
                    "workingDirectory": str(repo),
                },
                {
                    "tool": "Read",
                    "actionType": "read",
                    "target": "docs/commands.md",
                    "content": "This guide explains how rm removes a file.",
                    "workingDirectory": str(repo),
                },
                {
                    "tool": "Grep",
                    "actionType": "read",
                    "target": "docs",
                    "content": "rm src/core/algorithm.go",
                    "workingDirectory": str(repo),
                },
                {
                    "tool": "Write",
                    "actionType": "write",
                    "target": "docs/commands.md",
                    "content": "rm src/core/algorithm.go",
                    "workingDirectory": str(repo),
                },
            ]
            for payload in payloads:
                with self.subTest(payload=payload):
                    for module in (HOOK, CODEX_HOOK):
                        with self.subTest(module=module.__name__):
                            self.assertIsNone(module.project_protection_floor(str(repo), payload))

    def test_project_protection_recognizes_wrapped_delete_commands_without_prose_false_positive(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            self.write_project_protection(
                repo,
                {
                    "version": 1,
                    "localActionFirewall": {
                        "enabled": True,
                        "protectedPaths": [
                            {"pattern": "deploy/production/**", "delete": "deny"}
                        ],
                        "egress": {"enabled": False},
                    },
                },
            )
            for command in (
                "sudo rm deploy/production/app.yaml",
                "cmd /c del deploy/production/app.yaml",
                "powershell -Command Remove-Item deploy/production/app.yaml",
                "echo ok; rm deploy/production/app.yaml",
            ):
                with self.subTest(command=command):
                    floor = HOOK.project_protection_floor(
                        str(repo),
                        {
                            "tool": "shell",
                            "actionType": "exec",
                            "content": command,
                            "workingDirectory": str(repo),
                        },
                    )
                    self.assertIsNotNone(floor)
                    self.assertEqual(floor["decision"], "deny")
            self.assertIsNone(
                HOOK.project_protection_floor(
                    str(repo),
                    {
                        "tool": "shell",
                        "actionType": "exec",
                        "content": "the rm command deletes a file",
                        "workingDirectory": str(repo),
                    },
                )
            )
            self.assertIsNone(
                HOOK.project_protection_floor(
                    str(repo),
                    {
                        "tool": "shell",
                        "actionType": "exec",
                        "content": 'echo "ok; rm deploy/production/app.yaml"',
                        "workingDirectory": str(repo),
                    },
                )
            )

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

    def test_invalid_control_file_fails_closed(self) -> None:
        for control in (
            "{bad json",
            '{"mode":"preview"}',
            '{"mode":"live","mode":"off"}',
            '{"mode":"live","unknown":true}',
            '{"mode":true}',
            '[{"mode":"live"}]',
        ):
            with self.subTest(control=control), tempfile.TemporaryDirectory() as temp_dir:
                repo = Path(temp_dir)
                (repo / ".git").mkdir()
                self.set_hook_control(repo, raw=control)
                raw = self.invoke_raw(
                    {"tool_name": "Write", "tool_input": {"path": ".env", "content": "TOKEN=x"}, "cwd": str(repo)},
                    post_json=lambda *_args, **_kwargs: self.fail("损坏控制文件时不应调用后端"),
                    enable_live=False,
                )
                decision = json.loads(raw)["hookSpecificOutput"]
                self.assertEqual(decision["permissionDecision"], "deny")
                self.assertIn("hook control invalid", decision["permissionDecisionReason"])

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
            self.assertEqual(preview["decisionPreview"], "deny")
            self.assertNotIn("#!/bin/sh", json.dumps(preview, ensure_ascii=False))

    def test_dry_run_previews_project_code_execution_as_approval(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            self.set_hook_control(repo, "dry-run")
            raw = self.invoke_raw(
                {"tool_name": "Bash", "tool_input": {"command": "go test ./..."}, "cwd": str(repo)},
                post_json=lambda *_args, **_kwargs: self.fail("dry-run 模式不应调用后端"),
                enable_live=False,
            )
            self.assertEqual(raw, "")
            preview_path = repo / ".tmp" / "agenttoolgate" / "hook-dry-run.jsonl"
            preview = json.loads(preview_path.read_text(encoding="utf-8").strip())
            self.assertEqual(preview["decisionPreview"], "ask")
            self.assertEqual(preview["riskLevel"], "medium")
            self.assertIn("project_code_execution", preview["signals"])

    def test_dry_run_previews_project_rule_as_approval(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = Path(temp_dir)
            (repo / ".git").mkdir()
            self.set_hook_control(repo, "dry-run")
            self.write_project_protection(
                repo,
                {
                    "version": 1,
                    "localActionFirewall": {
                        "enabled": True,
                        "protectedPaths": [
                            {
                                "pattern": "src/core/**",
                                "read": "require_approval",
                            }
                        ],
                        "egress": {"enabled": False},
                    },
                },
            )
            raw = self.invoke_raw(
                {"tool_name": "Read", "tool_input": {"file_path": "src/core/algorithm.go"}, "cwd": str(repo)},
                post_json=lambda *_args, **_kwargs: self.fail("dry-run 模式不应调用后端"),
                enable_live=False,
            )
            self.assertEqual(raw, "")
            preview_path = repo / ".tmp" / "agenttoolgate" / "hook-dry-run.jsonl"
            preview = json.loads(preview_path.read_text(encoding="utf-8").strip())
            self.assertEqual(preview["decisionPreview"], "ask")
            self.assertEqual(preview["riskLevel"], "high")
            self.assertIn("project_protection_rule", preview["signals"])

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

    def test_offline_repo_reads_and_keyword_mentions_are_allowed_with_pending_audit(self) -> None:
        cases = [
            ("Read", {"file_path": "internal/secrets/parser.go"}),
            ("Read", {"file_path": "docs/credentials/guide.md"}),
            ("Read", {"file_path": "AGENTS.md"}),
            ("Read", {"file_path": "package.json"}),
            ("Read", {"file_path": ".claude/settings.local.json"}),
            ("Read", {"file_path": ".codex/config.toml"}),
            ("Grep", {"pattern": "timeout", "path": ".github/workflows"}),
            ("Glob", {"pattern": ".agents/**/*.md"}),
            ("Grep", {"pattern": "WindowStyle Hidden", "path": "docs"}),
            ("Glob", {"pattern": "src/**/*.py"}),
        ]
        for tool_name, tool_input in cases:
            with self.subTest(tool=tool_name, tool_input=tool_input):
                decision, repo = self.run_offline_hook(tool_name, tool_input)
                self.assertEqual(decision["permissionDecision"], "allow")
                audit_path = repo / ".tmp" / "local-action-firewall" / "pending-audit.jsonl"
                self.assertTrue(audit_path.is_file(), "离线 allow 必须留下 pending audit")

    def test_offline_exec_requires_go_cli_except_exact_commands(self) -> None:
        for command in ("git status", "pwd", "Get-Location"):
            with self.subTest(command=command):
                decision, repo = self.run_offline_hook("bash", {"command": command})
                self.assertEqual(decision["permissionDecision"], "allow")
                self.assertTrue((repo / ".tmp" / "local-action-firewall" / "pending-audit.jsonl").is_file())

        for command in (
            'rg "WindowStyle Hidden" docs',
            "rg --hidden TODO .",
            "grep -r TODO .",
            r"git diff -O..\outside",
            r"Select-String -LiteralP ..\outside.txt TODO",
            "Get-Content README.md -Wai",
            r'rg foo . x\"; touch owned #"',
        ):
            with self.subTest(command=command):
                decision, repo = self.run_offline_hook("bash", {"command": command})
                self.assertEqual(decision["permissionDecision"], "deny")
                self.assertFalse((repo / ".tmp" / "local-action-firewall" / "pending-audit.jsonl").exists())

    def test_offline_workspace_write_fails_closed_without_go_cli(self) -> None:
        decision, repo = self.run_offline_hook(
            "Write",
            {"path": "report.md", "content": "普通项目说明。"},
        )
        self.assertEqual(decision["permissionDecision"], "deny")
        self.assertFalse((repo / ".tmp" / "local-action-firewall" / "pending-audit.jsonl").exists())

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
            ("Read", {"file_path": "src/client.pem"}),
            ("Grep", {"pattern": ".", "path": "src/client.pem"}),
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
            ("bash", {"command": "mkdir -p .ssh && printf synthetic-stage5-only > .ssh/id_rsa"}),
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
                    output = self.invoke_hook(input_data, post_json=lambda *_args, response=response, **_kwargs: response)
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
            first = self.invoke_hook(input_data, post_json=post_json)
            self.assertEqual(first["hookSpecificOutput"]["permissionDecision"], "ask")
            second = self.invoke_hook(input_data, post_json=post_json)
            self.assertEqual(second["hookSpecificOutput"]["permissionDecision"], "allow")
            self.assertNotIn("ticketId", captured[0])
            self.assertEqual(captured[1]["ticketId"], "approval-test-1")
            ticket_dir = repo / ".tmp" / "agenttoolgate" / "hook-tickets"
            self.assertFalse(any(ticket_dir.glob("*.json")))

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

    def invoke_hook(
        self,
        input_data: dict[str, Any],
        post_json: Any | None = None,
        go_cli: Any | None = None,
    ) -> dict[str, Any]:
        raw = self.invoke_raw(input_data, post_json=post_json, go_cli=go_cli)
        self.assertTrue(raw, "hook should emit a decision")
        return json.loads(raw)

    def invoke_raw(
        self,
        input_data: dict[str, Any],
        post_json: Any | None = None,
        go_cli: Any | None = None,
        enable_live: bool = True,
    ) -> str:
        original_post_json = HOOK.post_json
        original_go_cli = HOOK.call_agenttoolgate_guard_hook_claude
        original_stdin = sys.stdin
        original_stdout = sys.stdout
        old_disable = os.environ.pop("TRELLIS_DISABLE_HOOKS", None)
        old_hooks = os.environ.pop("TRELLIS_HOOKS", None)
        try:
            repo_root = HOOK.find_repo_root(input_data.get("cwd", os.getcwd()))
            if enable_live and repo_root is not None:
                self.set_hook_control(Path(repo_root), "live")
            HOOK.call_agenttoolgate_guard_hook_claude = go_cli or (lambda _payload: None)
            HOOK.post_json = post_json or (lambda *args, **kwargs: (0, {}, "offline"))
            sys.stdin = io.StringIO(json.dumps(input_data, ensure_ascii=False))
            captured = io.StringIO()
            sys.stdout = captured
            self.assertEqual(HOOK.main(), 0)
            raw = captured.getvalue().strip()
            return raw
        finally:
            HOOK.call_agenttoolgate_guard_hook_claude = original_go_cli
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
