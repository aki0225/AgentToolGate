#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Trellis local-action-firewall PreToolUse adapter for Claude Code.

离线兜底只按路径段 / 路径序列识别高危落点，与后端
isAgentGuardSensitiveTarget 保持一致，避免把只读命令或普通文档里的
敏感词子串误判为真实持久化 / 凭据目标。
"""

from __future__ import annotations

import json
import http.client
import os
import re
import sys
import urllib.parse
import ssl
from pathlib import Path
from typing import Any

DEFAULT_HOOK_TIMEOUT_SECONDS = 0.2
MIN_HOOK_TIMEOUT_MS = 50
MAX_HOOK_TIMEOUT_MS = 2000
HOOK_CONTROL_MODES = {"off", "dry-run", "live"}
SENSITIVE_TARGET_KEYS = {
    "access_token",
    "api_key",
    "apikey",
    "auth",
    "cookie",
    "key",
    "password",
    "secret",
    "signature",
    "token",
}

if sys.platform.startswith("win"):
    import io as _io

    for _stream_name in ("stdin", "stdout", "stderr"):
        _stream = getattr(sys, _stream_name, None)
        if _stream is None:
            continue
        if hasattr(_stream, "reconfigure"):
            try:
                _stream.reconfigure(encoding="utf-8", errors="replace")  # type: ignore[union-attr]
            except Exception:
                pass
        elif hasattr(_stream, "detach"):
            try:
                setattr(sys, _stream_name, _io.TextIOWrapper(_stream.detach(), encoding="utf-8", errors="replace"))
            except Exception:
                pass


def find_repo_root(start_path: str) -> str | None:
    current = Path(start_path).resolve()
    while current != current.parent:
        if (current / ".git").exists():
            return str(current)
        current = current.parent
    return None


def detect_adapter() -> str:
    parts = {part.lower() for part in Path(__file__).parts}
    if ".codex" in parts:
        return "codex"
    return "claude"


def get_text(value: Any) -> str:
    if isinstance(value, str):
        return value.strip()
    return ""


def get_tool_name(input_data: dict[str, Any]) -> str:
    for key in ("tool_name", "toolName", "name", "tool"):
        text = get_text(input_data.get(key))
        if text:
            return text

    tool_input = input_data.get("tool_input")
    if isinstance(tool_input, dict):
        for key in ("tool_name", "toolName", "name"):
            text = get_text(tool_input.get(key))
            if text:
                return text
    return ""


def get_tool_input(input_data: dict[str, Any]) -> dict[str, Any]:
    raw = input_data.get("tool_input")
    if isinstance(raw, dict):
        return raw
    if isinstance(input_data.get("toolInput"), dict):
        return input_data["toolInput"]  # type: ignore[index]
    if isinstance(input_data.get("toolArgs"), dict):
        return input_data["toolArgs"]  # type: ignore[index]
    if isinstance(raw, str) and raw.strip():
        return {"content": raw.strip()}
    return {}


def is_guarded_tool(tool_name: str) -> bool:
    normalized = tool_name.lower().strip()
    if normalized in {"task", "agent"}:
        return False
    if normalized.startswith("mcp__"):
        return True
    return normalized in {
        "bash",
        "write",
        "edit",
        "multiedit",
        "notebookedit",
        "webfetch",
        "websearch",
        "shell",
        "command",
        "powershell",
        "pwsh",
        "apply_patch",
    }


def first_non_empty(*values: Any) -> str:
    for value in values:
        text = get_text(value)
        if text:
            return text
    return ""


def infer_exec_target(command: str) -> str:
    command = command.strip()
    if not command:
        return ""
    patterns = [
        r"""(?:^|\s)-(?:file|f)\s+(?:"([^"]+)"|'([^']+)'|([^\s]+))""",
        r"""(?:^|\s)(?:python|python3|pwsh|powershell|bash|sh|node)\s+(?:"([^"]+)"|'([^']+)'|([^\s]+))""",
    ]
    for pattern in patterns:
        match = re.search(pattern, command, flags=re.IGNORECASE)
        if match:
            for group in match.groups():
                text = get_text(group)
                if text:
                    return text
    return command


