#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Local Action Firewall 共享离线判定核心。"""

from __future__ import annotations

import base64
import binascii
import json
import os
import re
import urllib.parse
from pathlib import Path
from typing import Any


PROJECT_PROTECTION_MAX_BYTES = 64 * 1024
PROJECT_CODE_EXECUTION_PATTERNS = (
    re.compile(r"(?i)(^|[\s;&|])go\s+(test|vet)(\s|$)"),
    re.compile(r"(?i)(^|[\s;&|])(npm|pnpm|yarn|bun)\s+(test|run\s+(build|test))(\s|$)"),
)


class ProjectProtectionError(ValueError):
    pass


def is_probably_script_target(target: str) -> bool:
    lowered = target.lower().strip()
    if not lowered:
        return False
    suffixes = (
        ".ps1",
        ".psm1",
        ".vbs",
        ".js",
        ".mjs",
        ".cjs",
        ".ts",
        ".tsx",
        ".py",
        ".sh",
        ".bash",
        ".bat",
        ".cmd",
        ".pl",
        ".rb",
        ".php",
    )
    return lowered.endswith(suffixes)


def path_segments(value: str) -> list[str]:
    normalized = value.lower().strip().replace("/", "\\")
    while normalized.startswith("\\\\?\\"):
        normalized = normalized[len("\\\\?\\") :]
    normalized = normalized.strip("\\")
    parts = []
    for part in normalized.split("\\"):
        part = part.strip().rstrip(" .")
        if not part or part == ".":
            continue
        parts.append(part)
    return parts


def has_suffix(parts: list[str], suffix: list[str]) -> bool:
    if not suffix or len(parts) < len(suffix):
        return False
    return parts[-len(suffix) :] == suffix


def has_sequence(parts: list[str], sequence: list[str]) -> bool:
    if not sequence or len(parts) < len(sequence):
        return False
    for offset in range(0, len(parts) - len(sequence) + 1):
        if parts[offset : offset + len(sequence)] == sequence:
            return True
    return False


def path_matches_exact_file(target: str, file_path: str) -> bool:
    return has_suffix(path_segments(target), path_segments(file_path))


def path_matches_dir_or_descendant(target: str, dir_path: str) -> bool:
    return has_sequence(path_segments(target), path_segments(dir_path))


def is_probably_high_risk_target(target: str) -> bool:
    lowered = target.lower().strip()
    if not lowered:
        return False
    parts = path_segments(lowered)
    if parts:
        filename = parts[-1]
        if filename == ".env" or filename.startswith(".env."):
            return True
        if filename in {
            ".npmrc",
            "agents.md",
            "bun.lockb",
            "id_ed25519",
            "id_rsa",
            "microsoft.powershell_profile.ps1",
            "package-lock.json",
            "package.json",
            "pnpm-lock.yaml",
            "yarn.lock",
        }:
            return True
        if any(
            marker in filename
            for marker in (
                ".asc",
                ".cer",
                ".crt",
                ".der",
                ".key",
                ".p12",
                ".p7b",
                ".p7c",
                ".password",
                ".pem",
                ".pfx",
                ".secret",
                ".token",
            )
        ):
            return True
    exact_files = (
        ".agenttoolgate/protected.json",
        ".tmp/agenttoolgate/hook-control.json",
        "configs/policies.yaml",
    )
    if any(path_matches_exact_file(lowered, item) for item in exact_files):
        return True
    dirs = (
        ".ssh",
        ".aws",
        ".azure",
        ".kube",
        ".git/hooks",
        ".claude",
        ".codex",
        ".agents",
        ".github/workflows",
        "appdata/roaming/microsoft/windows/start menu/programs/startup",
        "documents/powershell",
        "documents/windowspowershell",
        "appdata/local/google/chrome/user data",
        "appdata/local/microsoft/edge/user data",
        "appdata/local/bravesoftware/brave-browser/user data",
        "appdata/local/vivaldi/user data",
        "appdata/local/chromium/user data",
        "appdata/roaming/opera software/opera stable",
        "appdata/roaming/opera software/opera gx stable",
        "appdata/roaming/mozilla/firefox/profiles",
        ".config/google-chrome",
        ".config/chromium",
        ".config/bravesoftware/brave-browser",
        ".config/vivaldi",
        ".mozilla/firefox",
        "library/application support/google/chrome",
        "library/application support/microsoft edge",
        "library/application support/bravesoftware/brave-browser",
        "library/application support/vivaldi",
    )
    if any(path_matches_dir_or_descendant(lowered, item) for item in dirs):
        return True
    sensitive_sequences = (
        ("windows", "system32", "config"),
        ("software", "microsoft", "windows", "currentversion", "run"),
        ("software", "microsoft", "windows", "currentversion", "runonce"),
        ("windows", "system32", "tasks"),
    )
    return any(has_sequence(parts, list(sequence)) for sequence in sensitive_sequences)


def is_project_metadata_read_target(target: str) -> bool:
    parts = path_segments(target)
    if not parts:
        return False
    if parts[-1] in {
        "agents.md",
        "bun.lockb",
        "package-lock.json",
        "package.json",
        "pnpm-lock.yaml",
        "yarn.lock",
    }:
        return True
    return any(
        has_sequence(parts, path_segments(directory))
        for directory in (".claude", ".codex", ".agents", ".github/workflows")
    )


