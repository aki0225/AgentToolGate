#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""AgentToolGate 的 Codex PreToolUse 本地动作防火墙适配器。"""

from __future__ import annotations

import json
import hashlib
import http.client
import os
import re
import subprocess
import sys
import time
import urllib.parse
import ssl
from pathlib import Path
from typing import Any

# 产品 Hook 直接从项目目录加载本地 Core。禁止生成 __pycache__，避免一次
# Codex 调用就在用户仓库中制造未跟踪运行产物。
sys.dont_write_bytecode = True

DEFAULT_HOOK_TIMEOUT_SECONDS = 1.0
DEFAULT_GO_CLI_TIMEOUT_SECONDS = 1.5
MIN_HOOK_TIMEOUT_MS = 50
MAX_HOOK_TIMEOUT_MS = 2000
MIN_GO_CLI_TIMEOUT_MS = 100
MAX_GO_CLI_TIMEOUT_MS = 5000
HOOK_TICKET_TTL_SECONDS = 10 * 60
HOOK_CONTROL_MODES = {"off", "dry-run", "live"}
GO_CLI_UNCERTAIN = object()
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


class HookControlError(ValueError):
    pass

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
    current = Path(get_text(start_path) or os.getcwd()).resolve()
    nearest_marker: str | None = None
    while True:
        has_marker = (current / ".git").exists() or (current / ".agenttoolgate").exists()
        if has_marker:
            if nearest_marker is None:
                nearest_marker = str(current)
            control_path = current / ".tmp" / "agenttoolgate" / "hook-control.json"
            try:
                control_path.stat()
            except FileNotFoundError:
                pass
            except OSError:
                # 无法确认 control 是否存在时保守绑定当前项目，后续读取会 fail closed。
                return str(current)
            else:
                return str(current)
        if current == current.parent:
            return nearest_marker
        current = current.parent


def find_repo_root_safely(start_path: str) -> str | None:
    try:
        return find_repo_root(start_path)
    except (OSError, RuntimeError, ValueError):
        return None


def detect_adapter() -> str:
    parts = {part.lower() for part in Path(__file__).parts}
    if ".claude" in parts:
        return "claude"
    return "codex"


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
	for key in ("args", "arguments", "params", "input"):
		value = input_data.get(key)
		if isinstance(value, dict):
			return value
	if isinstance(raw, str) and raw.strip():
		return {"content": raw.strip()}
	return {}


def is_guarded_tool(tool_name: str) -> bool:
    normalized = tool_name.lower().strip()
    if normalized in {"task", "agent"}:
        return False
    if normalized.startswith("mcp__agenttoolgate__"):
        # ATG MCP 工具已经进入网关的策略、审批和审计链，避免重复治理。
        return False
    if normalized.startswith("mcp__"):
        return True
    return normalized in {
        "bash",
        "glob",
        "grep",
        "read",
        "write",
        "edit",
        "multiedit",
        "notebookedit",
        "webfetch",
        "websearch",
        "shell",
        "shell_command",
        "sh",
        "command",
        "powershell",
        "pwsh",
        "apply_patch",
        "http.request",
        "network.request",
    }


def first_non_empty(*values: Any) -> str:
    for value in values:
        text = get_text(value)
        if text:
            return text
    return ""


def build_glob_read_target(tool_input: dict[str, Any]) -> str:
    base = first_non_empty(
        tool_input.get("path"),
        tool_input.get("file_path"),
        tool_input.get("filePath"),
        tool_input.get("root"),
        tool_input.get("target"),
    )
    pattern = first_non_empty(tool_input.get("pattern"), tool_input.get("glob"))
    if not pattern:
        return base or "."
    if Path(pattern).is_absolute() or not base or base == ".":
        return pattern
    return str(Path(base) / pattern)


def shell_tokens(command: str) -> list[str]:
    tokens: list[str] = []
    pattern = re.compile(r'''"([^"]*)"|'([^']*)'|([^\s;&|]+)|([;&|])''')
    for match in pattern.finditer(command):
        token = first_non_empty(match.group(1), match.group(2), match.group(3), match.group(4))
        if token:
            tokens.append(token)
    return tokens


def extract_powershell_positional_targets(command: str) -> list[str]:
    command_names = {
        "set-content": 0,
        "add-content": 0,
        "out-file": 0,
        "new-item": 0,
        "copy-item": 1,
        "move-item": 1,
        "rename-item": 1,
    }
    target_parameters = {"-path", "-literalpath", "-filepath", "-destination"}
    value_parameters = {
        "-credential",
        "-encoding",
        "-filter",
        "-include",
        "-exclude",
        "-itemtype",
        "-name",
        "-newname",
        "-stream",
        "-type",
        "-value",
    }
    tokens = shell_tokens(command)
    candidates: list[str] = []
    for index, token in enumerate(tokens):
        target_index = command_names.get(token.lower())
        if target_index is None:
            continue
        positionals: list[str] = []
        cursor = index + 1
        while cursor < len(tokens):
            current = tokens[cursor]
            lowered = current.lower()
            if current in {";", "&", "|"}:
                break
            if lowered in target_parameters and cursor + 1 < len(tokens):
                candidates.append(tokens[cursor + 1])
                cursor += 2
                continue
            if current.startswith("-"):
                if lowered in value_parameters and cursor + 1 < len(tokens):
                    cursor += 2
                else:
                    cursor += 1
                continue
            positionals.append(current)
            cursor += 1
        if len(positionals) > target_index:
            candidates.append(positionals[target_index])
    return candidates


