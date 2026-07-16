#!/usr/bin/env python3
"""OpenAI function calling demo for AgentToolGate."""

from __future__ import annotations

import argparse
import json
import os
import sys
from typing import Any

import requests
from dotenv import load_dotenv
from openai import OpenAI


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

        payload = {"tool": gateway_tool, "arguments": arguments}
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
                json=payload,
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
        approval_id = data.get("approvalId", "")
        call_id = data.get("callId", "")
        print("\n[需要人工审批]")
        print(f"approvalId: {approval_id}")
        print(f"callId: {call_id}")
        print(f"请打开控制台审批: {self.console_url}/approvals")
        print("脚本不会自动审批，也不会调用 /api/approvals/*。")


def build_tools() -> list[dict[str, Any]]:
    return [
        {
            "type": "function",
            "function": {
                "name": "database_query",
                "description": (
                    "通过 AgentToolGate REST API 调用 database.query。"
                    "只能执行受后端 SQL Guard 保护的只读 SELECT 查询。"
                ),
                "parameters": {
                    "type": "object",
                    "properties": {
                        "datasource": {"type": "string", "description": "数据源名称，默认 local_postgres"},
                        "sql": {"type": "string", "description": "只读 SELECT SQL"},
                    },
                    "required": ["datasource", "sql"],
                    "additionalProperties": False,
                },
            },
        },
        {
            "type": "function",
            "function": {
                "name": "github_create_issue",
                "description": (
                    "通过 AgentToolGate REST API 调用 github.create_issue。"
                    "这是写入类工具，正常会返回 approval_required，必须等待用户到控制台人工审批。"
                ),
                "parameters": {
                    "type": "object",
                    "properties": {
                        "owner": {"type": "string", "description": "GitHub owner，必须在后端 allowlist 内"},
                        "repo": {"type": "string", "description": "GitHub repo，必须在后端 allowlist 内"},
                        "title": {"type": "string", "description": "Issue 标题"},
                        "body": {"type": "string", "description": "Issue 正文"},
                    },
                    "required": ["owner", "repo", "title"],
                    "additionalProperties": False,
                },
            },
        },
    ]


def default_prompt() -> str:
    datasource = env_value("DATABASE_QUERY_DATASOURCE", "local_postgres")
    sql = env_value("DATABASE_QUERY_SQL", "SELECT namespace, name, operation_type FROM public.tools")
    owner = env_value("GITHUB_OWNER", "acme")
    repo = env_value("GITHUB_REPO", "demo")
    title = env_value("GITHUB_ISSUE_TITLE", "AgentToolGate demo approval request")
    body = env_value("GITHUB_ISSUE_BODY", "Created by AgentToolGate OpenAI demo after human approval.")
    return (
        "请完成一个 AgentToolGate 端到端演示：\n"
        f"1. 先调用 database_query，datasource={datasource}，sql={sql!r}。\n"
        f"2. 再调用 github_create_issue，owner={owner}，repo={repo}，title={title!r}，body={body!r}。\n"
        "如果 GitHub 工具返回 approval_required，只说明需要人工审批，不要尝试自动审批。"
    )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="OpenAI AgentToolGate REST demo")
    parser.add_argument("--prompt", default=default_prompt(), help="覆盖默认演示提示词")
    parser.add_argument("--max-turns", type=int, default=4, help="最多模型往返次数")
    return parser.parse_args()


def main() -> int:
    load_dotenv()
    args = parse_args()
    if not os.getenv("OPENAI_API_KEY"):
        print("缺少 OPENAI_API_KEY，请先复制 .env.example 为 .env 并填写。", file=sys.stderr)
        return 2

    client = OpenAI()
    gateway = AgentToolGateClient()
    model = env_value("OPENAI_MODEL", "gpt-4.1-mini")
    tools = build_tools()
    messages: list[Any] = [
        {
            "role": "system",
            "content": (
                "你是 AgentToolGate 演示代理。需要访问数据库或 GitHub 时必须使用提供的工具，"
                "不要伪造工具结果。遇到 approval_required 时停止推进并提示人工审批。"
            ),
        },
        {"role": "user", "content": args.prompt},
    ]

    for _ in range(args.max_turns):
        completion = client.chat.completions.create(
            model=model,
            messages=messages,
            tools=tools,
            tool_choice="auto",
            parallel_tool_calls=False,
        )
        message = completion.choices[0].message
        tool_calls = message.tool_calls or []
        if not tool_calls:
            print(message.content or "")
            return 0

        messages.append(message.model_dump(exclude_none=True))
        should_stop_for_approval = False
        for tool_call in tool_calls:
            try:
                arguments = json.loads(tool_call.function.arguments or "{}")
            except json.JSONDecodeError as exc:
                arguments = {}
                result = {"status": "failed", "error": f"工具参数不是合法 JSON: {exc}"}
            else:
                print(f"\n[调用工具] {tool_call.function.name}")
                result = gateway.call_tool(tool_call.function.name, arguments)

            if result.get("status") == "approval_required":
                should_stop_for_approval = True

            messages.append(
                {
                    "role": "tool",
                    "tool_call_id": tool_call.id,
                    "content": json.dumps(result, ensure_ascii=False),
                }
            )

        if should_stop_for_approval:
            return 0

    print("达到 max-turns，演示提前结束。", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
