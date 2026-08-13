#!/usr/bin/env python3
"""拒绝上传包含已知 Secret、宿主标识或明显凭据模式的演示产物。"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import re
import sys
import urllib.parse
from pathlib import Path

V1_SUCCESS_FILES = {
    "summary.json",
    "hook-trust.json",
    "audit.json",
    "postconditions.json",
    "cleanup.json",
    "transcript.txt",
    "codex-real-demo.cast",
    "manifest.json",
}
V2_SUCCESS_FILES = {
    "summary.json",
    "hook-trust.json",
    "audit.json",
    "postconditions.json",
    "cleanup.json",
    "transcript.txt",
    "scenario-low-friction.cast",
    "scenario-sensitive-read.cast",
    "scenario-destructive-delete.cast",
    "scenario-network-egress.cast",
    "scenario-protected-write.cast",
    "manifest.json",
}
FAILURE_FILES = {"failure.json", "cleanup.json", "manifest.json"}
ALLOWED_FILES = V1_SUCCESS_FILES | V2_SUCCESS_FILES | FAILURE_FILES
MAX_PUBLIC_FILE_SIZE = 2 * 1024 * 1024

KNOWN_SECRET_ENV_NAMES = (
    "ATG_DEMO_API_KEY",
    "ATG_DEMO_SSH_HOST",
    "ATG_DEMO_SSH_USER",
    "ATG_DEMO_SSH_PASSWORD",
    "ATG_DEMO_SSH_KNOWN_HOSTS",
)
SYNTHETIC_SECRET_MARKERS: tuple[str, ...] = ()


def forbidden_patterns(contract_name: str | None) -> list[re.Pattern[bytes]]:
    patterns = [
        re.compile(rb"(?i)\bsk-[A-Za-z0-9_-]{16,}\b"),
        re.compile(rb"(?i)\b(?:github_pat_|gh[pousr]_)[A-Za-z0-9_-]{12,}\b"),
        re.compile(rb"-----BEGIN (?:OPENSSH|RSA|EC|DSA|PRIVATE) PRIVATE KEY-----"),
        re.compile(
            rb"""(?ix)
            ["']?authorization["']?\s*[:=]\s*["']?
            (?:bearer\s+)?[A-Za-z0-9._~+/=-]{12,}
            """
        ),
        re.compile(rb"(?i)\bAKIA[A-Z0-9]{16}\b"),
        re.compile(rb"(?i)\bxox[baprs]-[A-Za-z0-9-]{12,}\b"),
        re.compile(
            rb"(?i)\beyJ[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{12,}\."
            rb"[A-Za-z0-9_-]{8,}\b"
        ),
        # Runner、临时目录与常见宿主绝对路径均不应进入公开证据。
        re.compile(rb"(?i)(?:runner|github)[\\/](?:work|home|workspace)[\\/]"),
        re.compile(rb"(?i)(?:[A-Z]:\\Users\\|/home/[^/\s]+/|/Users/[^/\s]+/)"),
        re.compile(rb"(?i)(?:/tmp|/private/tmp|[A-Z]:\\(?:Temp|Windows\\Temp))[\\/]"),
        re.compile(rb"(?i)(?:provider\.key|known_hosts|askpass\.sh)"),
        re.compile(rb"(?i)ATG_SYNTHETIC_[A-Z0-9_]*DO_NOT_PUBLISH[A-Z0-9_]*"),
        re.compile(rb"(?i)ATG_SYNTHETIC_SSH_SECRET_[A-F0-9]{24,}"),
        re.compile(rb"(?i)synthetic_secret\s*=\s*[A-Za-z0-9._-]{4,}"),
    ]
    if contract_name == "v2-success":
        patterns.append(
            re.compile(
                rb"(?i)[A-Z]:[\\/]+"
                rb"(?:Program Files[\\/]+PowerShell|Windows[\\/]+System32[\\/]+WindowsPowerShell)"
                rb"[\\/]+[^\"\r\n]*(?:pwsh|powershell)(?:\.exe)?"
            )
        )
    return patterns


def validate_manifest(
    root: Path,
    file_names: set[str],
    expected_schema_version: str,
) -> list[str]:
    failures: list[str] = []
    manifest_path = root / "manifest.json"
    try:
        document = json.loads(manifest_path.read_text(encoding="utf-8"))
        entries = document.get("files", [])
        manifest_entries = {
            str(item["path"]): item
            for item in entries
            if isinstance(item, dict) and "path" in item
        }
    except (OSError, UnicodeDecodeError, json.JSONDecodeError, KeyError, TypeError):
        return ["manifest.json: 格式无效"]

    if document.get("schemaVersion") != expected_schema_version:
        failures.append(
            "manifest.json: schemaVersion 与公开产物契约不一致"
        )
    if not isinstance(entries, list) or len(entries) != len(manifest_entries):
        failures.append("manifest.json: 文件条目格式无效或存在重复路径")
    expected_names = file_names - {"manifest.json"}
    if set(manifest_entries) != expected_names:
        failures.append("manifest.json: 文件集合与实际产物不一致")
        return failures
    for name in sorted(expected_names):
        content = (root / name).read_bytes()
        entry = manifest_entries[name]
        if entry.get("size") != len(content):
            failures.append(f"manifest.json: {name} 大小不一致")
        if entry.get("sha256") != hashlib.sha256(content).hexdigest():
            failures.append(f"manifest.json: {name} 哈希不一致")
    return failures


def encoded_candidates(value: str) -> list[bytes]:
    candidates: list[bytes] = []
    for candidate in (value, *(line.strip() for line in value.splitlines())):
        if len(candidate) < 4:
            continue
        raw = candidate.encode("utf-8")
        candidates.extend(
            [
                raw,
                base64.b64encode(raw),
                base64.b64encode(raw).rstrip(b"="),
                base64.urlsafe_b64encode(raw),
                base64.urlsafe_b64encode(raw).rstrip(b"="),
                raw.hex().encode("ascii"),
                urllib.parse.quote(candidate, safe="").encode("utf-8"),
                json.dumps(candidate).encode("utf-8")[1:-1],
            ]
        )
    return candidates


def detect_contract(file_names: set[str]) -> tuple[str, str] | None:
    contracts = (
        ("v1-success", "v1", V1_SUCCESS_FILES),
        ("v2-success", "v2", V2_SUCCESS_FILES),
        ("failure", "", FAILURE_FILES),
    )
    matches = [
        (name, schema_version)
        for name, schema_version, expected_files in contracts
        if file_names == expected_files
    ]
    if len(matches) != 1:
        return None
    return matches[0]


def json_schema_version(root: Path, name: str) -> str | None:
    try:
        document = json.loads((root / name).read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError):
        return None
    value = document.get("schemaVersion")
    return value if isinstance(value, str) else None


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path)
    args = parser.parse_args()
    root = args.input
    if not root.is_dir():
        print("公开演示产物目录不存在。", file=sys.stderr)
        return 2

    encoded_values: list[bytes] = []
    for name in KNOWN_SECRET_ENV_NAMES:
        value = os.environ.get(name, "")
        if value:
            encoded_values.extend(encoded_candidates(value))
    for marker in SYNTHETIC_SECRET_MARKERS:
        encoded_values.extend(encoded_candidates(marker))

    failures: list[str] = []
    files: list[Path] = []
    for path in root.iterdir():
        if path.is_symlink() or not path.is_file():
            failures.append(f"{path.name}: 不是普通公开文件")
            continue
        if path.name not in ALLOWED_FILES:
            failures.append(f"{path.name}: 不在公开白名单")
        if path.stat().st_size > MAX_PUBLIC_FILE_SIZE:
            failures.append(f"{path.name}: 文件过大")
        files.append(path)
    if not files:
        failures.append("公开产物目录为空")

    file_names = {path.name for path in files}
    contract = detect_contract(file_names)
    contract_name: str | None = None
    if contract is None:
        failures.append("公开产物文件集合不符合 v1、v2 或失败契约")
    else:
        contract_name, expected_schema = contract
        if contract_name == "failure":
            expected_schema = json_schema_version(root, "failure.json") or ""
            if expected_schema not in {"v1", "v2"}:
                failures.append("failure.json: schemaVersion 必须是 v1 或 v2")
        if expected_schema:
            failures.extend(validate_manifest(root, file_names, expected_schema))
            for name in sorted(file_names & {
                "summary.json",
                "hook-trust.json",
                "audit.json",
                "postconditions.json",
                "cleanup.json",
            }):
                if json_schema_version(root, name) != expected_schema:
                    failures.append(
                        f"{name}: schemaVersion 与 {contract_name} 契约不一致"
                    )

    for path in files:
        content = path.read_bytes()
        if any(value in content for value in encoded_values):
            failures.append(f"{path.name}: 命中已知或 synthetic 敏感值")
        if any(pattern.search(content) for pattern in forbidden_patterns(contract_name)):
            failures.append(f"{path.name}: 命中凭据或宿主路径格式")

    if failures:
        for failure in failures:
            print(failure, file=sys.stderr)
        return 1
    print(f"公开演示产物敏感扫描通过：{len(files)} 个文件")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