def extract_exec_target_candidates(command: str) -> list[str]:
    command = command.strip()
    if not command:
        return []
    patterns = [
        r"""(?:^|\s)-(?:literalpath|filepath|path|file|f)\s+(?:"([^"]+)"|'([^']+)'|([^\s;&|]+))""",
        r"""(?:^|\s)(?:python|python3|pwsh|powershell|bash|sh|node)\s+(?:"([^"]+)"|'([^']+)'|([^\s]+))""",
        r"""(?:^|[\s;&|])(?:\d*>>?|&>)\s*(?:"([^"]+)"|'([^']+)'|([^\s;&|]+))""",
        r"""(?:^|[;&|]\s*|\s)(?:mkdir|touch|tee|truncate)\s+(?:-[^\s]+\s+)*(?:"([^"]+)"|'([^']+)'|([^\s;&|]+))""",
    ]
    candidates: list[str] = []
    for pattern in patterns:
        for match in re.finditer(pattern, command, flags=re.IGNORECASE):
            for group in match.groups():
                text = get_text(group)
                if text and not text.startswith("&"):
                    candidates.append(text.rstrip(",;)"))
                    break
    candidates.extend(extract_powershell_positional_targets(command))
    return candidates


_HOOK_DIR = str(Path(__file__).resolve().parent)
if _HOOK_DIR not in sys.path:
    sys.path.insert(0, _HOOK_DIR)

try:
    from _guard_core import (
        ProjectProtectionError,
        contains_hidden_script_features,
        is_high_risk_offline_target,
        is_project_metadata_read_target,
        is_probably_high_risk_target,
        is_probably_script_target,
        local_guard_preview,
        project_protection_floor,
    )
except ImportError:  # pragma: no cover - 兼容直接复制单文件调试的场景。
    from ._guard_core import (  # type: ignore[no-redef]
        ProjectProtectionError,
        contains_hidden_script_features,
        is_high_risk_offline_target,
        is_project_metadata_read_target,
        is_probably_high_risk_target,
        is_probably_script_target,
        local_guard_preview,
        project_protection_floor,
    )


def infer_exec_target(command: str) -> str:
    candidates = extract_exec_target_candidates(command)
    for candidate in candidates:
        if is_probably_high_risk_target(candidate):
            return candidate
    if candidates:
        return candidates[0]
    return command.strip()


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


def extract_patch_targets(patch_text: str) -> list[str]:
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
    return targets


def build_agent_guard_request(
    adapter: str,
    input_data: dict[str, Any],
    tool_name: str,
    tool_input: dict[str, Any],
    workspace_root: str = "",
) -> dict[str, Any]:
    normalized_name = tool_name.lower().strip()
    action_type = "read"
    target = ""
    targets: list[str] = []
    network_method = ""
    network_url = ""
    content = ""
    cwd = first_non_empty(input_data.get("cwd")) or os.getcwd()

    if normalized_name in {"bash", "shell", "shell_command", "sh", "command", "powershell", "pwsh"}:
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
    elif normalized_name in {"grep", "glob"}:
        action_type = "read"
        pattern = first_non_empty(
            tool_input.get("pattern"),
            tool_input.get("glob"),
            tool_input.get("query"),
            tool_input.get("regex"),
        )
        if normalized_name == "glob":
            target = build_glob_read_target(tool_input)
        else:
            target = first_non_empty(
                tool_input.get("path"),
                tool_input.get("file_path"),
                tool_input.get("filePath"),
                tool_input.get("root"),
                tool_input.get("target"),
                ".",
            )
        content = pattern
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
            content = content or first_non_empty(tool_input.get("command"), tool_input.get("input"), tool_input.get("diff"))
            targets = extract_patch_targets(content)
            target = target or ";".join(targets)
        if not content:
            content = json.dumps(tool_input, ensure_ascii=False, separators=(",", ":"))
    elif normalized_name in {"http.request", "network.request"}:
        action_type = "network"
        network_method = first_non_empty(
            tool_input.get("method"),
            tool_input.get("http_method"),
            tool_input.get("httpMethod"),
        )
        network_url = first_non_empty(
            tool_input.get("url"),
            tool_input.get("uri"),
            tool_input.get("endpoint"),
            tool_input.get("target"),
        )
        target = network_url
        content = first_non_empty(
            tool_input.get("body"),
            tool_input.get("content"),
            tool_input.get("text"),
        )
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
        "targets": targets,
        "networkMethod": network_method,
        "networkUrl": network_url,
        "workspaceRoot": first_non_empty(
            workspace_root,
            input_data.get("project_root"),
            input_data.get("projectRoot"),
            input_data.get("workspace_root"),
            input_data.get("workspaceRoot"),
            input_data.get("cwd"),
        ),
        "workingDirectory": cwd,
        "isScript": is_probably_script_target(target) or is_probably_script_target(content),
        "contentEncoding": "plain",
        "content": content,
    }


def build_url(repo_root: str = "", control_endpoint: str = "") -> str:
    base = os.environ.get("AGENTTOOLGATE_URL", "").strip().rstrip("/")
    if not base and control_endpoint:
        base = normalize_hook_control_endpoint(control_endpoint)
    if not base and repo_root:
        _, base, _ = read_hook_control(repo_root)
    if not base:
        base = "http://127.0.0.1:8080"
    return base + "/api/agent-guard/evaluate"


def repo_local_hook_control_path(repo_root: str) -> Path:
    return Path(repo_root) / ".tmp" / "agenttoolgate" / "hook-control.json"