def contains_hidden_script_features(content: str) -> bool:
    lowered = content.lower().strip()
    if not lowered:
        return False
    if "executionpolicy bypass" in lowered and "windowstyle hidden" in lowered:
        return True
    return (
        "windowstyle hidden" in lowered
        or "executionpolicy bypass" in lowered
        or "-encodedcommand" in lowered
        or "-enc " in lowered
    )


def _looks_like_base64_token(value: str) -> bool:
    if not value:
        return False
    for char in value:
        if char.isalnum() or char in "+/=":
            continue
        return False
    return True


def decoded_base64_payloads(content: str) -> list[str]:
    if not content:
        return []
    tokens = []
    current: list[str] = []
    for char in content:
        if char.isalnum() or char in "+/=":
            current.append(char)
            continue
        if current:
            tokens.append("".join(current))
            current = []
    if current:
        tokens.append("".join(current))

    decoded: list[str] = []
    for token in tokens:
        trimmed = token.strip()
        if len(trimmed) < 16 or len(trimmed) % 4 != 0 or not _looks_like_base64_token(trimmed):
            continue
        try:
            raw = base64.b64decode(trimmed, validate=True)
        except (binascii.Error, ValueError):
            continue
        try:
            decoded.append(raw.decode("utf-8", errors="replace"))
        except Exception:
            continue
    return decoded


def contains_hidden_script_features_in_decoded_base64(content: str) -> bool:
    return any(contains_hidden_script_features(decoded.lower()) for decoded in decoded_base64_payloads(content))


def is_read_only_search_payload(payload: dict[str, object]) -> bool:
    action = str(payload.get("actionType") or "").strip().lower()
    tool = str(payload.get("tool") or "").strip().lower()
    if action == "read" and tool in {"read", "grep", "glob"}:
        return True
    if action != "exec" or tool not in {"bash", "shell", "command", "powershell", "pwsh"}:
        return False
    content = str(payload.get("content") or "").lstrip().lower()
    return any(content == name or content.startswith(name + " ") for name in ("rg", "grep", "select-string"))


def is_high_risk_offline_target(payload: dict[str, object]) -> bool:
    target = str(payload.get("target") or "").strip()
    content = str(payload.get("content") or "").strip()
    skip_hidden_script_scan = is_read_only_search_payload(payload)
    return (
        is_probably_high_risk_target(target)
        or is_probably_high_risk_target(content)
        or (not skip_hidden_script_scan and contains_hidden_script_features(content))
        or (not skip_hidden_script_scan and contains_hidden_script_features_in_decoded_base64(content))
    )


def is_project_code_execution(payload: dict[str, Any]) -> bool:
    action = str(payload.get("actionType") or "").strip().lower()
    if action not in {"command", "exec", "execute"}:
        return False
    candidates = (
        str(payload.get("target") or ""),
        str(payload.get("content") or ""),
    )
    return any(pattern.search(candidate) for candidate in candidates for pattern in PROJECT_CODE_EXECUTION_PATTERNS)


def local_guard_preview(repo_root: str, payload: dict[str, Any]) -> dict[str, Any]:
    high_risk = is_high_risk_offline_target(payload)
    project_code_execution = is_project_code_execution(payload)
    if high_risk:
        decision = "deny"
        risk_level = "high"
    elif project_code_execution:
        decision = "ask"
        risk_level = "medium"
    else:
        decision = "allow"
        risk_level = "low"

    project_floor = project_protection_floor(repo_root, payload)
    if project_floor is not None:
        rank = {"allow": 1, "ask": 2, "deny": 3}
        floor_decision = str(project_floor.get("decision") or "")
        if rank.get(floor_decision, 0) > rank.get(decision, 0):
            decision = floor_decision
        risk_level = "high"

    return {
        "decision": decision,
        "riskLevel": risk_level,
        "projectCodeExecution": project_code_execution,
        "projectRule": project_floor is not None,
    }


def project_protection_floor(repo_root: str, payload: dict[str, Any]) -> dict[str, str] | None:
    protection = _load_project_protection(repo_root)
    if protection is None or not protection["enabled"]:
        return None

    floor: dict[str, str] | None = None
    for target, operation in _project_target_operations(payload):
        relative = _project_relative_path(repo_root, target, str(payload.get("workingDirectory") or ""))
        if relative is None:
            continue
        for rule in protection["protectedPaths"]:
            if not _project_pattern_matches(rule["pattern"], relative):
                continue
            effect = str(rule.get(operation) or "")
            if not effect:
                continue
            floor = _stricter_project_floor(
                floor,
                {
                    "decision": "deny" if effect == "deny" else "ask",
                    "riskLevel": "high",
                    "reason": str(rule.get("reason") or "project protected path"),
                },
            )

    egress = protection["egress"]
    if egress["enabled"] and _is_project_network_write(payload):
        host = _project_network_host(str(payload.get("target") or ""))
        if not host or not _project_host_allowed(host, egress["allowedHosts"]):
            floor = _stricter_project_floor(
                floor,
                {
                    "decision": "deny" if egress["unlistedWrite"] == "deny" else "ask",
                    "riskLevel": "high",
                    "reason": "project egress policy",
                },
            )
    return floor


