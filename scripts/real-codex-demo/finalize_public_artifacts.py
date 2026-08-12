#!/usr/bin/env python3
"""在私有目录与 SSH 隧道清理后，补写公开后置条件并刷新 manifest。"""

from __future__ import annotations

import argparse
import hashlib
import json
import socket
import sys
from datetime import UTC, datetime
from pathlib import Path

SUCCESS_FILES = {
    "summary.json",
    "hook-trust.json",
    "audit.json",
    "postconditions.json",
    "transcript.txt",
    "codex-real-demo.cast",
}
FAILURE_FILES = {"failure.json"}
MAX_PUBLIC_FILE_SIZE = 2 * 1024 * 1024


def port_is_listening(port: int) -> bool:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as connection:
        connection.settimeout(0.5)
        return connection.connect_ex(("127.0.0.1", port)) == 0


def write_json(path: Path, value: object) -> None:
    path.write_text(
        json.dumps(value, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
        newline="\n",
    )


def refresh_manifest(root: Path) -> None:
    entries = []
    for path in sorted(root.iterdir()):
        if not path.is_file() or path.name == "manifest.json":
            continue
        content = path.read_bytes()
        entries.append(
            {
                "path": path.name,
                "size": len(content),
                "sha256": hashlib.sha256(content).hexdigest(),
            }
        )
    write_json(
        root / "manifest.json",
        {
            "schemaVersion": "v1",
            "generatedAt": datetime.now(UTC).isoformat().replace("+00:00", "Z"),
            "files": entries,
        },
    )


def validate_public_artifact_contract(root: Path) -> None:
    regular_files: set[str] = set()
    for path in root.iterdir():
        if path.is_symlink() or not path.is_file():
            raise ValueError("公开产物目录包含非普通文件")
        if path.stat().st_size > MAX_PUBLIC_FILE_SIZE:
            raise ValueError(f"公开产物过大：{path.name}")
        regular_files.add(path.name)

    has_success = SUCCESS_FILES.issubset(regular_files)
    has_failure = FAILURE_FILES.issubset(regular_files)
    if has_success == has_failure:
        raise ValueError("公开产物必须且只能包含成功或失败证据契约")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--private-root", required=True, type=Path)
    parser.add_argument("--ssh-dir", required=True, type=Path)
    parser.add_argument("--tunnel-port", required=True, type=int)
    args = parser.parse_args()

    if not args.input.is_dir():
        print("公开演示产物目录不存在。", file=sys.stderr)
        return 2
    try:
        validate_public_artifact_contract(args.input)
    except ValueError as error:
        print(str(error), file=sys.stderr)
        return 2
    checks = {
        "privateRootAbsent": not args.private_root.exists(),
        "sshWorkingDirectoryAbsent": not args.ssh_dir.exists(),
        "sshTunnelPortListeningAfterCleanup": port_is_listening(args.tunnel_port),
    }
    write_json(
        args.input / "cleanup.json",
        {
            "schemaVersion": "v1",
            "checkedAt": datetime.now(UTC).isoformat().replace("+00:00", "Z"),
            "checks": checks,
        },
    )
    refresh_manifest(args.input)

    failed = [
        name
        for name, value in checks.items()
        if (name.endswith("Absent") and not value)
        or (name.endswith("AfterCleanup") and value)
    ]
    if failed:
        print("清理后置条件失败：" + ", ".join(failed), file=sys.stderr)
        return 1
    print("私有目录与 SSH 隧道清理后置条件通过。")
    return 0