def repo_local_hook_dry_run_path(repo_root: str) -> Path:
    return Path(repo_root) / ".tmp" / "agenttoolgate" / "hook-dry-run.jsonl"


def repo_local_hook_ticket_dir(repo_root: str) -> Path:
    return Path(repo_root) / ".tmp" / "agenttoolgate" / "hook-tickets"


def validate_hook_control_path(repo_root: str, path: Path) -> None:
    try:
        root = Path(repo_root).resolve(strict=True)
        resolved = path.resolve(strict=False)
        resolved.relative_to(root)
        if path.is_symlink():
            raise HookControlError("hook control invalid")
    except (OSError, RuntimeError, ValueError) as exc:
        raise HookControlError("hook control invalid") from exc


def read_hook_control(repo_root: str) -> tuple[str, str, str]:
    path = repo_local_hook_control_path(repo_root)
    validate_hook_control_path(repo_root, path)
    try:
        raw = path.read_text(encoding="utf-8")
    except FileNotFoundError:
        return "off", "", ""
    except (OSError, UnicodeError) as exc:
        raise HookControlError("hook control invalid") from exc
    try:
        data = json.loads(raw, object_pairs_hook=_reject_hook_control_duplicates)
    except (json.JSONDecodeError, HookControlError, RecursionError) as exc:
        raise HookControlError("hook control invalid") from exc
    if not isinstance(data, dict) or any(
        key not in {"mode", "updatedAt", "reason", "endpoint", "executable"} for key in data
    ):
        raise HookControlError("hook control invalid")
    if not isinstance(data.get("mode"), str):
        raise HookControlError("hook control invalid")
    if any(
        key in data and not isinstance(data[key], str)
        for key in ("updatedAt", "reason", "endpoint", "executable")
    ):
        raise HookControlError("hook control invalid")
    mode = data["mode"].strip().lower()
    if mode not in HOOK_CONTROL_MODES:
        raise HookControlError("hook control invalid")
    return (
        mode,
        normalize_hook_control_endpoint(data.get("endpoint", "")),
        normalize_hook_control_executable(data.get("executable", "")),
    )


def read_hook_control_mode(repo_root: str) -> str:
    return read_hook_control(repo_root)[0]


def normalize_hook_control_endpoint(value: str) -> str:
    endpoint = value.strip().rstrip("/")
    if not endpoint:
        return ""
    try:
        parsed = urllib.parse.urlsplit(endpoint)
        port = parsed.port
    except ValueError as exc:
        raise HookControlError("hook control invalid") from exc
    host = (parsed.hostname or "").lower()
    if (
        parsed.scheme.lower() != "http"
        or parsed.username is not None
        or parsed.password is not None
        or host not in {"127.0.0.1", "localhost", "::1"}
        or port is None
        or port < 1
        or parsed.path not in {"", "/"}
        or parsed.query
        or parsed.fragment
    ):
        raise HookControlError("hook control invalid")
    rendered_host = f"[{host}]" if host == "::1" else host
    return f"http://{rendered_host}:{port}"


def normalize_hook_control_executable(value: str) -> str:
    executable = value.strip()
    if not executable:
        return ""
    try:
        path = Path(executable)
        if not path.is_absolute() or not path.resolve(strict=True).is_file():
            raise HookControlError("hook control invalid")
        return str(path.resolve(strict=True))
    except (OSError, RuntimeError) as exc:
        raise HookControlError("hook control invalid") from exc