def _load_project_protection(repo_root: str) -> dict[str, Any] | None:
    try:
        root = Path(repo_root).resolve(strict=True)
        config_path = root / ".agenttoolgate" / "protected.json"
        if not config_path.exists():
            return None
        if config_path.is_symlink() or not config_path.is_file():
            raise ProjectProtectionError("project protection config must be a regular file")
        if config_path.stat().st_size > PROJECT_PROTECTION_MAX_BYTES:
            raise ProjectProtectionError("project protection config is too large")
        config_path.resolve(strict=True).relative_to(root)
        document = json.loads(config_path.read_text(encoding="utf-8"), object_pairs_hook=_reject_duplicate_keys)
    except (OSError, UnicodeError, json.JSONDecodeError, ValueError) as exc:
        raise ProjectProtectionError("project protection config is invalid") from exc
    if not isinstance(document, dict):
        raise ProjectProtectionError("project protection config must be an object")
    _reject_unknown_fields(document, {"version", "projectRoot", "workspace", "localActionFirewall"})
    if document.get("version") != 1:
        raise ProjectProtectionError("unsupported project protection version")
    firewall = document.get("localActionFirewall")
    if not isinstance(firewall, dict):
        raise ProjectProtectionError("localActionFirewall must be an object")
    _reject_unknown_fields(firewall, {"enabled", "defaultMode", "protectedPaths", "egress", "notes"})
    enabled = _strict_project_bool(firewall.get("enabled"), "localActionFirewall.enabled")
    if "defaultMode" in firewall and not isinstance(firewall["defaultMode"], str):
        raise ProjectProtectionError("defaultMode must be a string")
    if "notes" in firewall and (
        not isinstance(firewall["notes"], list) or any(not isinstance(note, str) for note in firewall["notes"])
    ):
        raise ProjectProtectionError("notes must be a string array")

    raw_rules = firewall.get("protectedPaths", [])
    if not isinstance(raw_rules, list) or len(raw_rules) > 128:
        raise ProjectProtectionError("protectedPaths must be a bounded array")
    rules: list[dict[str, str]] = []
    for raw_rule in raw_rules:
        if not isinstance(raw_rule, dict):
            raise ProjectProtectionError("protected path rule must be an object")
        _reject_unknown_fields(raw_rule, {"pattern", "read", "write", "delete", "exec", "reason"})
        pattern = _normalize_project_pattern(raw_rule.get("pattern"))
        if "reason" in raw_rule and not isinstance(raw_rule["reason"], str):
            raise ProjectProtectionError("protected path reason must be a string")
        rule = {"pattern": pattern, "reason": str(raw_rule.get("reason") or "").strip()[:160]}
        configured = False
        for action in ("read", "write", "delete", "exec"):
            if action in raw_rule and not isinstance(raw_rule[action], str):
                raise ProjectProtectionError("protected path effect must be a string")
            effect = str(raw_rule.get(action) or "").strip().lower()
            if effect and effect not in {"require_approval", "deny"}:
                raise ProjectProtectionError("unsupported protected path effect")
            if effect:
                configured = True
                rule[action] = effect
        if not configured:
            raise ProjectProtectionError("protected path rule has no action")
        rules.append(rule)

    raw_egress = firewall.get("egress", {})
    if raw_egress is None:
        raw_egress = {}
    if not isinstance(raw_egress, dict):
        raise ProjectProtectionError("egress must be an object")
    _reject_unknown_fields(raw_egress, {"enabled", "allowedHosts", "unlistedWrite"})
    egress_enabled = _strict_project_bool(raw_egress.get("enabled"), "egress.enabled")
    raw_hosts = raw_egress.get("allowedHosts", [])
    if not isinstance(raw_hosts, list) or len(raw_hosts) > 128:
        raise ProjectProtectionError("allowedHosts must be a bounded array")
    hosts = [_normalize_project_host(host) for host in raw_hosts]
    if "unlistedWrite" in raw_egress and not isinstance(raw_egress["unlistedWrite"], str):
        raise ProjectProtectionError("egress effect must be a string")
    unlisted_write = str(raw_egress.get("unlistedWrite") or "").strip().lower()
    if egress_enabled and not unlisted_write:
        unlisted_write = "require_approval"
    if unlisted_write and unlisted_write not in {"require_approval", "deny"}:
        raise ProjectProtectionError("unsupported egress effect")
    return {
        "enabled": enabled,
        "protectedPaths": rules,
        "egress": {
            "enabled": egress_enabled,
            "allowedHosts": hosts,
            "unlistedWrite": unlisted_write,
        },
    }