_HOOK_DIR = str(Path(__file__).resolve().parent)
if _HOOK_DIR not in sys.path:
    sys.path.insert(0, _HOOK_DIR)

try:
    from _guard_core import (
        contains_hidden_script_features,
        is_high_risk_offline_target,
        is_probably_high_risk_target,
        is_probably_script_target,
    )
except ImportError:  # pragma: no cover - 兼容直接复制单文件调试的场景。
    from ._guard_core import (  # type: ignore[no-redef]
        contains_hidden_script_features,
        is_high_risk_offline_target,
        is_probably_high_risk_target,
        is_probably_script_target,
    )


def read_script_file_content(target: str, cwd: str) -> str:
    candidates = []
    if target.strip():
        candidates.append(Path(target))
        if cwd.strip():
            candidates.append(Path(cwd) / target)
    for candidate in candidates:
        try:
            if candidate.is_file():
                return candidate.read_text(encoding="utf-8", errors="replace")
        except Exception:
            continue
    return ""


def extract_patch_targets(patch_text: str) -> str:
    targets: list[str] = []
    for raw_line in patch_text.splitlines():
        line = raw_line.strip()
        for prefix in ("*** Update File: ", "*** Add File: ", "*** Delete File: "):
            if line.startswith(prefix):
                target = line[len(prefix) :].strip()
                if target:
                    targets.append(target)
        if line.startswith("*** Move to: "):
            target = line[len("*** Move to: ") :].strip()
            if target:
                targets.append(target)
    return ";".join(targets)


def build_agent_guard_request(adapter: str, input_data: dict[str, Any], tool_name: str, tool_input: dict[str, Any]) -> dict[str, Any]:
    normalized_name = tool_name.lower().strip()
    action_type = "read"
    target = ""
    content = ""
    cwd = first_non_empty(input_data.get("cwd")) or os.getcwd()

    if normalized_name in {"bash", "shell", "command", "powershell", "pwsh"}:
        action_type = "exec"
        content = first_non_empty(
            tool_input.get("command"),
            tool_input.get("cmd"),
            tool_input.get("script"),
            tool_input.get("content"),
            tool_input.get("text"),
        )
        if not content:
            content = json.dumps(tool_input, ensure_ascii=False, separators=(",", ":"))
        target = infer_exec_target(content)
        if is_probably_script_target(target):
            script_content = read_script_file_content(target, cwd)
            if script_content:
                content = script_content
    elif normalized_name in {"write", "edit", "multiedit", "notebookedit", "apply_patch"}:
        action_type = "write"
        target = first_non_empty(
            tool_input.get("path"),
            tool_input.get("file_path"),
            tool_input.get("filePath"),
            tool_input.get("target"),
        )
        content = first_non_empty(
            tool_input.get("content"),
            tool_input.get("text"),
            tool_input.get("body"),
            tool_input.get("patch"),
        )
        if normalized_name == "apply_patch":
            content = content or first_non_empty(tool_input.get("input"), tool_input.get("diff"))
            target = target or extract_patch_targets(content)
        if not content:
            content = json.dumps(tool_input, ensure_ascii=False, separators=(",", ":"))
    elif normalized_name.startswith("mcp__"):
        action_type = "write" if any(word in normalized_name for word in ("create", "write", "update", "patch", "delete", "post")) else "read"
        target = first_non_empty(
            tool_input.get("path"),
            tool_input.get("url"),
            tool_input.get("target"),
            tool_name,
        )
        content = first_non_empty(tool_input.get("content"), tool_input.get("body"), tool_input.get("text"))
        if not content:
            content = json.dumps(tool_input, ensure_ascii=False, separators=(",", ":"))

    if not target:
        target = first_non_empty(
            tool_input.get("path"),
            tool_input.get("file_path"),
            tool_input.get("filePath"),
            tool_input.get("url"),
            tool_name,
        )
    if not content:
        content = first_non_empty(tool_input.get("content"), tool_input.get("text"), tool_input.get("body"))
    if not content:
        content = json.dumps(tool_input, ensure_ascii=False, separators=(",", ":"))

    return {
        "adapter": adapter,
        "tool": tool_name,
        "actionType": action_type,
        "target": target,
        "isScript": is_probably_script_target(target) or is_probably_script_target(content),
        "contentEncoding": "plain",
        "content": content,
    }


