#!/usr/bin/env python3
"""拒绝上传包含已知 Secret、VPS 标识或明显凭据模式的演示产物。"""

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

ALLOWED_FILES = {
    "summary.json",
    "failure.json",
    "hook-trust.json",
    "audit.json",
    "postconditions.json",
    "cleanup.json",
    "transcript.txt",
    "codex-real-demo.cast",
    "manifest.json",
}
MAX_PUBLIC_FILE_SIZE = 2 * 1024 * 1024
SUCCESS_FILES = {
    "summary.json",
    "hook-trust.json",
    "audit.json",
    "postconditions.json",
    "cleanup.json",
    "transcript.txt",
    "codex-real-demo.cast",
    "manifest.json",
}
FAILURE_FILES = {"failure.json", "cleanup.json", "manifest.json"}


def validate_manifest(root: Path, file_names: set[str]) -> list[str]:
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


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path)
    args = parser.parse_args()
    root = args.input
    if not root.is_dir():
        print("公开演示产物目录不存在。", file=sys.stderr)
        return 2

    known_values = [
        os.environ.get(name, "")
        for name in (
            "ATG_DEMO_API_KEY",
            "ATG_DEMO_SSH_HOST",
            "ATG_DEMO_SSH_USER",
            "ATG_DEMO_SSH_PASSWORD",
            "ATG_DEMO_SSH_KNOWN_HOSTS",
        )
    ]
    encoded_values: list[bytes] = []
    for value in known_values:
        candidates = [value, *(line.strip() for line in value.splitlines())]
        for candidate in candidates:
            if len(candidate) < 4:
                continue
            raw = candidate.encode("utf-8")
            encoded_values.extend(
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
    forbidden_patterns = [
        re.compile(rb"(?i)\bsk-[A-Za-z0-9_-]{16,}\b"),
        re.compile(rb"(?i)\b(?:github_pat_|gh[pousr]_)[A-Za-z0-9_-]{12,}\b"),
        re.compile(rb"-----BEGIN (?:OPENSSH|RSA|EC|PRIVATE) PRIVATE KEY-----"),
        re.compile(
            rb"""(?ix)
            ["']?authorization["']?\s*[:=]\s*["']?
            (?:bearer\s+)?[A-Za-z0-9._~+/=-]{12,}
            """
        ),
        re.compile(rb"(?i)\bAKIA[A-Z0-9]{16}\b"),
        re.compile(rb"(?i)\bxox[baprs]-[A-Za-z0-9-]{12,}\b"),
        re.compile(rb"(?i)\beyJ[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{8,}\b"),
        re.compile(rb"(?i)(?:runner|github)[\\/](?:work|home|workspace)[\\/]"),
    ]

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
    if file_names not in (SUCCESS_FILES, FAILURE_FILES):
        failures.append("公开产物文件集合不符合成功或失败契约")
    elif "manifest.json" in file_names:
        failures.extend(validate_manifest(root, file_names))
    for path in files:
        content = path.read_bytes()
        if any(value in content for value in encoded_values):
            failures.append(f"{path.name}: 命中已知敏感值")
        if any(pattern.search(content) for pattern in forbidden_patterns):
            failures.append(f"{path.name}: 命中凭据格式")

    if failures:
        for failure in failures:
            print(failure, file=sys.stderr)
        return 1
    print(f"公开演示产物敏感扫描通过：{len(files)} 个文件")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