def _reject_duplicate_keys(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ProjectProtectionError("duplicate project protection field")
        result[key] = value
    return result


def _reject_unknown_fields(value: dict[str, Any], allowed: set[str]) -> None:
    if any(key not in allowed for key in value):
        raise ProjectProtectionError("unknown project protection field")


def _strict_project_bool(value: Any, field: str) -> bool:
    if value is None:
        return False
    if not isinstance(value, bool):
        raise ProjectProtectionError(f"{field} must be a boolean")
    return value


def _normalize_project_pattern(value: Any) -> str:
    if not isinstance(value, str):
        raise ProjectProtectionError("project pattern must be a string")
    raw = value.strip()
    if not raw or raw.startswith(("/", "\\\\")) or re.match(r"^[A-Za-z]:", raw):
        raise ProjectProtectionError("project pattern must be repo-relative")
    normalized = raw.replace("\\", "/").strip("/")
    segments = normalized.split("/")
    if any(segment in {"", ".", ".."} for segment in segments):
        raise ProjectProtectionError("project pattern contains unsafe traversal")
    base = normalized[:-3] if normalized.endswith("/**") else normalized
    if any(marker in base for marker in ("*", "?", "[")) or ("**" in normalized and not normalized.endswith("/**")):
        raise ProjectProtectionError("project pattern only supports exact paths or /** subtrees")
    return normalized


def _normalize_project_host(value: Any) -> str:
    if not isinstance(value, str):
        raise ProjectProtectionError("project host must be a string")
    host = value.strip().lower()
    if not host or host == "*" or any(marker in host for marker in ("/", "?", "#", "@")):
        raise ProjectProtectionError("project host is invalid")
    if "*" in host:
        if not host.startswith("*.") or host.count("*") != 1 or len(host.removeprefix("*.")) < 3:
            raise ProjectProtectionError("project host wildcard is invalid")
    return host


def _project_operation(payload: dict[str, Any]) -> str:
    action = str(payload.get("actionType") or "").strip().lower()
    tool = str(payload.get("tool") or "").strip().lower()
    content = str(payload.get("content") or "").lstrip().lower()
    if tool in {"apply_patch", "applypatch"} or "*** begin patch" in content:
        return "write"
    if tool in {"write", "edit", "multiedit", "notebookedit"} or any(
        marker in tool for marker in ("write_file", "edit_file")
    ):
        return "write"
    if tool in {"read", "grep", "glob"}:
        return "read"
    if action == "delete" or any(word in tool for word in ("delete", "remove")) or _is_project_delete_command(content):
        return "delete"
    if content.startswith(("set-content ", "out-file ", "add-content ")):
        return "write"
    if content.startswith(("get-content ", "select-string ", "rg ", "grep ", "cat ", "type ")):
        return "read"
    if action in {"write", "create", "update", "patch", "post"}:
        return "write"
    if action in {"read", "inspect", "view", "list", "get"}:
        return "read"
    if action in {"exec", "execute", "command"} or content:
        return "exec"
    return ""


def _project_target_operations(payload: dict[str, Any]) -> list[tuple[str, str]]:
    tool = str(payload.get("tool") or "").strip().lower()
    content = str(payload.get("content") or "")
    is_patch = tool in {"apply_patch", "applypatch"} or "*** Begin Patch" in content
    if is_patch and tool not in {"read", "grep", "glob"}:
        operations = _extract_patch_target_operations(content)
        explicit_targets = {_project_target_key(target) for target, _ in operations}
        for target in _project_targets(payload):
            if _project_target_key(target) not in explicit_targets:
                operations.append((target, "write"))
        if operations:
            return operations

    if tool in {"bash", "shell", "shell_command", "command", "powershell", "pwsh"}:
        operations = _project_shell_target_operations(content)
        if operations:
            return operations

    operation = _project_operation(payload)
    if not operation:
        return []
    return [(target, operation) for target in _project_targets(payload)]


def _project_target_key(target: str) -> str:
    return target.casefold() if os.name == "nt" else target


def _project_shell_target_operations(command: str, depth: int = 0) -> list[tuple[str, str]]:
    if not command or depth > 2:
        return []
    operations: list[tuple[str, str]] = []
    for tokens in _project_command_segments(command):
        for candidate in _parse_project_shell_tokens(tokens, depth):
            if candidate not in operations:
                operations.append(candidate)
    return operations


def _parse_project_shell_tokens(tokens: list[str], depth: int) -> list[tuple[str, str]]:
    if not tokens or depth > 2:
        return []
    index = 0
    if tokens[index].lower() == "sudo":
        index = _skip_project_sudo_options(tokens, index + 1)
    if index < len(tokens) and tokens[index].lower() == "command":
        index += 1
    if index >= len(tokens):
        return []

    executable = _project_command_name(tokens[index])
    args = tokens[index + 1 :]
    if executable == "cmd":
        command_index = 0
        while command_index < len(args) and args[command_index].lower() in {"/d", "/q", "/s"}:
            command_index += 1
        if command_index >= len(args) or args[command_index].lower() not in {"/c", "/k"}:
            return []
        return _project_shell_target_operations(" ".join(args[command_index + 1 :]), depth + 1)
    if executable in {"powershell", "pwsh"}:
        command_index = next(
            (
                candidate + 1
                for candidate, argument in enumerate(args)
                if argument.lower() in {"-command", "-c"}
            ),
            -1,
        )
        if command_index < 0:
            return []
        return _project_shell_target_operations(" ".join(args[command_index:]), depth + 1)
    if executable in {"rm", "del", "erase", "rmdir", "rd", "remove-item"}:
        return [(target, "delete") for target in _extract_project_delete_targets(executable, args)]
    if executable in {"set-content", "add-content", "out-file"}:
        return [(target, "write") for target in _extract_project_write_targets(executable, args)]
    if executable in {"get-content", "cat", "type", "more", "head", "tail"}:
        return [(target, "read") for target in _extract_project_read_targets(executable, args)]
    if executable in {"rg", "grep"}:
        return [(target, "read") for target in _extract_project_search_targets(executable, args)]
    if executable == "select-string":
        return [(target, "read") for target in _extract_project_select_string_targets(args)]
    return []


def _skip_project_sudo_options(tokens: list[str], index: int) -> int:
    value_options = {"-u", "--user", "-g", "--group", "-h", "--host", "-p", "--prompt", "-c", "--close-from"}
    while index < len(tokens):
        argument = tokens[index]
        if argument == "--":
            return index + 1
        if not argument.startswith("-"):
            return index
        option = argument.split("=", 1)[0].lower()
        if option in value_options and "=" not in argument:
            index += 2
        else:
            index += 1
    return index


def _project_command_name(value: str) -> str:
    return value.replace("\\", "/").rsplit("/", 1)[-1].lower().removesuffix(".exe")


def _project_static_command_target(value: str) -> str:
    target = value.strip()
    if not target or target in {"-", "--"}:
        return ""
    if target.startswith(("-", "/", "\\", "~")) or re.match(r"^[A-Za-z]:", target):
        return ""
    if urllib.parse.urlsplit(target).scheme:
        return ""
    if any(marker in target for marker in ("$", "%", "*", "?", "[", "]", "{", "}", "(", ")", "<", ">", "|", "&", ";", "`", "\r", "\n", ",")):
        return ""
    normalized = target.replace("\\", "/")
    if any(segment == ".." for segment in normalized.split("/")):
        return ""
    if ":" in target:
        return ""
    return target


def _project_static_command_targets(values: list[str]) -> list[str]:
    targets: list[str] = []
    for value in values:
        target = _project_static_command_target(value)
        if target and target not in targets:
            targets.append(target)
    return targets


def _extract_project_read_targets(command: str, args: list[str]) -> list[str]:
    if command == "get-content":
        target_options = {"-path", "-literalpath"}
        value_options = {
            "-credential",
            "-delimiter",
            "-encoding",
            "-exclude",
            "-filter",
            "-include",
            "-readcount",
            "-stream",
            "-tail",
            "-totalcount",
        }
        switch_options = {"-asbytestream", "-force", "-raw", "-wait"}
    elif command in {"cat", "type"}:
        target_options = set()
        value_options = set()
        switch_options = {
            "-a",
            "--show-all",
            "-b",
            "--number-nonblank",
            "-e",
            "-n",
            "--number",
            "-s",
            "--squeeze-blank",
            "-t",
            "-v",
            "--show-nonprinting",
        }
    elif command in {"head", "tail"}:
        target_options = set()
        value_options = {
            "-c",
            "--bytes",
            "-n",
            "--lines",
            "--max-unchanged-stats",
            "--pid",
            "--sleep-interval",
        }
        switch_options = {
            "-f",
            "--follow",
            "-q",
            "--quiet",
            "--retry",
            "-v",
            "--verbose",
            "-z",
            "--zero-terminated",
        }
    else:
        target_options = set()
        value_options = {"-n", "--lines"}
        switch_options = {"-c", "-d", "-f", "-l", "-p", "-s", "-u"}

    targets: list[str] = []
    positionals: list[str] = []
    options_ended = False
    index = 0
    while index < len(args):
        argument = args[index]
        lowered = argument.lower()
        if argument == "--":
            options_ended = True
            index += 1
            continue
        if not options_ended and command in {"more", "head", "tail"} and (
            re.fullmatch(r"[+-]\d+", argument) or argument.startswith("+/")
        ):
            index += 1
            continue
        if not options_ended and argument.startswith(("-", "/")):
            option = lowered.split("=", 1)[0]
            if option in target_options:
                if "=" in argument or index + 1 >= len(args):
                    return []
                targets.append(args[index + 1])
                index += 2
                continue
            if option in value_options:
                if "=" not in argument:
                    if index + 1 >= len(args):
                        return []
                    index += 2
                else:
                    index += 1
                continue
            if option in switch_options or _is_known_project_read_short_option(command, argument):
                index += 1
                continue
            return []
        positionals.append(argument)
        index += 1
    targets.extend(positionals)
    return _project_static_command_targets(targets)


def _is_known_project_read_short_option(command: str, argument: str) -> bool:
    if command in {"cat", "type"}:
        return bool(re.fullmatch(r"-[AbEnTsv]+", argument))
    if command in {"head", "tail"}:
        return bool(re.fullmatch(r"-(?:[cqvz]+|\d+|[cn]\d+)", argument, flags=re.IGNORECASE))
    if command == "more":
        return bool(re.fullmatch(r"(?:/[ceps]|/t\d+|-[dlfpcsu]+)", argument, flags=re.IGNORECASE))
    return False


def _extract_project_search_targets(command: str, args: list[str]) -> list[str]:
    if command == "rg":
        switch_options = {
            "-0",
            "--null",
            "-a",
            "--text",
            "-c",
            "--count",
            "--count-matches",
            "-F",
            "--fixed-strings",
            "-h",
            "--help",
            "--hidden",
            "-i",
            "--ignore-case",
            "-l",
            "--files-with-matches",
            "-L",
            "--files-without-match",
            "-n",
            "--line-number",
            "--no-ignore",
            "--no-ignore-global",
            "--no-ignore-parent",
            "--no-ignore-vcs",
            "--no-messages",
            "--one-file-system",
            "--pcre2",
            "-q",
            "--quiet",
            "-s",
            "--case-sensitive",
            "-S",
            "--smart-case",
            "--stats",
            "-U",
            "--multiline",
            "--multiline-dotall",
            "-u",
            "-w",
            "--word-regexp",
            "--crlf",
            "--json",
            "--files",
            "--follow",
        }
        value_options = {
            "-A",
            "--after-context",
            "-B",
            "--before-context",
            "-C",
            "--context",
            "--encoding",
            "--engine",
            "-g",
            "--glob",
            "-j",
            "--threads",
            "-m",
            "--max-count",
            "--max-depth",
            "--pre",
            "--pre-glob",
            "--sort",
            "--sortr",
            "-t",
            "--type",
            "-T",
            "--type-not",
            "--type-add",
        }
        pattern_options = {"-e", "--regexp", "-f", "--file"}
        short_switches = "0acFhilLnqSsUuw"
        attached_value_options = {"-A", "-B", "-C", "-f", "-g", "-j", "-m", "-t", "-T"}
    else:
        switch_options = {
            "-a",
            "--text",
            "-c",
            "--count",
            "-E",
            "--extended-regexp",
            "-F",
            "--fixed-strings",
            "-G",
            "--basic-regexp",
            "-h",
            "--no-filename",
            "-H",
            "--with-filename",
            "-i",
            "--ignore-case",
            "-I",
            "--binary-files-without-match",
            "-l",
            "--files-with-matches",
            "-L",
            "--files-without-match",
            "-n",
            "--line-number",
            "-o",
            "--only-matching",
            "-P",
            "--perl-regexp",
            "-q",
            "--quiet",
            "-r",
            "--recursive",
            "-R",
            "--dereference-recursive",
            "-s",
            "--no-messages",
            "-v",
            "--invert-match",
            "-w",
            "--word-regexp",
            "-x",
            "--line-regexp",
            "-z",
            "--null-data",
        }
        value_options = {
            "-A",
            "--after-context",
            "-B",
            "--before-context",
            "-C",
            "--context",
            "-d",
            "--devices",
            "-D",
            "--directories",
            "--exclude",
            "--exclude-dir",
            "--include",
            "--label",
            "-m",
            "--max-count",
        }
        pattern_options = {"-e", "--regexp", "-f", "--file"}
        short_switches = "abcEFGhHiIlLnoPqRrsvwxz"
        attached_value_options = {"-A", "-B", "-C", "-d", "-D", "-m"}

    positionals: list[str] = []
    explicit_pattern = False
    files_mode = False
    options_ended = False
    index = 0
    while index < len(args):
        argument = args[index]
        lowered = argument.lower()
        if argument == "--":
            options_ended = True
            index += 1
            continue
        if not options_ended and argument.startswith("-") and argument != "-":
            raw_option = argument.split("=", 1)[0]
            option = lowered.split("=", 1)[0] if raw_option.startswith("--") else raw_option
            if option in pattern_options:
                explicit_pattern = True
                if "=" not in argument:
                    if index + 1 >= len(args):
                        return []
                    index += 2
                else:
                    index += 1
                continue
            if option == "--files":
                files_mode = True
                index += 1
                continue
            if option in value_options:
                if "=" not in argument:
                    if index + 1 >= len(args):
                        return []
                    index += 2
                else:
                    index += 1
                continue
            if option in switch_options:
                index += 1
                continue
            if len(argument) > 2 and argument[:2] in pattern_options:
                explicit_pattern = True
                index += 1
                continue
            if len(argument) > 2 and argument[:2] in attached_value_options:
                index += 1
                continue
            if len(argument) > 2 and all(character in short_switches for character in argument[1:]):
                index += 1
                continue
            return []
        positionals.append(argument)
        index += 1

    if files_mode or explicit_pattern:
        candidates = positionals
    else:
        if not positionals:
            return []
        candidates = positionals[1:]
    if command == "rg" and not candidates:
        candidates = ["."]
    return _project_static_command_targets(candidates)


def _extract_project_select_string_targets(args: list[str]) -> list[str]:
    targets: list[str] = []
    value_options = {
        "-context",
        "-culture",
        "-encoding",
        "-exclude",
        "-include",
        "-inputobject",
        "-pattern",
    }
    switch_options = {"-allmatches", "-casesensitive", "-list", "-notmatch", "-quiet", "-raw", "-simplematch"}
    index = 0
    while index < len(args):
        argument = args[index]
        lowered = argument.lower()
        if lowered in {"-path", "-literalpath"}:
            if index + 1 >= len(args):
                return []
            targets.append(args[index + 1])
            index += 2
            continue
        if argument.startswith("-"):
            if lowered in value_options:
                if index + 1 >= len(args):
                    return []
                index += 2
                continue
            if lowered in switch_options:
                index += 1
                continue
            return []
        index += 1
    return _project_static_command_targets(targets)


def _extract_project_write_targets(command: str, args: list[str]) -> list[str]:
    target_options = {"-filepath"} if command == "out-file" else {"-path", "-literalpath"}
    value_options = {"-encoding", "-exclude", "-filter", "-include", "-inputobject", "-stream", "-value", "-width"}
    switch_options = {"-append", "-force", "-noclobber", "-nonewline", "-passthru", "-whatif", "-confirm"}
    explicit_targets: list[str] = []
    positionals: list[str] = []
    index = 0
    while index < len(args):
        argument = args[index]
        lowered = argument.lower()
        if lowered in target_options:
            if index + 1 >= len(args):
                return []
            explicit_targets.append(args[index + 1])
            index += 2
            continue
        if argument.startswith("-"):
            if lowered in value_options:
                if index + 1 >= len(args):
                    return []
                index += 2
                continue
            if lowered in switch_options:
                index += 1
                continue
            return []
        positionals.append(argument)
        index += 1
    targets = explicit_targets or positionals[:1]
    return _project_static_command_targets(targets)


def _is_project_delete_command(command: str) -> bool:
    _, matched = _project_delete_command_targets(command)
    return matched


def _project_delete_command_targets(command: str) -> tuple[list[str], bool]:
    targets: list[str] = []
    matched = False
    for tokens in _project_command_segments(command):
        segment_targets, segment_matched = _parse_project_delete_tokens(tokens, 0)
        if not segment_matched:
            continue
        matched = True
        for target in segment_targets:
            if target and target not in targets:
                targets.append(target)
    return targets, matched


def _project_command_segments(command: str) -> list[list[str]]:
    segments: list[list[str]] = []
    current: list[str] = []
    token: list[str] = []
    quote = ""

    def flush_token() -> None:
        if token:
            current.append("".join(token))
            token.clear()

    def flush_segment() -> None:
        flush_token()
        if current:
            segments.append(list(current))
            current.clear()

    for char in command:
        if quote:
            if char == quote:
                quote = ""
            else:
                token.append(char)
            continue
        if char in {"'", '"'}:
            quote = char
        elif char in {";", "&", "|"}:
            flush_segment()
        elif char.isspace():
            flush_token()
        else:
            token.append(char)
    flush_segment()
    return segments


def _parse_project_delete_tokens(tokens: list[str], depth: int) -> tuple[list[str], bool]:
    if not tokens or depth > 2:
        return [], False
    index = 0
    if tokens[index].lower() == "sudo":
        index += 1
        while index < len(tokens):
            if tokens[index] == "--":
                index += 1
                break
            if not tokens[index].startswith("-"):
                break
            index += 1
    if index < len(tokens) and tokens[index].lower() == "command":
        index += 1
    if index >= len(tokens):
        return [], False
    executable = tokens[index].lower().removesuffix(".exe")
    if executable == "cmd":
        index += 1
        if index >= len(tokens) or tokens[index].lower() not in {"/c", "/k"}:
            return [], False
        return _parse_nested_project_delete_command(tokens[index + 1 :], depth)
    if executable in {"powershell", "pwsh"}:
        command_index = next(
            (
                candidate + 1
                for candidate in range(index + 1, len(tokens))
                if tokens[candidate].lower() in {"-command", "-c"}
            ),
            -1,
        )
        if command_index < 0:
            return [], False
        return _parse_nested_project_delete_command(tokens[command_index:], depth)
    if executable not in {"rm", "del", "erase", "rmdir", "rd", "remove-item"}:
        return [], False
    return _extract_project_delete_targets(executable, tokens[index + 1 :]), True


def _parse_nested_project_delete_command(tokens: list[str], depth: int) -> tuple[list[str], bool]:
    if not tokens:
        return [], False
    for nested in _project_command_segments(" ".join(tokens)):
        targets, matched = _parse_project_delete_tokens(nested, depth + 1)
        if matched:
            return targets, True
    return [], False


def _extract_project_delete_targets(command: str, args: list[str]) -> list[str]:
    targets: list[str] = []
    options_ended = False
    value_options = {"-filter", "-include", "-exclude", "-stream"}
    remove_item_switches = {"-confirm", "-force", "-recurse", "-verbose", "-whatif"}
    rm_switches = {
        "-d",
        "--dir",
        "-f",
        "--force",
        "-i",
        "--interactive",
        "--no-preserve-root",
        "--one-file-system",
        "--preserve-root",
        "-r",
        "--recursive",
        "-v",
        "--verbose",
    }
    windows_switches = {"/a", "/f", "/q", "/s"}
    index = 0
    while index < len(args):
        argument = args[index].strip()
        lowered = argument.lower()
        if not argument:
            index += 1
            continue
        if argument == "--":
            options_ended = True
            index += 1
            continue
        if command == "remove-item":
            if lowered in {"-path", "-literalpath"} and index + 1 < len(args):
                index += 1
                target = args[index].rstrip(",")
                if target and target not in targets:
                    targets.append(target)
                index += 1
                continue
            if not options_ended and argument.startswith("-"):
                if lowered in value_options and index + 1 < len(args):
                    index += 1
                elif lowered not in remove_item_switches:
                    return []
                index += 1
                continue
        elif not options_ended:
            if command == "rm" and argument.startswith("-"):
                if lowered not in rm_switches and not re.fullmatch(r"-[dfiIrvR]+", argument):
                    return []
                index += 1
                continue
            if command != "rm" and argument.startswith(("/", "-")):
                if lowered not in windows_switches and not lowered.startswith("/a:"):
                    return []
                index += 1
                continue
        target = _project_static_command_target(argument.rstrip(","))
        if target and target not in targets:
            targets.append(target)
        index += 1
    return targets


def _project_targets(payload: dict[str, Any]) -> list[str]:
    targets: list[str] = []

    def append_target(value: Any) -> None:
        if not isinstance(value, str):
            return
        target = value.strip()
        if target and target not in targets:
            targets.append(target)

    raw_targets = payload.get("targets")
    if isinstance(raw_targets, list):
        for target in raw_targets:
            append_target(target)
    content = str(payload.get("content") or "")
    tool = str(payload.get("tool") or "").strip().lower()
    if tool not in {"read", "grep", "glob"} and (tool in {"apply_patch", "applypatch"} or "*** Begin Patch" in content):
        for target in _extract_patch_targets(content):
            append_target(target)
    if _project_operation(payload) == "delete":
        delete_targets, delete_matched = _project_delete_command_targets(content)
        if delete_matched:
            for target in delete_targets:
                append_target(target)
    append_target(payload.get("target"))
    return targets


def _extract_patch_targets(content: str) -> list[str]:
    prefixes = ("*** Add File: ", "*** Delete File: ", "*** Update File: ", "*** Move to: ")
    targets: list[str] = []
    for raw_line in content.splitlines():
        line = raw_line.strip()
        for prefix in prefixes:
            if line.startswith(prefix):
                target = line[len(prefix) :].strip()
                if target and target not in targets:
                    targets.append(target)
                break
    return targets


def _extract_patch_target_operations(content: str) -> list[tuple[str, str]]:
    prefixes = (
        ("*** Add File: ", "write"),
        ("*** Update File: ", "write"),
        ("*** Move to: ", "write"),
        ("*** Delete File: ", "delete"),
    )
    operations: list[tuple[str, str]] = []
    for raw_line in content.splitlines():
        line = raw_line.strip()
        for prefix, operation in prefixes:
            if not line.startswith(prefix):
                continue
            target = line[len(prefix) :].strip()
            candidate = (target, operation)
            if target and candidate not in operations:
                operations.append(candidate)
            break
    return operations


def _project_relative_path(repo_root: str, target: str, working_directory: str) -> str | None:
    if not target or urllib.parse.urlsplit(target).scheme:
        return None
    root = Path(repo_root).resolve(strict=True)
    cwd = Path(working_directory).resolve(strict=False) if working_directory else root
    try:
        cwd.relative_to(root)
    except ValueError:
        cwd = root
    candidate = Path(target)
    if not candidate.is_absolute():
        candidate = cwd / candidate
    try:
        relative = candidate.resolve(strict=False).relative_to(root).as_posix()
    except (OSError, ValueError):
        return None
    return relative.casefold() if os.name == "nt" else relative


def _project_pattern_matches(pattern: str, relative: str) -> bool:
    normalized_pattern = pattern.casefold() if os.name == "nt" else pattern
    if normalized_pattern.endswith("/**"):
        prefix = normalized_pattern[:-3]
        return relative == prefix or relative.startswith(prefix + "/")
    return relative == normalized_pattern


def _is_project_network_write(payload: dict[str, Any]) -> bool:
    target = str(payload.get("target") or "").strip()
    parsed = urllib.parse.urlsplit(target)
    if parsed.scheme not in {"http", "https"} or not parsed.hostname:
        return False
    method = str(payload.get("networkMethod") or "").strip().upper()
    if method:
        return method not in {"GET", "HEAD", "OPTIONS"}
    return str(payload.get("actionType") or "").strip().lower() != "read"


def _project_network_host(target: str) -> str:
    parsed = urllib.parse.urlsplit(target)
    if not parsed.hostname:
        return ""
    host = parsed.hostname.lower()
    try:
        port = parsed.port
    except ValueError:
        return ""
    return f"{host}:{port}" if port else host


def _project_host_allowed(host: str, allowed: list[str]) -> bool:
    hostname = host.rsplit(":", 1)[0] if host.count(":") == 1 else host
    for pattern in allowed:
        if pattern in {host, hostname}:
            return True
        if pattern.startswith("*."):
            suffix = pattern[1:]
            if hostname.endswith(suffix) and hostname != suffix[1:]:
                return True
    return False


def _stricter_project_floor(current: dict[str, str] | None, candidate: dict[str, str]) -> dict[str, str]:
    if current is None:
        return candidate
    rank = {"ask": 1, "deny": 2}
    return candidate if rank.get(candidate["decision"], 0) >= rank.get(current["decision"], 0) else current