def build_url() -> str:
    base = os.environ.get("AGENTTOOLGATE_URL", "http://127.0.0.1:8080").strip().rstrip("/")
    return base + "/api/agent-guard/evaluate"


def repo_local_hook_control_path(repo_root: str) -> Path:
    return Path(repo_root) / ".tmp" / "agenttoolgate" / "hook-control.json"


def repo_local_hook_dry_run_path(repo_root: str) -> Path:
    return Path(repo_root) / ".tmp" / "agenttoolgate" / "hook-dry-run.jsonl"


def read_hook_control_mode(repo_root: str) -> str:
    try:
        raw = repo_local_hook_control_path(repo_root).read_text(encoding="utf-8")
        data = json.loads(raw)
    except Exception:
        return "off"
    if not isinstance(data, dict):
        return "off"
    mode = get_text(data.get("mode")).lower()
    if mode not in HOOK_CONTROL_MODES:
        return "off"
    return mode


def repo_local_pending_audit_path(repo_root: str) -> Path:
    return Path(repo_root) / ".tmp" / "local-action-firewall" / "pending-audit.jsonl"


def hook_preview_signals(payload: dict[str, Any]) -> list[str]:
    target = get_text(payload.get("target"))
    content = get_text(payload.get("content"))
    signals: list[str] = []
    if is_probably_high_risk_target(target) or is_probably_high_risk_target(content):
        signals.append("high-risk target")
    if contains_hidden_script_features(content):
        signals.append("hidden script execution feature")
    if is_high_risk_offline_target(payload) and not signals:
        signals.append("high-risk local action")
    if not signals:
        signals.append("local action preview")
    return signals


def is_sensitive_target_key(key: str) -> bool:
    normalized = key.lower().strip().replace("-", "_")
    if normalized in SENSITIVE_TARGET_KEYS:
        return True
    return (
        "token" in normalized
        or "secret" in normalized
        or "password" in normalized
        or "auth" in normalized
        or "signature" in normalized
        or "cookie" in normalized
        or normalized.endswith("_key")
    )


def encode_query_pair(key: str, value: str) -> str:
    return urllib.parse.quote(key, safe="") + "=" + urllib.parse.quote(value, safe="[]")


def redact_non_url_target(target: str) -> str:
    redacted = re.sub(
        r"(?i)\b(token|access_token|api_key|key|secret|password|auth|signature|cookie)\s*[:=]\s*([^\s&;]+)",
        lambda match: f"{match.group(1)}=[REDACTED]",
        target,
    )
    redacted = re.sub(r"(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+", "Bearer [REDACTED]", redacted)
    return redacted


def redact_preview_target(value: Any) -> str:
    target = get_text(value)
    if not target:
        return ""
    lowered = target.lower()
    if lowered.startswith("http://") or lowered.startswith("https://"):
        try:
            parsed = urllib.parse.urlsplit(target)
            if parsed.scheme.lower() not in {"http", "https"} or not parsed.netloc:
                return "[REDACTED_TARGET]"
            pairs = urllib.parse.parse_qsl(parsed.query, keep_blank_values=True)
            query = "&".join(
                encode_query_pair(key, "[REDACTED]" if is_sensitive_target_key(key) else value)
                for key, value in pairs
            )
            return urllib.parse.urlunsplit((parsed.scheme, parsed.netloc, parsed.path, query, ""))
        except Exception:
            return "[REDACTED_TARGET]"
    return redact_non_url_target(target)