def _reject_hook_control_duplicates(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise HookControlError("hook control invalid")
        result[key] = value
    return result


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


def hook_request_digest(payload: dict[str, Any]) -> str:
    raw_targets = payload.get("targets")
    targets = [get_text(item) for item in raw_targets] if isinstance(raw_targets, list) else []
    fields = [
        get_text(payload.get("adapter")),
        get_text(payload.get("tool")),
        get_text(payload.get("actionType")),
        get_text(payload.get("target")),
        targets,
        get_text(payload.get("networkMethod")).upper(),
        get_text(payload.get("networkUrl")),
        get_text(payload.get("workspaceRoot")),
        get_text(payload.get("workingDirectory")),
        get_text(payload.get("guardDecision")),
        get_text(payload.get("guardRiskLevel")),
        bool(payload.get("isScript")),
        get_text(payload.get("contentEncoding")),
        get_text(payload.get("content")),
    ]
    raw = json.dumps(fields, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(raw).hexdigest()


def hook_ticket_path(repo_root: str, payload: dict[str, Any]) -> Path:
    return repo_local_hook_ticket_dir(repo_root) / f"{hook_request_digest(payload)}.json"


def clear_hook_ticket(repo_root: str, payload: dict[str, Any]) -> bool:
    try:
        hook_ticket_path(repo_root, payload).unlink()
    except FileNotFoundError:
        return True
    except Exception:
        return False
    return True


def load_hook_ticket(repo_root: str, payload: dict[str, Any]) -> tuple[str, bool]:
    path = hook_ticket_path(repo_root, payload)
    digest = hook_request_digest(payload)
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
        ticket_id = get_text(data.get("ticketId"))
        fingerprint = get_text(data.get("fingerprint"))
        request_digest = get_text(data.get("requestDigest"))
        expires_at = float(data.get("expiresAtUnix") or 0)
    except FileNotFoundError:
        return "", True
    except Exception:
        return "", clear_hook_ticket(repo_root, payload)
    if not ticket_id or not fingerprint or request_digest != digest or expires_at <= time.time():
        return "", clear_hook_ticket(repo_root, payload)
    return ticket_id, True


def store_hook_ticket(repo_root: str, payload: dict[str, Any], decision: dict[str, Any]) -> bool:
    ticket_id = get_text(decision.get("approvalId"))
    fingerprint = get_text(decision.get("fingerprint"))
    if not ticket_id or not fingerprint:
        return False
    path = hook_ticket_path(repo_root, payload)
    document = {
        "ticketId": ticket_id,
        "fingerprint": fingerprint,
        "requestDigest": hook_request_digest(payload),
        "expiresAtUnix": time.time() + HOOK_TICKET_TTL_SECONDS,
    }
    temp_path = path.with_name(f"{path.name}.{os.getpid()}.tmp")
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
        temp_path.write_text(json.dumps(document, ensure_ascii=False, separators=(",", ":")) + "\n", encoding="utf-8")
        try:
            temp_path.chmod(0o600)
        except OSError:
            pass
        os.replace(temp_path, path)
        return True
    except Exception:
        return False
    finally:
        try:
            temp_path.unlink()
        except FileNotFoundError:
            pass
        except Exception:
            pass


def attach_hook_ticket(repo_root: str, payload: dict[str, Any]) -> tuple[dict[str, Any], bool]:
    request = dict(payload)
    ticket_id, ok = load_hook_ticket(repo_root, payload)
    if not ok:
        return request, False
    if ticket_id:
        request["ticketId"] = ticket_id
    return request, True


def update_hook_ticket(repo_root: str, payload: dict[str, Any], decision: dict[str, Any]) -> str:
    result = get_text(decision.get("decision"))
    if result == "deny_with_ticket":
        if not store_hook_ticket(repo_root, payload, decision):
            return "hook ticket persistence failed"
    elif result in {"allow", "deny"}:
        if not clear_hook_ticket(repo_root, payload):
            return "hook ticket cleanup failed"
    return ""


def record_local_hook_dry_run(repo_root: str, payload: dict[str, Any]) -> None:
    try:
        preview_path = repo_local_hook_dry_run_path(repo_root)
        preview_path.parent.mkdir(parents=True, exist_ok=True)
        signals = hook_preview_signals(payload)
        try:
            preview = local_guard_preview(repo_root, payload)
        except ProjectProtectionError:
            preview = {
                "decision": "deny",
                "riskLevel": "high",
                "projectCodeExecution": False,
                "projectRule": False,
            }
            signals.append("project_protection_config_invalid")
        if preview["projectRule"]:
            signals.append("project_protection_rule")
        if preview["projectCodeExecution"]:
            signals.append("project_code_execution")
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
            "riskLevel": preview["riskLevel"],
            "decisionPreview": preview["decision"],
            "signals": signals,
            "time": __import__("datetime").datetime.now(__import__("datetime").timezone.utc).isoformat(),
        }
        with preview_path.open("a", encoding="utf-8") as handle:
            handle.write(json.dumps(record, ensure_ascii=False) + "\n")
    except Exception:
        return


def record_local_pending_audit(repo_root: str, payload: dict[str, Any], reason: str, offline: bool) -> bool:
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
            "target": redact_preview_target(payload.get("target")),
            "time": __import__("datetime").datetime.now(__import__("datetime").timezone.utc).isoformat(),
            "reason": reason,
            "offline": offline,
        }
        with audit_path.open("a", encoding="utf-8") as handle:
            handle.write(json.dumps(record, ensure_ascii=False) + "\n")
    except Exception:
        return False
    return True


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


def agenttoolgate_executable(repo_root: str = "") -> str:
    configured = os.environ.get("AGENTTOOLGATE_EXE", "").strip()
    if configured:
        return configured
    if repo_root:
        _, _, configured = read_hook_control(repo_root)
        if configured:
            return configured
    return "agenttoolgate.exe" if os.name == "nt" else "agenttoolgate"


def go_cli_timeout_seconds() -> float:
    raw = os.environ.get("AGENTTOOLGATE_CLI_TIMEOUT_MS", "").strip()
    if not raw:
        return DEFAULT_GO_CLI_TIMEOUT_SECONDS
    try:
        timeout_ms = float(raw)
    except ValueError:
        return DEFAULT_GO_CLI_TIMEOUT_SECONDS
    if timeout_ms < MIN_GO_CLI_TIMEOUT_MS or timeout_ms > MAX_GO_CLI_TIMEOUT_MS:
        return DEFAULT_GO_CLI_TIMEOUT_SECONDS
    return timeout_ms / 1000.0


def is_valid_codex_hook_output(output: Any) -> bool:
    if not isinstance(output, dict) or set(output) != {"hookSpecificOutput"}:
        return False
    specific = output.get("hookSpecificOutput")
    if not isinstance(specific, dict):
        return False
    allowed_fields = {"hookEventName", "permissionDecision", "permissionDecisionReason"}
    if not set(specific).issubset(allowed_fields):
        return False
    if specific.get("hookEventName") != "PreToolUse":
        return False
    decision = specific.get("permissionDecision")
    if not isinstance(decision, str) or decision not in {"allow", "deny"}:
        return False
    reason = specific.get("permissionDecisionReason")
    if reason is not None and not isinstance(reason, str):
        return False
    if decision == "deny" and (not isinstance(reason, str) or not reason.strip()):
        return False
    return True


