#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Local Action Firewall 共享离线判定核心。"""

from __future__ import annotations

import base64
import binascii


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