def record_local_hook_dry_run(repo_root: str, payload: dict[str, Any]) -> None:
    try:
        preview_path = repo_local_hook_dry_run_path(repo_root)
        preview_path.parent.mkdir(parents=True, exist_ok=True)
        high_risk = is_high_risk_offline_target(payload)
        workspace = get_text(os.environ.get("AGENTTOOLGATE_WORKSPACE_ORG_ID") or os.environ.get("WORKSPACE_ORG_ID"))
        if not workspace:
            workspace = Path(repo_root).name
        record = {
            "workspace": workspace,
            "actor": get_text(os.environ.get("AGENTTOOLGATE_ACTOR") or os.environ.get("USER") or os.environ.get("USERNAME")),
            "adapter": get_text(payload.get("adapter")),
            "tool": get_text(payload.get("tool")),
            "action": get_text(payload.get("actionType")),
            "target": redact_preview_target(payload.get("target")),
            "mode": "dry-run",
            "riskLevel": "high" if high_risk else "low",
            "decisionPreview": "would_block_in_live" if high_risk else "would_allow_in_live",
            "signals": hook_preview_signals(payload),
            "time": __import__("datetime").datetime.now(__import__("datetime").timezone.utc).isoformat(),
        }
        with preview_path.open("a", encoding="utf-8") as handle:
            handle.write(json.dumps(record, ensure_ascii=False) + "\n")
    except Exception:
        return


def record_local_pending_audit(repo_root: str, payload: dict[str, Any], reason: str, offline: bool) -> None:
    try:
        audit_path = repo_local_pending_audit_path(repo_root)
        audit_path.parent.mkdir(parents=True, exist_ok=True)
        workspace = get_text(os.environ.get("AGENTTOOLGATE_WORKSPACE_ORG_ID") or os.environ.get("WORKSPACE_ORG_ID"))
        if not workspace:
            workspace = Path(repo_root).name
        record = {
            "workspace": workspace,
            "actor": get_text(os.environ.get("AGENTTOOLGATE_ACTOR") or os.environ.get("USER") or os.environ.get("USERNAME")),
            "tool": get_text(payload.get("tool")),
            "action": get_text(payload.get("actionType")),
            "target": get_text(payload.get("target")),
            "time": __import__("datetime").datetime.now(__import__("datetime").timezone.utc).isoformat(),
            "reason": reason,
            "offline": offline,
        }
        with audit_path.open("a", encoding="utf-8") as handle:
            handle.write(json.dumps(record, ensure_ascii=False) + "\n")
    except Exception:
        return


def build_headers() -> dict[str, str]:
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json",
    }
    bearer = os.environ.get("AGENTTOOLGATE_BEARER_TOKEN", "").strip()
    if bearer:
        headers["Authorization"] = f"Bearer {bearer}"
    workspace_org_id = os.environ.get("AGENTTOOLGATE_WORKSPACE_ORG_ID", "").strip() or os.environ.get("WORKSPACE_ORG_ID", "").strip()
    if workspace_org_id:
        headers["X-Workspace-Org-Id"] = workspace_org_id
    return headers


def hook_timeout_seconds() -> float:
    raw = os.environ.get("AGENTTOOLGATE_HOOK_TIMEOUT_MS", "").strip()
    if not raw:
        return DEFAULT_HOOK_TIMEOUT_SECONDS
    try:
        timeout_ms = float(raw)
    except ValueError:
        return DEFAULT_HOOK_TIMEOUT_SECONDS
    if timeout_ms < MIN_HOOK_TIMEOUT_MS or timeout_ms > MAX_HOOK_TIMEOUT_MS:
        return DEFAULT_HOOK_TIMEOUT_SECONDS
    return timeout_ms / 1000.0


