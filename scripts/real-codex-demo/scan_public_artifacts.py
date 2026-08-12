#!/usr/bin/env python3
"""拒绝上传包含已知 Secret、VPS 标识或明显凭据模式的演示产物。"""

from __future__ import annotations

import argparse
import base64
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
        if len(value) < 4:
            continue
        raw = value.encode("utf-8")
        encoded_values.extend(
            [
                raw,
                base64.b64encode(raw),
                base64.b64encode(raw).rstrip(b"="),
                base64.urlsafe_b64encode(raw),
                base64.urlsafe_b64encode(raw).rstrip(b"="),
                raw.hex().encode("ascii"),
                urllib.parse.quote(value, safe="").encode("utf-8"),
                json.dumps(value).encode("utf-8")[1:-1],
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
