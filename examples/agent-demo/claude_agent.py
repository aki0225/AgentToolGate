#!/usr/bin/env python3
"""Claude tool use demo for AgentToolGate."""

from __future__ import annotations

import argparse
import json
import os
import sys
from typing import Any

import anthropic
import requests
from dotenv import load_dotenv


TOOL_NAME_MAP = {
    "database_query": "database.query",
    "github_create_issue": "github.create_issue",
}


def env_value(name: str, default: str) -> str:
    value = os.getenv(name)
    return value.strip() if value and value.strip() else default


class AgentToolGateClient:
    def __init__(self) -> None:
        self.base_url = env_value("AGENTTOOLGATE_URL", "http://localhost:8080").rstrip("/")
        self.console_url = env_value("AGENTTOOLGATE_CONSOLE_URL", "http://localhost:5173").rstrip("/")
        self.workspace_org_id = env_value("AGENTTOOLGATE_WORKSPACE_ORG_ID", "local-org")
        self.timeout = float(env_value("AGENTTOOLGATE_TIMEOUT_SECONDS", "15"))
        self.token = os.getenv("AGENTTOOLGATE_TOKEN", "").strip()

    def call_tool(self, tool_name: str, arguments: dict[str, Any]) -> dict[str, Any]:
        gateway_tool = TOOL_NAME_MAP.get(tool_name)
        if not gateway_tool:
            return {"status": "failed", "error": f"未知工具: {tool_name}"}

        headers = {
            "Accept": "application/json",
            "Content-Type": "application/json",
            "X-Workspace-Org-Id": self.workspace_org_id,
        }
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"

        try:
            response = requests.post(
                f"{self.base_url}/api/tool-calls",
                headers=headers,
                json={"tool": gateway_tool, "arguments": arguments},
                timeout=self.timeout,
            )
            data = response.json() if response.content else {}
        except requests.RequestException as exc:
            return {"status": "failed", "error": f"调用 AgentToolGate 失败: {exc}"}
        except ValueError:
            return {"status": "failed", "error": "AgentToolGate 返回了非 JSON 响应"}

        if response.status_code >= 400:
            message = data.get("error") or data.get("message") or response.text
            return {"status": "failed", "error": message, "httpStatus": response.status_code}

        if data.get("status") == "approval_required":
            self.print_approval_hint(data)
        return data

    def print_approval_hint(self, data: dict[str, Any]) -> None:
        print("\n[需要人工审批]")
        print(f"approvalId: {data.get('approvalId', '')}")
        print(f"callId: {data.get('callId', '')}")
        print(f"请打开控制台审批: {self.console_url}/approvals")
        print("脚本不会自动审批，也不会调用 /api/approvals/*。")


def build_tools() -> list[dict[str, Any]]:
    return [
        {
            "name": "database_query",
            "description": (
                "通过 AgentToolGate REST API 调用 database.query。"
                "该工具只允许后端 SQL Guard 放行的只读 SELECT，返回审计后的查询结果。"
            ),
            "input_schema": {
                "type": "object",
                "properties": {
                    "datasource": {"type": "string", "description": "数据源名称，默认 local_postgres"},
                    "sql": {"type": "string", "description": "只读 SELECT SQL"},
                },
                "required": ["datasource", "sql"],
            },
        },
        {
            "name": "github_create_issue",
            "description": (
                "通过 AgentToolGate REST API 调用 github.create_issue。"
                "这是写入类工具，通常返回 approval_required；返回后必须让用户去 AgentToolGate 控制台人工审批。"
            ),
            "input_schema": {
                "type": "object",
                "properties": {
                    "owner": {"type": "string", "description": "GitHub owner，必须在后端 allowlist 内"},
                    "repo": {"type": "string", "description": "GitHub repo，必须在后端 allowlist 内"},
                    "title": {"type": "string", "description": "Issue 标题"},
                    "body": {"type": "string", "description": "Issue 正文"},
                },
                "required": ["owner", "repo", "title"],
            },
        },
    ]