def post_json(url: str, payload: dict[str, Any], timeout: float | None = None) -> tuple[int, dict[str, Any], str]:
    if timeout is None:
        timeout = hook_timeout_seconds()
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    try:
        parsed = urllib.parse.urlsplit(url)
        path = parsed.path or "/"
        if parsed.query:
            path = f"{path}?{parsed.query}"
        headers = build_headers()
        if parsed.scheme.lower() == "https":
            conn = http.client.HTTPSConnection(parsed.hostname, parsed.port or 443, timeout=timeout, context=ssl.create_default_context())
        else:
            conn = http.client.HTTPConnection(parsed.hostname, parsed.port or 80, timeout=timeout)
        try:
            conn.request("POST", path, body=body, headers=headers)
            response = conn.getresponse()
            raw = response.read().decode("utf-8", errors="replace")
            try:
                data = json.loads(raw) if raw else {}
            except json.JSONDecodeError:
                data = {}
            return response.status, data if isinstance(data, dict) else {}, raw
        finally:
            conn.close()
    except Exception as exc:
        return 0, {}, str(exc)


def build_output(adapter: str, decision: dict[str, Any], original_input: dict[str, Any]) -> dict[str, Any]:
    result = get_text(decision.get("decision"))
    reason = get_text(decision.get("reason"))
    approval_id = get_text(decision.get("approvalId"))
    approval_status = get_text(decision.get("approvalStatus"))
    call_id = get_text(decision.get("callId"))
    fingerprint = get_text(decision.get("fingerprint"))

    if not reason:
        if result == "allow":
            reason = "allowed"
        elif result == "deny_with_ticket":
            reason = "approval required"
        else:
            reason = "denied"

    if approval_id:
        reason = f"{reason} (ticket: {approval_id})"
    if approval_status and approval_status not in {"pending", "approved", "consumed", "rejected", "expired"}:
        reason = f"{reason} ({approval_status})"
    if fingerprint:
        reason = f"{reason} [fp:{fingerprint}]"
    if call_id:
        reason = f"{reason} [call:{call_id}]"

    if adapter == "claude":
        permission = "allow"
        if result == "deny":
            permission = "deny"
        elif result == "deny_with_ticket":
            permission = "ask"
        return {
            "hookSpecificOutput": {
                "hookEventName": "PreToolUse",
                "permissionDecision": permission,
                "permissionDecisionReason": reason,
                "updatedInput": original_input,
            }
        }

    permission = "allow" if result == "allow" else "deny"
    return {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": permission,
            "permissionDecisionReason": reason,
            "updatedInput": original_input,
        }
    }


def main() -> int:
    if os.environ.get("TRELLIS_HOOKS") == "0" or os.environ.get("TRELLIS_DISABLE_HOOKS") == "1":
        return 0

    try:
        input_data = json.load(sys.stdin)
    except json.JSONDecodeError:
        return 0
    if not isinstance(input_data, dict):
        return 0

    repo_root = find_repo_root(input_data.get("cwd", os.getcwd()))
    if repo_root is None:
        return 0
    hook_mode = read_hook_control_mode(repo_root)
    if hook_mode == "off":
        return 0

    tool_name = get_tool_name(input_data)
    if not tool_name or not is_guarded_tool(tool_name):
        return 0

    tool_input = get_tool_input(input_data)
    adapter = detect_adapter()
    payload = build_agent_guard_request(adapter, input_data, tool_name, tool_input)
    if hook_mode == "dry-run":
        record_local_hook_dry_run(repo_root, payload)
        return 0

    status, decision, raw = post_json(build_url(), payload)
    if status == 0:
        if is_high_risk_offline_target(payload):
            decision = {"decision": "deny", "reason": "ATG offline, sensitive target denied"}
        else:
            decision = {"decision": "allow", "reason": "ATG offline, local pending audit"}
            record_local_pending_audit(repo_root, payload, get_text(decision.get("reason")), True)
    elif not decision:
        decision = {"decision": "deny", "reason": f"agenttoolgate returned empty response (HTTP {status})"}

    output = build_output(adapter, decision, tool_input)
    print(json.dumps(output, ensure_ascii=False), flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
