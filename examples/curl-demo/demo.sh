#!/usr/bin/env bash
set -euo pipefail

# curl 端到端演示：mock.echo -> database.query -> github.create_issue 审批请求。
# 默认面向 docker compose local auth，不会自动审批 GitHub 写入操作。
# 如果后端未启动，高危落点会 fail-closed，普通 workspace 动作会 fail-open 并记录 pending audit。

API_URL="${AGENTTOOLGATE_URL:-http://localhost:8080}"
CONSOLE_URL="${AGENTTOOLGATE_CONSOLE_URL:-http://localhost:5173}"
WORKSPACE_ORG_ID="${AGENTTOOLGATE_WORKSPACE_ORG_ID:-local-org}"
TOKEN="${AGENTTOOLGATE_TOKEN:-}"

GITHUB_OWNER="${GITHUB_OWNER:-acme}"
GITHUB_REPO="${GITHUB_REPO:-demo}"
GITHUB_ISSUE_TITLE="${GITHUB_ISSUE_TITLE:-AgentToolGate curl demo approval request}"
GITHUB_ISSUE_BODY="${GITHUB_ISSUE_BODY:-Created by examples/curl-demo/demo.sh after human approval.}"
DATABASE_QUERY_DATASOURCE="${DATABASE_QUERY_DATASOURCE:-local_postgres}"
DATABASE_QUERY_SQL="${DATABASE_QUERY_SQL:-SELECT namespace, name, operation_type FROM public.tools}"

die() {
  echo "错误: $*" >&2
  exit 1
}

resolve_python() {
  if command -v python3 >/dev/null 2>&1; then
    echo "python3"
    return
  fi
  if command -v python >/dev/null 2>&1; then
    echo "python"
    return
  fi
  die "需要 python3 或 python 来安全生成/解析 JSON"
}

PYTHON_BIN="$(resolve_python)"

json_field() {
  local field="$1"
  "$PYTHON_BIN" -c '
import json
import sys

data = json.load(sys.stdin)
value = data
for part in sys.argv[1].split("."):
    if isinstance(value, dict):
        value = value.get(part)
    else:
        value = None
        break
if value is None:
    sys.exit(0)
if isinstance(value, str):
    print(value)
else:
    print(json.dumps(value, ensure_ascii=False))
' "$field"
}

build_payload() {
  local tool="$1"
  shift
  "$PYTHON_BIN" - "$tool" "$@" <<'PY'
import json
import sys

tool = sys.argv[1]
args = dict(item.split("=", 1) for item in sys.argv[2:])
if tool == "mock.echo":
    arguments = {"message": args["message"]}
elif tool == "database.query":
    arguments = {"datasource": args["datasource"], "sql": args["sql"]}
elif tool == "github.create_issue":
    arguments = {
        "owner": args["owner"],
        "repo": args["repo"],
        "title": args["title"],
        "body": args["body"],
    }
else:
    raise SystemExit(f"unsupported tool: {tool}")
print(json.dumps({"tool": tool, "arguments": arguments}, ensure_ascii=False))
PY
}

HEADERS=(
  -H "Accept: application/json"
  -H "Content-Type: application/json"
  -H "X-Workspace-Org-Id: ${WORKSPACE_ORG_ID}"
)

if [[ -n "${TOKEN}" ]]; then
  HEADERS+=(-H "Authorization: Bearer ${TOKEN}")
fi

post_tool_call() {
  local payload="$1"
  local body_file
  local http_status
  body_file="$(mktemp)"

  http_status="$(
    curl -sS \
      -o "${body_file}" \
      -w "%{http_code}" \
      -X POST \
      "${API_URL%/}/api/tool-calls" \
      "${HEADERS[@]}" \
      --data "${payload}"
  )" || {
    rm -f "${body_file}"
    die "curl 请求失败，请确认后端已启动: ${API_URL}"
  }

  local body
  body="$(cat "${body_file}")"
  rm -f "${body_file}"

  if [[ "${http_status}" -lt 200 || "${http_status}" -ge 300 ]]; then
    echo "${body}" >&2
    die "AgentToolGate 返回 HTTP ${http_status}"
  fi

  printf '%s' "${body}"
}

print_json() {
  "$PYTHON_BIN" -m json.tool
}

echo "检查后端健康状态: ${API_URL%/}/health"
curl -fsS "${API_URL%/}/health" >/dev/null || die "后端健康检查失败，请先运行 docker compose up --build"

echo
echo "步骤 1/3: 调用 mock.echo"
mock_payload="$(build_payload "mock.echo" "message=hello from curl demo")"
mock_response="$(post_tool_call "${mock_payload}")"
printf '%s\n' "${mock_response}" | print_json

echo
echo "步骤 2/3: 调用 database.query"
db_payload="$(
  build_payload \
    "database.query" \
    "datasource=${DATABASE_QUERY_DATASOURCE}" \
    "sql=${DATABASE_QUERY_SQL}"
)"
db_response="$(post_tool_call "${db_payload}")"
printf '%s\n' "${db_response}" | print_json

echo
echo "步骤 3/3: 提交 github.create_issue，预期触发 approval_required"
github_payload="$(
  build_payload \
    "github.create_issue" \
    "owner=${GITHUB_OWNER}" \
    "repo=${GITHUB_REPO}" \
    "title=${GITHUB_ISSUE_TITLE}" \
    "body=${GITHUB_ISSUE_BODY}"
)"
github_response="$(post_tool_call "${github_payload}")"
printf '%s\n' "${github_response}" | print_json

approval_status="$(printf '%s' "${github_response}" | json_field "status")"
approval_id="$(printf '%s' "${github_response}" | json_field "approvalId")"
call_id="$(printf '%s' "${github_response}" | json_field "callId")"

if [[ "${approval_status}" == "approval_required" ]]; then
  echo
  echo "已创建待审批请求。脚本不会自动审批，避免在配置真实 GITHUB_TOKEN 时误创建 issue。"
  echo "approvalId: ${approval_id}"
  echo "callId: ${call_id}"
  echo "请打开控制台处理: ${CONSOLE_URL%/}/approvals"
  echo
  echo "如需手动用 curl 审批，可确认风险后执行："
  echo "curl -X POST '${API_URL%/}/api/approvals/${approval_id}/approve' \\"
  echo "  -H 'Accept: application/json' \\"
  echo "  -H 'X-Workspace-Org-Id: ${WORKSPACE_ORG_ID}'"
else
  echo
  echo "提示: github.create_issue 返回状态为 ${approval_status}，不是预期的 approval_required。请检查策略、allowlist 或工具配置。"
fi