def call_agenttoolgate_guard_hook_codex(input_data: dict[str, Any]) -> dict[str, Any] | None | object:
    try:
        repo_root = find_repo_root(get_text(input_data.get("cwd")) or os.getcwd()) or ""
        completed = subprocess.run(
            [agenttoolgate_executable(repo_root), "guard", "hook", "codex", "--input", "-"],
            input=json.dumps(input_data, ensure_ascii=False),
            text=True,
            encoding="utf-8",
            errors="replace",
            capture_output=True,
            timeout=go_cli_timeout_seconds(),
            check=False,
            env=agenttoolgate_subprocess_env(input_data),
        )
    except subprocess.TimeoutExpired:
        # 子进程超时时请求可能已经到达后端，禁止再发一次 fallback HTTP。
        return GO_CLI_UNCERTAIN
    except (FileNotFoundError, OSError, HookControlError):
        return None
    if completed.returncode != 0:
        return GO_CLI_UNCERTAIN
    stdout = completed.stdout.strip()
    if not stdout:
        return {}
    try:
        output = json.loads(stdout, object_pairs_hook=_reject_hook_control_duplicates)
    except (json.JSONDecodeError, HookControlError, RecursionError):
        return GO_CLI_UNCERTAIN
    if not is_valid_codex_hook_output(output):
        return GO_CLI_UNCERTAIN
    return output


def agenttoolgate_subprocess_env(input_data: dict[str, Any]) -> dict[str, str]:
    env = os.environ.copy()
    if env.get("AGENTTOOLGATE_URL", "").strip():
        return env
    repo_root = find_repo_root(get_text(input_data.get("cwd")) or os.getcwd())
    if repo_root:
        _, endpoint, _ = read_hook_control(repo_root)
        if endpoint:
            env["AGENTTOOLGATE_URL"] = endpoint
    return env


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


def normalized_backend_decision(status: int, decision: dict[str, Any]) -> dict[str, Any]:
    if status < 200 or status >= 300:
        return {"decision": "deny", "reason": f"agenttoolgate request failed (HTTP {status})"}
    result = get_text(decision.get("decision"))
    if result not in {"allow", "deny", "deny_with_ticket"}:
        return {"decision": "deny", "reason": "agenttoolgate returned an invalid decision"}
    if result == "deny_with_ticket" and (
        not get_text(decision.get("approvalId")) or not get_text(decision.get("fingerprint"))
    ):
        return {"decision": "deny", "reason": "agenttoolgate returned an invalid ticket response"}
    return decision


def is_path_within_repo(repo_root: str, target: str, working_directory: str = "") -> bool:
    if not target or target.lower().startswith(("http://", "https://")):
        return False
    try:
        root = Path(repo_root).resolve()
        candidate = Path(target)
        if not candidate.is_absolute():
            base = Path(working_directory).resolve() if working_directory else root
            candidate = base / candidate
        candidate.resolve(strict=False).relative_to(root)
        return True
    except Exception:
        return False


def has_unquoted_shell_control(command: str) -> bool:
    quote = ""
    for char in command:
        if char in {"'", '"'}:
            if not quote:
                quote = char
                continue
            if quote == char:
                quote = ""
                continue
        if quote == "'":
            continue
        if char in {"`", "$"}:
            return True
        if not quote and char in "(){};&|><\r\n":
            return True
    return bool(quote)


def has_command_name(command: str, name: str) -> bool:
    return command == name or command.startswith(name + " ")


def is_explicit_read_only_command(command: str) -> bool:
    normalized = command.strip().lower()
    if not normalized or has_unquoted_shell_control(command):
        return False
    if has_command_name(normalized, "rg"):
        return is_explicit_read_only_search_command(command, "rg")
    if has_command_name(normalized, "grep"):
        return is_explicit_read_only_search_command(command, "grep")
    if has_command_name(normalized, "select-string"):
        return is_explicit_read_only_select_string_command(command)
    if has_command_name(normalized, "git status"):
        return is_explicit_read_only_git_command(command, "status")
    if (
        has_command_name(normalized, "git diff")
        or has_command_name(normalized, "git log")
        or has_command_name(normalized, "git show")
    ):
        return is_explicit_read_only_git_command(command, normalized.split()[1])
    if has_command_name(normalized, "git rev-parse"):
        return is_explicit_read_only_git_command(command, "rev-parse")
    if normalized in {"pwd", "get-location"}:
        return True
    if has_command_name(normalized, "sed"):
        return is_explicit_read_only_sed_command(command)
    if any(has_command_name(normalized, name) for name in ("ls", "dir", "get-childitem")):
        return is_explicit_read_only_path_command(command, allow_no_target=True, allow_glob=True)
    if any(has_command_name(normalized, name) for name in ("cat", "type", "get-content", "gc")):
        return is_explicit_read_only_path_command(command, allow_no_target=False, allow_glob=False)
    return False