def default_prompt() -> str:
    datasource = env_value("DATABASE_QUERY_DATASOURCE", "local_postgres")
    sql = env_value("DATABASE_QUERY_SQL", "SELECT namespace, name, operation_type FROM public.tools")
    owner = env_value("GITHUB_OWNER", "acme")
    repo = env_value("GITHUB_REPO", "demo")
    title = env_value("GITHUB_ISSUE_TITLE", "AgentToolGate demo approval request")
    body = env_value("GITHUB_ISSUE_BODY", "Created by AgentToolGate Claude demo after human approval.")
    return (
        "请完成一个 AgentToolGate 端到端演示：\n"
        f"1. 先调用 database_query，datasource={datasource}，sql={sql!r}。\n"
        f"2. 再调用 github_create_issue，owner={owner}，repo={repo}，title={title!r}，body={body!r}。\n"
        "如果 GitHub 工具返回 approval_required，只说明需要人工审批，不要尝试自动审批。"
    )


def block_to_dict(block: Any) -> dict[str, Any]:
    if isinstance(block, dict):
        return block
    if hasattr(block, "model_dump"):
        return block.model_dump(exclude_none=True)
    return {"type": getattr(block, "type", "text"), "text": str(block)}


def extract_text(content: list[Any]) -> str:
    parts: list[str] = []
    for block in content:
        block_type = getattr(block, "type", None)
        if block_type == "text":
            parts.append(getattr(block, "text", ""))
    return "\n".join(part for part in parts if part)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Claude AgentToolGate REST demo")
    parser.add_argument("--prompt", default=default_prompt(), help="覆盖默认演示提示词")
    parser.add_argument("--max-turns", type=int, default=4, help="最多模型往返次数")
    return parser.parse_args()


def main() -> int:
    load_dotenv()
    args = parse_args()
    if not os.getenv("ANTHROPIC_API_KEY"):
        print("缺少 ANTHROPIC_API_KEY，请先复制 .env.example 为 .env 并填写。", file=sys.stderr)
        return 2

    client = anthropic.Anthropic()
    gateway = AgentToolGateClient()
    model = env_value("ANTHROPIC_MODEL", "claude-sonnet-4-5")
    messages: list[dict[str, Any]] = [{"role": "user", "content": args.prompt}]
    tools = build_tools()

    for _ in range(args.max_turns):
        response = client.messages.create(
            model=model,
            max_tokens=1200,
            system=(
                "你是 AgentToolGate 演示代理。需要访问数据库或 GitHub 时必须使用提供的工具，"
                "不要伪造工具结果。遇到 approval_required 时停止推进并提示人工审批。"
            ),
            messages=messages,
            tools=tools,
            tool_choice={"type": "auto"},
        )

        tool_uses = [block for block in response.content if getattr(block, "type", None) == "tool_use"]
        if not tool_uses:
            print(extract_text(response.content))
            return 0

        should_stop_for_approval = False
        tool_results: list[dict[str, Any]] = []
        for tool_use in tool_uses:
            tool_name = getattr(tool_use, "name", "")
            tool_input = getattr(tool_use, "input", {}) or {}
            print(f"\n[调用工具] {tool_name}")
            result = gateway.call_tool(tool_name, dict(tool_input))
            if result.get("status") == "approval_required":
                should_stop_for_approval = True

            tool_results.append(
                {
                    "type": "tool_result",
                    "tool_use_id": getattr(tool_use, "id", ""),
                    "content": json.dumps(result, ensure_ascii=False),
                    "is_error": result.get("status") in {"failed", "denied"},
                }
            )

        if should_stop_for_approval:
            return 0

        # Claude 要求 tool_result 紧跟包含 tool_use 的 assistant 消息。
        messages.append({"role": "assistant", "content": [block_to_dict(block) for block in response.content]})
        messages.append({"role": "user", "content": tool_results})

    print("达到 max-turns，演示提前结束。", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
