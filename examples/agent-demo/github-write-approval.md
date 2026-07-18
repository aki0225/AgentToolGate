# GitHub 写操作审批 Synthetic Demo

这个 demo 用本机 mock GitHub API 演示 `github.create_issue` 的治理链路：

1. Agent 发起写操作时，ATG 返回 `approval_required`。
2. 审批前不触达上游 mock。
3. 请求者不能自批。
4. 独立 reviewer 审批后，ATG 才向上游发送一次请求。

公开边界：

- 只使用 `localhost` / `127.0.0.1` 和 `AUTH_MODE=local`。
- 不访问 GitHub 公网，不使用真实 PAT，也不创建真实 Issue。
- 所有 token、actor、仓储名和响应值都是 synthetic 占位符。
- 本 demo 不需要外部身份系统或生产 Secret 管理系统。

## 前置条件

- 在仓库根目录执行命令。
- 已安装 Go、Python 3 和 Windows 自带的 `curl.exe`。
- `8080` 与 `18080` 未被其他进程占用。
- 本 demo 使用 SQLite 保存 approval，因此 requester 和 reviewer 需要在同一终端中依次重启 backend，并复用同一个仓库 `.tmp` 数据库文件。

## 终端 1：启动本地 GitHub mock

mock 只记录请求方法和路径，不记录 Authorization 值。

```powershell
Set-Location <repo>

$mockCode = @'
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json

counts = {"githubIssue": 0}
expected_auth = "Bearer " + "<synthetic-github-demo-credential>"

class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        print(f"{self.command} {self.path}")

    def send_json(self, status, payload):
        raw = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        if self.path == "/_count":
            self.send_json(200, counts)
            return
        self.send_json(404, {"error": "not found"})

    def do_POST(self):
        if self.path != "/repos/acme/demo/issues":
            self.send_json(404, {"error": "not found"})
            return

        counts["githubIssue"] += 1
        if self.headers.get("Authorization") != expected_auth:
            self.send_json(401, {"error": "synthetic authentication missing"})
            return

        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length) if length else b"{}"
        body = json.loads(raw.decode("utf-8"))
        self.send_json(201, {
            "number": 7,
            "title": body.get("title", ""),
            "state": "open",
            "html_url": "http://127.0.0.1:18080/mock/issues/7",
        })

ThreadingHTTPServer(("127.0.0.1", 18080), Handler).serve_forever()
'@

python -u -c $mockCode
```

保持这个终端运行。审批前它不应出现 `POST /repos/acme/demo/issues`。

## 终端 2：以 requester 身份启动 SQLite backend

```powershell
Set-Location <repo>

$repo = (Get-Location).Path
$runId = [guid]::NewGuid().ToString("N").Substring(0, 12)
$db = Join-Path $repo ".tmp\github-write-approval-$runId.db"

$env:GOPATH = Join-Path $repo ".tmp\go\gopath"
$env:GOMODCACHE = Join-Path $env:GOPATH "pkg\mod"
$env:GOCACHE = Join-Path $repo ".tmp\go\build"
$env:STORE_DRIVER = "sqlite"
$env:AGT_SQLITE_PATH = $db
$env:AUTH_MODE = "local"
$env:DEFAULT_WORKSPACE_ORG_ID = "local-org"
$env:POLICY_CONFIG_PATH = "..\configs\policies.yaml"
$env:HOST = "127.0.0.1"
$env:PORT = "8080"
$env:GITHUB_API_BASE_URL = "http://127.0.0.1:18080"
$env:GITHUB_ALLOWED_REPOS = "acme/demo"
$env:GITHUB_TOKEN = "<synthetic-github-demo-credential>"
$env:NO_PROXY = "127.0.0.1,localhost"

$env:LOCAL_SUBJECT = "requester-$runId"
$env:LOCAL_EMAIL = "requester-$runId@agenttoolgate.local"
$env:LOCAL_NAME = "Synthetic Requester"
$env:LOCAL_ROLE = "owner"

New-Item -ItemType Directory -Force -Path $env:GOPATH, $env:GOMODCACHE, $env:GOCACHE | Out-Null
Set-Location .\backend
go run .\cmd\server
```

不要关闭此 PowerShell 窗口。稍后会在同一窗口保留 `$db` 和 `$runId`，只切换 local identity 再次启动。

## 终端 3：创建受审批的 GitHub 写操作

```powershell
Set-Location <repo>

$api = "http://127.0.0.1:8080"
$workspaceHeader = "X-Workspace-Org-Id: local-org"
$mock = "http://127.0.0.1:18080"

$before = curl.exe -sS "$mock/_count" | ConvertFrom-Json

$create = @'
{
  "tool": "github.create_issue",
  "arguments": {
    "owner": "acme",
    "repo": "demo",
    "title": "Synthetic approval demo",
    "body": "Created only after reviewer approval"
  }
}
'@ | curl.exe -sS -X POST "$api/api/tool-calls" `
  -H $workspaceHeader `
  -H "Content-Type: application/json" `
  --data-binary "@-"