def is_explicit_read_only_search_command(command: str, name: str) -> bool:
    tokens = split_read_only_command_tokens(command)
    if not tokens or len(tokens) < 2 or tokens[0].lower() != name:
        return False
    pattern_provided = False
    files_mode = False
    paths_only = False
    index = 1
    while index < len(tokens):
        token = tokens[index]
        if paths_only:
            if not is_safe_relative_read_command_token(token, True):
                return False
            index += 1
            continue
        if token == "--":
            paths_only = True
            index += 1
            continue
        if token.startswith("-") and token != "-":
            if is_forbidden_search_option(token):
                return False
            option, inline_value, has_inline_value = split_read_only_option(token)
            if option == "--files":
                files_mode = True
                index += 1
                continue
            if option in {"-e", "--regexp"}:
                if not has_inline_value:
                    index += 1
                    if index >= len(tokens):
                        return False
                elif not inline_value:
                    return False
                pattern_provided = True
                index += 1
                continue
            if token.startswith("-e") and not token.startswith("--") and len(token) > 2:
                pattern_provided = True
                index += 1
                continue
            if search_option_consumes_value(option) and not has_inline_value:
                index += 1
                if index >= len(tokens):
                    return False
            index += 1
            continue
        if not pattern_provided and not files_mode:
            pattern_provided = True
            index += 1
            continue
        if not is_safe_relative_read_command_token(token, True):
            return False
        index += 1
    return pattern_provided or files_mode


def is_forbidden_search_option(token: str) -> bool:
    lowered = token.lower()
    if token == "-f" or (token.startswith("-f") and not token.startswith("--")):
        return True
    return any(
        lowered == prefix or lowered.startswith(prefix + "=")
        for prefix in (
            "--pre",
            "--file",
            "--ignore-file",
            "--hostname-bin",
            "--exclude-from",
            "--include-from",
        )
    )


def split_read_only_option(token: str) -> tuple[str, str, bool]:
    if "=" in token and token.index("=") > 0:
        name, value = token.split("=", 1)
        return (name.lower() if name.startswith("--") else name, value, True)
    return (token.lower() if token.startswith("--") else token, "", False)


def search_option_consumes_value(option: str) -> bool:
    return option in {
        "-A",
        "-B",
        "-C",
        "-E",
        "-M",
        "-T",
        "-d",
        "-e",
        "-g",
        "-j",
        "-m",
        "-r",
        "-t",
        "--regexp",
        "--glob",
        "--type",
        "--type-not",
        "--encoding",
        "--engine",
        "--sort",
        "--sortr",
        "--color",
        "--colors",
        "--max-count",
        "--max-depth",
        "--context",
        "--before-context",
        "--after-context",
        "--replace",
    }


def is_explicit_read_only_select_string_command(command: str) -> bool:
    tokens = split_read_only_command_tokens(command)
    if not tokens or len(tokens) < 2 or tokens[0].lower() != "select-string":
        return False
    pattern_provided = False
    target_count = 0
    index = 1
    while index < len(tokens):
        token = tokens[index]
        if token.startswith("-"):
            option, inline_value, has_inline_value = split_powershell_parameter(token)
            if option == "-inputobject":
                return False
            if option in {"-path", "-literalpath"}:
                value = inline_value
                if not has_inline_value:
                    index += 1
                    if index >= len(tokens):
                        return False
                    value = tokens[index]
                if not is_safe_powershell_read_path_token(value, True):
                    return False
                target_count += 1
            elif option == "-pattern":
                if not has_inline_value:
                    index += 1
                    if index >= len(tokens):
                        return False
                elif not inline_value:
                    return False
                pattern_provided = True
            elif option in {"-context", "-encoding", "-include", "-exclude", "-culture"}:
                if not has_inline_value:
                    index += 1
                    if index >= len(tokens):
                        return False
            elif has_inline_value:
                return False
            index += 1
            continue
        if not pattern_provided:
            pattern_provided = True
            index += 1
            continue
        if not is_safe_powershell_read_path_token(token, True):
            return False
        target_count += 1
        index += 1
    return pattern_provided and target_count > 0


def split_powershell_parameter(token: str) -> tuple[str, str, bool]:
    if ":" in token and token.index(":") > 0:
        name, value = token.split(":", 1)
        return name.lower(), value, True
    return token.lower(), "", False


def is_explicit_read_only_git_command(command: str, subcommand: str) -> bool:
    tokens = split_read_only_command_tokens(command)
    if (
        not tokens
        or len(tokens) < 2
        or tokens[0].lower() != "git"
        or tokens[1].lower() != subcommand
    ):
        return False
    paths_only = False
    for token in tokens[2:]:
        if token == "--":
            paths_only = True
            continue
        if is_forbidden_git_read_option(token):
            return False
        if not paths_only and token.startswith("-"):
            continue
        if not is_safe_git_read_argument(token):
            return False
    return True


def is_forbidden_git_read_option(token: str) -> bool:
    lowered = token.lower()
    return any(
        lowered == prefix or lowered.startswith(prefix)
        for prefix in (
            "--output",
            "--ext-diff",
            "--textconv",
            "--no-index",
            "--pathspec-from-file",
            "--git-dir=",
            "--work-tree=",
            "--config-env",
        )
    )


def is_safe_git_read_argument(token: str) -> bool:
    value = token.strip()
    if not value:
        return False
    normalized = value.replace("\\", "/")
    if normalized.startswith(("/", "~")):
        return False
    if len(normalized) >= 2 and normalized[1] == ":" and normalized[0].isalpha():
        return False
    if ":" in normalized and normalized.index(":") > 0:
        provider = normalized.split(":", 1)[0].lower()
        if provider in {"alias", "cert", "env", "function", "hkcu", "hklm", "registry", "variable", "wsman"}:
            return False
    parts: list[str] = []
    for part in normalized.split("/"):
        if not part or part == ".":
            continue
        if part == "..":
            if not parts:
                return False
            parts.pop()
            continue
        parts.append(part)
    return True


