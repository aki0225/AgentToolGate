#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""运行 Hook 兼容测试，并在 GitHub Actions 失败时输出可公开审阅的诊断。"""

from __future__ import annotations

import os
import subprocess
import sys
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
TEST_SUITES = (
    ("Codex Hook", REPO_ROOT / ".codex" / "hooks" / "test_agent_guard_pretool.py"),
    ("Claude Hook", REPO_ROOT / ".claude" / "hooks" / "test_agent_guard_pretool.py"),
)
MAX_ANNOTATION_CHARS = 16_000
MAX_INLINE_ANNOTATION_CHARS = 3_000


def configure_console_encoding() -> None:
    """确保 Windows runner 能打印包含中文断言消息的失败输出。"""
    for stream in (sys.stdout, sys.stderr):
        reconfigure = getattr(stream, "reconfigure", None)
        if callable(reconfigure):
            reconfigure(encoding="utf-8", errors="replace")


def sanitize_output(output: str) -> str:
    """隐藏 runner 工作区绝对路径，并限制失败注释体积。"""
    sanitized = output.replace(str(REPO_ROOT), "<WORKSPACE>")
    if len(sanitized) <= MAX_ANNOTATION_CHARS:
        return sanitized
    return "[前文已截断]\n" + sanitized[-MAX_ANNOTATION_CHARS:]


def escape_github_command(value: str) -> str:
    """按 GitHub workflow command 约定转义注释内容。"""
    return value.replace("%", "%25").replace("\r", "%0D").replace("\n", "%0A")


def compact_annotation(output: str) -> str:
    """提取失败尾部，避免超长 workflow command 被 runner 丢弃。"""
    lines = [line.strip() for line in output.splitlines() if line.strip()]
    compact = " | ".join(lines[-24:])
    if len(compact) <= MAX_INLINE_ANNOTATION_CHARS:
        return compact
    return "[前文已截断] " + compact[-MAX_INLINE_ANNOTATION_CHARS:]


def append_github_summary(label: str, output: str) -> None:
    """把完整的受限诊断写到公开 job summary，便于无日志权限时审阅。"""
    summary_path = os.environ.get("GITHUB_STEP_SUMMARY", "").strip()
    if not summary_path:
        return
    with Path(summary_path).open("a", encoding="utf-8", newline="\n") as summary:
        summary.write(f"### {label} 兼容测试失败\n\n")
        summary.write("```text\n")
        summary.write(output.replace("```", "`` `"))
        if not output.endswith("\n"):
            summary.write("\n")
        summary.write("```\n\n")


def run_suite(label: str, script_path: Path) -> int:
    env = dict(os.environ)
    # Windows runner 重定向输出时编码可能不同；固定 UTF-8 只影响测试日志，不改变 Hook 判定。
    env["PYTHONIOENCODING"] = "utf-8"
    env["PYTHONUTF8"] = "1"
    result = subprocess.run(
        [sys.executable, str(script_path), "-v"],
        cwd=REPO_ROOT,
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
    )
    print(result.stdout, end="")
    if result.returncode != 0 and os.environ.get("GITHUB_ACTIONS") == "true":
        diagnostic = sanitize_output(result.stdout)
        append_github_summary(label, diagnostic)
        inline = compact_annotation(diagnostic)
        print(f"::error title={label} 兼容测试失败::{escape_github_command(inline)}")
    return result.returncode


def main() -> int:
    failed = False
    for label, script_path in TEST_SUITES:
        failed = run_suite(label, script_path) != 0 or failed
    return 1 if failed else 0


if __name__ == "__main__":
    configure_console_encoding()
    raise SystemExit(main())
