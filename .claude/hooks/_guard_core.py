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
    exact_files = (
        ".env",
        ".claude/settings.json",
        ".codex/hooks.json",
        "configs/policies.yaml",
    )
    if any(path_matches_exact_file(lowered, item) for item in exact_files):
        return True
    dirs = (
        ".ssh",
        ".git/hooks",
        ".claude/hooks",
        ".codex/hooks",
        "backend/cmd/server",
        "cmd/server",
    )
    if any(path_matches_dir_or_descendant(lowered, item) for item in dirs):
        return True
    parts = path_segments(lowered)
    sensitive_segments = (
        "startup",
        "credentials",
        "secrets",
        "taskschd",
    )
    if any(segment in parts for segment in sensitive_segments):
        return True
    sensitive_sequences = (
        ("windows", "system32", "config"),
        ("software", "microsoft", "windows", "currentversion", "run"),
        ("software", "microsoft", "windows", "currentversion", "runonce"),
        ("windows", "system32", "tasks"),
    )
    return any(has_sequence(parts, list(sequence)) for sequence in sensitive_sequences)


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


def is_high_risk_offline_target(payload: dict[str, object]) -> bool:
    target = str(payload.get("target") or "").strip()
    content = str(payload.get("content") or "").strip()
    return (
        is_probably_high_risk_target(target)
        or is_probably_high_risk_target(content)
        or contains_hidden_script_features(content)
        or contains_hidden_script_features_in_decoded_base64(content)
    )