def is_explicit_read_only_sed_command(command: str) -> bool:
    tokens = split_read_only_command_tokens(command)
    if tokens is None:
        return False
    if len(tokens) < 4 or tokens[0].lower() != "sed" or tokens[1].lower() not in {"-n", "--quiet", "--silent"}:
        return False
    if re.fullmatch(r"\d+(?:,\d+)?p", tokens[2].lower()) is None:
        return False
    return all(
        token and not token.startswith("-") and is_safe_relative_read_command_token(token, False)
        for token in tokens[3:]
    )


def is_explicit_read_only_path_command(command: str, allow_no_target: bool, allow_glob: bool) -> bool:
    tokens = split_read_only_command_tokens(command)
    if not tokens:
        return False
    target_count = 0
    for token in tokens[1:]:
        if token.startswith("-"):
            if token.lower() == "-wait":
                return False
            option, inline_value, has_inline_value = split_powershell_parameter(token)
            if has_inline_value:
                if option not in {"-path", "-literalpath"}:
                    return False
                if not is_safe_powershell_read_path_token(inline_value, allow_glob):
                    return False
                target_count += 1
            continue
        if not is_safe_powershell_read_path_token(token, allow_glob):
            return False
        target_count += 1
    return allow_no_target or target_count > 0


def is_safe_powershell_read_path_token(token: str, allow_glob: bool) -> bool:
    return "," not in token and is_safe_relative_read_command_token(token, allow_glob)


def is_safe_relative_read_command_token(token: str, allow_glob: bool) -> bool:
    value = token.strip()
    if not value:
        return False
    normalized = value.replace("\\", "/")
    if (
        normalized.startswith(("/", "~"))
        or ":" in normalized
        or any(char in normalized for char in "$%@{}()`")
        or (not allow_glob and any(char in normalized for char in "*?[]"))
    ):
        return False
    parts: list[str] = []
    for part in normalized.split("/"):
        if not part or part == ".":
            continue
        if part == "..":
            if not parts:
                return False
            parts.pop()
            continue
        parts.append(part)
    return True


def split_read_only_command_tokens(command: str) -> list[str] | None:
    tokens: list[str] = []
    current: list[str] = []
    quote = ""

    def flush() -> None:
        if current:
            tokens.append("".join(current))
            current.clear()

    for char in command:
        if quote:
            if char == quote:
                quote = ""
            else:
                current.append(char)
            continue
        if char in {"'", '"'}:
            quote = char
        elif char in {" ", "\t"}:
            flush()
        else:
            current.append(char)
    if quote:
        return None
    flush()
    return tokens


def is_explicitly_low_risk_offline_action(repo_root: str, payload: dict[str, Any]) -> bool:
    try:
        if project_protection_floor(repo_root, payload) is not None:
            return False
    except ProjectProtectionError:
        return False
    action = get_text(payload.get("actionType")).lower()
    tool = get_text(payload.get("tool")).lower()
    target = get_text(payload.get("target"))
    content = get_text(payload.get("content"))
    working_directory = get_text(payload.get("workingDirectory"))
    if action == "read" and tool in {"read", "grep", "glob"} and is_path_within_repo(
        repo_root,
        target,
        working_directory,
    ):
        return is_project_metadata_read_target(target) or not is_high_risk_offline_target(payload)
    if action != "exec":
        return False
    # Go CLI 不可用时只放行经过审查的精确只读命令变体。
    return " ".join(content.strip().lower().split()) in {
        "git status",
        "git status --short",
        "git status -s",
        "pwd",
        "get-location",
    }


def is_fast_path_repo_read(repo_root: str, payload: dict[str, Any]) -> bool:
    try:
        if project_protection_floor(repo_root, payload) is not None:
            return False
    except ProjectProtectionError:
        return False
    return (
        get_text(payload.get("actionType")).lower() == "read"
        and get_text(payload.get("tool")).lower() == "read"
        and is_path_within_repo(
            repo_root,
            get_text(payload.get("target")),
            get_text(payload.get("workingDirectory")),
        )
        and (
            is_project_metadata_read_target(get_text(payload.get("target")))
            or not is_high_risk_offline_target(payload)
        )
    )


def attach_python_guard_floor(repo_root: str, payload: dict[str, Any]) -> dict[str, Any]:
    request = dict(payload)
    if is_explicitly_low_risk_offline_action(repo_root, payload):
        request["guardDecision"] = "allow"
        request["guardRiskLevel"] = "low"
    elif is_high_risk_offline_target(payload):
        request["guardDecision"] = "deny"
        request["guardRiskLevel"] = "high"
    else:
        request["guardDecision"] = "ask"
        request["guardRiskLevel"] = "medium"
    try:
        project_floor = project_protection_floor(repo_root, payload)
    except ProjectProtectionError:
        request["guardDecision"] = "deny"
        request["guardRiskLevel"] = "high"
        return request
    if project_floor is not None:
        current = get_text(request.get("guardDecision"))
        if project_floor["decision"] == "deny" or current == "allow":
            request["guardDecision"] = project_floor["decision"]
        request["guardRiskLevel"] = "high"
    return request


