#!/usr/bin/env python3
"""在私有目录与网络入口清理后，补写公开后置条件并刷新 manifest。"""

from __future__ import annotations

import argparse
import hashlib
import json
import socket
import sys
from datetime import UTC, datetime
from pathlib import Path

V1_SUCCESS_FILES = {
    "summary.json",
    "hook-trust.json",
    "audit.json",
    "postconditions.json",
    "transcript.txt",
    "codex-real-demo.cast",
}
V2_SCENARIO_CASTS = {
    "scenario-low-friction.cast",
    "scenario-sensitive-read.cast",
    "scenario-destructive-delete.cast",
    "scenario-network-egress.cast",
    "scenario-protected-write.cast",
}
V2_SUCCESS_FILES = {
    "summary.json",
    "hook-trust.json",
    "audit.json",
    "postconditions.json",
    "transcript.txt",
    *V2_SCENARIO_CASTS,
}
V1_PRE_FINALIZE_FILES = V1_SUCCESS_FILES - {"cleanup.json", "manifest.json"}
V2_PRE_FINALIZE_FILES = V2_SUCCESS_FILES - {"cleanup.json", "manifest.json"}
# 保留旧名称，避免既有测试和外部脚本失去 v1 兼容性。
SUCCESS_FILES = V1_SUCCESS_FILES
FAILURE_FILES = {"failure.json"}
MAX_PUBLIC_FILE_SIZE = 2 * 1024 * 1024
ALLOWED_FILES = (
    V1_SUCCESS_FILES
    | V2_SUCCESS_FILES
    | FAILURE_FILES
    | {"cleanup.json", "manifest.json"}
)


def port_is_listening(port: int | None) -> bool:
    if port is None:
        return False
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as connection:
        connection.settimeout(0.5)
        return connection.connect_ex(("127.0.0.1", port)) == 0


def write_json(path: Path, value: object) -> None:
    path.write_text(
        json.dumps(value, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
        newline="\n",
    )


def refresh_manifest(root: Path, schema_version: str = "v1") -> None:
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
            "schemaVersion": schema_version,
            "generatedAt": datetime.now(UTC).isoformat().replace("+00:00", "Z"),
            "files": entries,
        },
    )


def validate_public_artifact_contract(root: Path) -> str:
    regular_files: set[str] = set()
    for path in root.iterdir():
        if path.is_symlink() or not path.is_file():
            raise ValueError("公开产物目录包含非普通文件")
        if path.stat().st_size > MAX_PUBLIC_FILE_SIZE:
            raise ValueError(f"公开产物过大：{path.name}")
        if path.name not in ALLOWED_FILES:
            raise ValueError(f"公开产物不在白名单：{path.name}")
        regular_files.add(path.name)

    success_contracts = [
        ("success", V1_PRE_FINALIZE_FILES),
        ("success-v2", V2_PRE_FINALIZE_FILES),
    ]
    matching_success = [
        mode for mode, required_files in success_contracts
        if required_files.issubset(regular_files)
    ]
    has_failure = FAILURE_FILES.issubset(regular_files)
    if len(matching_success) + int(has_failure) != 1:
        raise ValueError("公开产物必须且只能包含一套完整成功或失败证据契约")
    if has_failure:
        return "failure"
    return matching_success[0]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--private-root", required=True, type=Path)
    parser.add_argument("--ssh-dir", required=True, type=Path)
    parser.add_argument("--tunnel-port", required=True, type=int)
    parser.add_argument(
        "--atg-port",
        type=int,
        help="AgentToolGate 回环端口；v2 工作流和失败路径必须传入，v1 调用可省略。",
    )
    parser.add_argument(
        "--collector-port",
        type=int,
        help="网络外传观察器回环端口；v2 工作流和失败路径必须传入，v1 调用可省略。",
    )
    args = parser.parse_args()

    if not args.input.is_dir():
        print("公开演示产物目录不存在。", file=sys.stderr)
        return 2
    try:
        artifact_mode = validate_public_artifact_contract(args.input)
    except ValueError as error:
        print(str(error), file=sys.stderr)
        return 2
    is_v2 = artifact_mode == "success-v2"
    existing_failure_schema = "v1"
    if artifact_mode == "failure":
        try:
            failure_document = json.loads(
                (args.input / "failure.json").read_text(encoding="utf-8")
            )
            if failure_document.get("schemaVersion") == "v2":
                existing_failure_schema = "v2"
        except (OSError, UnicodeDecodeError, json.JSONDecodeError):
            pass
    if (
        (artifact_mode == "success-v2" or existing_failure_schema == "v2")
        and (args.atg_port is None or args.collector_port is None)
    ):
        print(
            "v2 工作流成功或失败收尾必须传入 --atg-port 和 --collector-port。",
            file=sys.stderr,
        )
        return 2
    if artifact_mode == "failure":
        for path in args.input.iterdir():
            if path.name not in FAILURE_FILES:
                path.unlink()

    checks = {
        "privateRootAbsent": not args.private_root.exists(),
        "sshWorkingDirectoryAbsent": not args.ssh_dir.exists(),
        "sshTunnelPortListeningAfterCleanup": port_is_listening(args.tunnel_port),
    }
    if (
        artifact_mode == "success-v2"
        or existing_failure_schema == "v2"
        or args.atg_port is not None
    ):
        checks["agentToolGatePortListeningAfterCleanup"] = port_is_listening(
            args.atg_port
        )
    if (
        artifact_mode == "success-v2"
        or existing_failure_schema == "v2"
        or args.collector_port is not None
    ):
        checks["collectorPortListeningAfterCleanup"] = port_is_listening(
            args.collector_port
        )

    schema_version = (
        "v2" if is_v2 or existing_failure_schema == "v2" else "v1"
    )
    write_json(
        args.input / "cleanup.json",
        {
            "schemaVersion": schema_version,
            "checkedAt": datetime.now(UTC).isoformat().replace("+00:00", "Z"),
            "checks": checks,
        },
    )
    refresh_manifest(args.input, schema_version)

    failed = [
        name
        for name, value in checks.items()
        if (name.endswith("Absent") and not value)
        or (name.endswith("AfterCleanup") and value)
    ]
    if failed:
        print("清理后置条件失败：" + ", ".join(failed), file=sys.stderr)
        return 1
    print("私有目录与回环网络入口清理后置条件通过。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