$create
$approvalId = ($create | ConvertFrom-Json).approvalId
$afterPending = curl.exe -sS "$mock/_count" | ConvertFrom-Json
"githubIssue: $($before.githubIssue) -> $($afterPending.githubIssue)"
```

预期：

```json
{
  "status": "approval_required",
  "approvalId": "<approval-id>",
  "approvalStatus": "pending",
  "callId": "<call-id>",
  "traceId": "<trace-id>"
}
```

mock 计数必须保持不变：

```text
githubIssue: 0 -> 0
```

这说明 `github.create_issue` 在生成 approval 后停止，不会先调用 GitHub 上游。

## 请求者不能自批

在 backend 仍以 requester 身份运行时：

```powershell
@'
{
  "reason": "requester must not self-approve"
}
'@ | curl.exe -sS -i -X POST "$api/api/approvals/$approvalId/approve" `
  -H $workspaceHeader `
  -H "Content-Type: application/json" `
  --data-binary "@-"

curl.exe -sS "$mock/_count"
```

预期：

```text
HTTP/1.1 403 Forbidden
{"error":"forbidden"}

{"githubIssue": 0}
```

这个 `403` 是 reviewer 身份边界，不是 policy deny。approval 仍为 `pending`，上游仍未被调用。

## 终端 2：切换到 reviewer 并重启 backend

在终端 2 按 `Ctrl+C` 停止 requester backend。不要关闭窗口，然后执行：

```powershell
$env:LOCAL_SUBJECT = "reviewer-$runId"
$env:LOCAL_EMAIL = "reviewer-$runId@agenttoolgate.local"
$env:LOCAL_NAME = "Synthetic Reviewer"
$env:LOCAL_ROLE = "approver"

Set-Location "$repo\backend"
go run .\cmd\server
```

SQLite 路径和 GitHub mock 配置保持不变，但当前 actor 已从 requester 切换为 reviewer。

## 终端 3：reviewer 审批并放行一次

```powershell
$approved = @'
{
  "reason": "synthetic reviewer approved"
}
'@ | curl.exe -sS -X POST "$api/api/approvals/$approvalId/approve" `
  -H $workspaceHeader `
  -H "Content-Type: application/json" `
  --data-binary "@-"

$approved
$afterApproved = curl.exe -sS "$mock/_count" | ConvertFrom-Json
"githubIssue: $($afterPending.githubIssue) -> $($afterApproved.githubIssue)"

if ($approved.Contains("<synthetic-github-demo-credential>")) {
    throw "Synthetic credential leaked into approval response"
}
```

预期稳定字段：

```json
{
  "approval": {
    "id": "<approval-id>",
    "status": "approved",
    "requestedBy": "requester-<run-id>",
    "reviewedBy": "reviewer-<run-id>"
  },
  "toolCall": {
    "toolKey": "github.create_issue",
    "status": "success",
    "policyDecision": "require_approval",
    "approvalStatus": "approved"
  },
  "result": {
    "owner": "acme",
    "repo": "demo",
    "number": 7,
    "state": "open"
  }
}
```

mock 计数应刚好增加一次：

```text
githubIssue: 0 -> 1
```

此外，公开 `toolCall` 不应包含 `inputExecutionJson`。ATG 只在 pending approval 期间将原始执行输入保存在内部字段；approve 后会清空，API 不返回该字段。

## 演示清理

1. 在两个后台终端按 `Ctrl+C` 停止 backend 和 mock。
2. 在终端 2 删除本次 SQLite 文件：

```powershell
Remove-Item -LiteralPath $db -Force
```

`$db` 指向仓库 `.tmp` 下本次 demo 创建的 `github-write-approval-$runId.db`。不要删除其他 `.tmp` 文件。

## 演示结论与边界

- `github.create_issue` 是写操作，首次调用返回 HTTP `200` + `status=approval_required`，而不是 `4xx`。
- approval 生成前已完成 repo allowlist、参数和 Secret 可用性校验；无效配置不会生成无意义审批单。
- 请求者不能自批；独立 reviewer 通过后，上游只会执行一次。
- 本 demo 使用 backend runtime 的 synthetic `GITHUB_TOKEN`。当前 ATG 的 GitHub 集成适合 PAT/demo token 形态，不是完整 GitHub App installation-token 生产闭环。
- 演示结束时必须删除仓库 `.tmp` 下本次 SQLite 文件，避免把临时 approval 状态误当作长期样例数据。