def enforce_python_guard_floor(request: dict[str, Any], decision: dict[str, Any]) -> dict[str, Any]:
    guard_decision = get_text(request.get("guardDecision"))
    if guard_decision == "deny":
        return {"decision": "deny", "reason": "local guard denied this action"}
    if guard_decision == "ask" and get_text(decision.get("decision")) == "allow" and not get_text(request.get("ticketId")):
        # 低/中风险 remembered allow 不要求客户端再次携带 ticket，但后端必须
        # 返回完整的已审批证据；普通 allow 仍不能绕过本地 ask floor。
        approval_status = get_text(decision.get("approvalStatus")).lower()
        if (
            approval_status in {"approved", "consumed"}
            and get_text(decision.get("approvalId"))
            and get_text(decision.get("fingerprint"))
        ):
            return decision
        return {"decision": "deny", "reason": "local guard requires confirmation"}
    return decision


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

    permission = "allow" if result == "allow" else "deny"
    specific: dict[str, str] = {
        "hookEventName": "PreToolUse",
        "permissionDecision": permission,
    }
    if permission != "allow" or reason != "allowed":
        specific["permissionDecisionReason"] = reason
    return {"hookSpecificOutput": specific}


def handle_invalid_hook_input(repo_root: str | None) -> None:
    if repo_root is None:
        return
    try:
        hook_mode, _, _ = read_hook_control(repo_root)
    except HookControlError:
        reason = "hook control invalid"
    else:
        if hook_mode != "live":
            return
        reason = "AgentToolGate blocked invalid hook input"
    output = {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "deny",
            "permissionDecisionReason": reason,
        }
    }
    print(json.dumps(output, ensure_ascii=False), flush=True)


def main() -> int:
    if os.environ.get("TRELLIS_HOOKS") == "0" or os.environ.get("TRELLIS_DISABLE_HOOKS") == "1":
        return 0

    try:
        input_data = json.load(sys.stdin)
    except (json.JSONDecodeError, RecursionError):
        handle_invalid_hook_input(find_repo_root_safely(os.getcwd()))
        return 0
    if not isinstance(input_data, dict):
        handle_invalid_hook_input(find_repo_root_safely(os.getcwd()))
        return 0

    try:
        repo_root = find_repo_root(input_data.get("cwd", os.getcwd()))
    except (OSError, RuntimeError, ValueError):
        handle_invalid_hook_input(find_repo_root_safely(os.getcwd()))
        return 0
    if repo_root is None:
        return 0

    tool_name = get_tool_name(input_data)
    if not tool_name:
        handle_invalid_hook_input(repo_root)
        return 0
    if not is_guarded_tool(tool_name):
        return 0

    tool_input = get_tool_input(input_data)
    adapter = detect_adapter()
    try:
        hook_mode, hook_endpoint, _ = read_hook_control(repo_root)
    except HookControlError:
        output = build_output(adapter, {"decision": "deny", "reason": "hook control invalid"}, tool_input)
        print(json.dumps(output, ensure_ascii=False), flush=True)
        return 0
    if hook_mode == "off":
        return 0

    payload = build_agent_guard_request(adapter, input_data, tool_name, tool_input, repo_root)
    if hook_mode == "dry-run":
        if is_fast_path_repo_read(repo_root, payload):
            return 0
        record_local_hook_dry_run(repo_root, payload)
        return 0

    go_output = call_agenttoolgate_guard_hook_codex(input_data)
    if go_output is GO_CLI_UNCERTAIN:
        if is_explicitly_low_risk_offline_action(repo_root, payload):
            if record_local_pending_audit(repo_root, payload, "AgentToolGate CLI uncertain, local pending audit", True):
                return 0
            decision = {"decision": "deny", "reason": "AgentToolGate CLI uncertain, pending audit unavailable"}
        elif is_high_risk_offline_target(payload):
            decision = {"decision": "deny", "reason": "AgentToolGate CLI uncertain, sensitive target denied"}
        else:
            decision = {"decision": "deny", "reason": "AgentToolGate CLI uncertain, action denied"}
        output = build_output(adapter, decision, tool_input)
        print(json.dumps(output, ensure_ascii=False), flush=True)
        return 0
    if go_output == {}:
        return 0
    if go_output is not None:
        specific = go_output.get("hookSpecificOutput") if isinstance(go_output, dict) else None
        if isinstance(specific, dict) and get_text(specific.get("permissionDecision")) == "allow":
            return 0
        print(json.dumps(go_output, ensure_ascii=False), flush=True)
        return 0

    guarded_payload = attach_python_guard_floor(repo_root, payload)
    request_payload, ticket_ok = attach_hook_ticket(repo_root, guarded_payload)
    if not ticket_ok:
        decision = {"decision": "deny", "reason": "hook ticket cleanup failed"}
        output = build_output(adapter, decision, tool_input)
        print(json.dumps(output, ensure_ascii=False), flush=True)
        return 0
    status, decision, raw = post_json(build_url(repo_root, hook_endpoint), request_payload)
    if status == 0:
        if is_explicitly_low_risk_offline_action(repo_root, payload):
            if record_local_pending_audit(repo_root, payload, "ATG offline, local pending audit", True):
                return 0
            decision = {"decision": "deny", "reason": "ATG offline, pending audit unavailable"}
        if is_high_risk_offline_target(payload):
            decision = {"decision": "deny", "reason": "ATG offline, sensitive target denied"}
        elif get_text(decision.get("decision")) == "":
            decision = {"decision": "deny", "reason": "ATG offline, action not explicitly low risk"}
    else:
        decision = enforce_python_guard_floor(request_payload, normalized_backend_decision(status, decision))
        ticket_error = update_hook_ticket(repo_root, guarded_payload, decision)
        if ticket_error:
            decision = {"decision": "deny", "reason": ticket_error}

    if get_text(decision.get("decision")) == "allow":
        return 0

    output = build_output(adapter, decision, tool_input)
    print(json.dumps(output, ensure_ascii=False), flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
