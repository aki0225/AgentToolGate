# Secret 外传阻断与脱敏 Synthetic Demo

这个 demo 演示两条不同但互补的安全行为：

1. Agent 试图在 `http.request` 参数中直接携带 `Authorization`，会在触达上游前被 HTTP adapter 硬拒绝。
2. 合法的 env-backed Secret 引用只能由后端在运行时注入到 allowlist 上游；即使上游响应回显敏感字段，API 结果和审计也会脱敏。

公开边界：

- 只使用 `localhost` / `127.0.0.1` 和 `AUTH_MODE=local`。
- 不访问公网，不使用真实 token，也不会把真实 Secret 写入 ATG。
- 所有 `<synthetic-...>` 值都是占位 demo 值，不是真实凭据。
- backend 使用 `memory` store；停止进程后，演示 Secret 元数据和审计元数据随进程一起清理。
- Go 缓存只写到仓库 `.tmp\go\...`。

## 先理解预期

当前 API 有两类不同结果：

- 直接提供 `Authorization` 等禁止 header：HTTP `400`，审计状态为 `failed`。这不是 policy `deny`，而是 adapter 在审批和上游请求前的硬校验。
- 通过 `headerSecretRefs` 使用已注册 Secret：请求可到达明确 allowlist 的上游；公开响应与审计中的敏感 header、JSON key 和 Secret 值会显示为 `[REDACTED]`。

因此，**脱敏不等于阻断**。ATG 阻止 Agent 直接控制敏感 header；它允许后端在明确配置、明确 allowlist 的连接中注入 Secret，并避免把该值回显给 Agent。

## 前置条件

- 在仓库根目录执行命令。
- 已安装 Go、Python 3 和 Windows 自带的 `curl.exe`。
- `8080` 与 `18080` 未被其他进程占用。

## 终端 1：启动本地 synthetic 上游

下面的 mock 不记录 Authorization 值。它只统计请求次数，并在合法请求时故意返回敏感形态的 response header 和 JSON 字段，供 ATG 脱敏验证。

```powershell
Set-Location <repo>

$mockCode = @'
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json

counts = {"httpEcho": 0}
expected_auth = "Bearer " + "<synthetic-http-demo-credential>"

class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        print(f"{self.command} {self.path}")

    def send_json(self, status, payload, extra_headers=None):
        raw = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        for key, value in (extra_headers or {}).items():
            self.send_header(key, value)
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        if self.path == "/_count":
            self.send_json(200, counts)
            return

        if self.path == "/http-echo":
            counts["httpEcho"] += 1
            if self.headers.get("Authorization") != expected_auth:
                self.send_json(401, {"error": "synthetic authentication missing"})
                return
            self.send_json(
                200,
                {
                    "ok": True,
                    "token": "<synthetic-response-token>",
                    "nested": {"secret": "<synthetic-response-secret>"},
                },
                {"X-Secret-Token": "<synthetic-response-header>"},
            )
            return

        self.send_json(404, {"error": "not found"})

ThreadingHTTPServer(("127.0.0.1", 18080), Handler).serve_forever()
'@

python -u -c $mockCode
```

保持这个终端运行。它只应输出请求方法和路径，例如 `GET /http-echo`。

## 终端 2：启动本地 ATG

```powershell
Set-Location <repo>

$repo = (Get-Location).Path
$env:GOPATH = Join-Path $repo ".tmp\go\gopath"
$env:GOMODCACHE = Join-Path $env:GOPATH "pkg\mod"
$env:GOCACHE = Join-Path $repo ".tmp\go\build"
$env:STORE_DRIVER = "memory"
$env:AUTH_MODE = "local"
$env:LOCAL_ROLE = "owner"
$env:DEFAULT_WORKSPACE_ORG_ID = "local-org"
$env:POLICY_CONFIG_PATH = "..\configs\policies.yaml"
$env:HOST = "127.0.0.1"
$env:PORT = "8080"
$env:HTTP_ALLOWED_HOSTS = "127.0.0.1:18080"
$env:HTTP_ALLOWED_METHODS = "GET,HEAD,OPTIONS,POST,PUT,PATCH,DELETE"
$env:AGT_DEMO_HTTP_AUTH = "Bearer " + "<synthetic-http-demo-credential>"

New-Item -ItemType Directory -Force -Path $env:GOPATH, $env:GOMODCACHE, $env:GOCACHE | Out-Null
Set-Location .\backend
go run .\cmd\server
```

等待 `/health` 可用后，在第三个终端继续。

## 终端 3：直接 header 外传尝试会被阻断

```powershell
Set-Location <repo>

$api = "http://127.0.0.1:8080"
$workspaceHeader = "X-Workspace-Org-Id: local-org"
$mock = "http://127.0.0.1:18080"

curl.exe -sS "$mock/_count"

@'
{
  "tool": "http.request",
  "arguments": {
    "method": "GET",
    "url": "http://127.0.0.1:18080/http-echo",
    "headers": {
      "Authorization": "Bearer <agent-supplied-synthetic-value>"
    }
  }
}
'@ | curl.exe -sS -i -X POST "$api/api/tool-calls" `
  -H $workspaceHeader `
  -H "Content-Type: application/json" `
  --data-binary "@-"

curl.exe -sS "$mock/_count"
```

预期：

```text
HTTP/1.1 400 Bad Request
{"error":"bad request: http header Authorization is not allowed"}

{"httpEcho": 0}
```

这证明 Agent 不能通过 `headers.Authorization` 自行携带值。校验在创建 approval 和发送上游请求前完成，所以 mock 计数仍是 `0`。

还可以检查失败审计是否也没有回显 Agent 提供的值：

```powershell
$failedAudit = curl.exe -sS `
  "$api/api/tool-calls?tool=http.request&status=failed&page=1&pageSize=10" `
  -H $workspaceHeader

$failedAudit
if ($failedAudit.Contains("<agent-supplied-synthetic-value>")) {
    throw "敏感值不应出现在审计 API 响应中"
}
```

公开审计输入中的 `Authorization` 应为 `[REDACTED]`，而不是原始值。

## 创建 env-backed Secret 元数据

API 只保存环境变量名 `AGT_DEMO_HTTP_AUTH`，不会保存或返回该环境变量解析后的值。

```powershell
@'
{
  "name": "demo_http_auth",
  "description": "Synthetic HTTP Authorization demo",
  "enabled": true,
  "secretType": "token",
  "valueSource": "env",
  "valueRef": "AGT_DEMO_HTTP_AUTH",
  "metadata": {
    "scope": "secret-exfiltration-blocked"
  }
}
'@ | curl.exe -sS -X POST "$api/api/secrets" `
  -H $workspaceHeader `
  -H "Content-Type: application/json" `
  --data-binary "@-"
```

预期响应只含 `name`、`enabled`、`secretType`、`valueSource`、`valueRef` 和 metadata，不含 `<synthetic-http-demo-credential>`。

## 受控 Secret 注入与 response 脱敏

```powershell
$before = curl.exe -sS "$mock/_count" | ConvertFrom-Json

$allowed = @'
{
  "tool": "http.request",
  "arguments": {
    "method": "GET",
    "url": "http://127.0.0.1:18080/http-echo",
    "headerSecretRefs": {
      "Authorization": "demo_http_auth"
    }
  }
}
'@ | curl.exe -sS -X POST "$api/api/tool-calls" `
  -H $workspaceHeader `
  -H "Content-Type: application/json" `
  --data-binary "@-"

$after = curl.exe -sS "$mock/_count" | ConvertFrom-Json
$allowed
"httpEcho: $($before.httpEcho) -> $($after.httpEcho)"

foreach ($value in @(
    "<synthetic-http-demo-credential>",
    "<synthetic-response-token>",
    "<synthetic-response-secret>",
    "<synthetic-response-header>"
)) {
    if ($allowed.Contains($value)) {
        throw "敏感值不应出现在公开响应中"
    }
}
```

预期稳定字段：

```json
{
  "status": "success",
  "result": {
    "statusCode": 200,
    "headers": {
      "X-Secret-Token": "[REDACTED]"
    },
    "body": {
      "ok": true,
      "token": "[REDACTED]",
      "nested": {
        "secret": "[REDACTED]"
      }
    }
  },
  "callId": "<call-id>",
  "traceId": "<trace-id>"
}
```

`httpEcho` 计数应从 `0 -> 1`。这表示 backend 已把 Secret 注入到明确 allowlist 的本地 mock；但 Agent 看到的结果和后续 audit 都不会包含该 Secret 或 mock 回显的敏感值。

## 演示清理

1. 在两个后台终端按 `Ctrl+C` 停止 backend 和 mock。
2. backend 使用 `STORE_DRIVER=memory`，进程停止后，演示 Secret 元数据、tool-call 审计元数据和其他 memory store 内容即被清理。
3. `.tmp\go\...` 下只包含 Go 缓存；可按仓库清理策略单独处理，不属于 demo Secret 数据。

## 演示结论与边界

- 直接由 Agent 提供的 `Authorization` 被硬拒绝，不触达上游。
- `headerSecretRefs` 只接受 Secret 名称，实际 env 值仅在 backend runtime 中解析。
- 上游响应中出现的 `token`、`secret` 和敏感 header 会被公开响应与审计脱敏。
- memory store 停止即清理；本 demo 不产生持久 Secret store。
- 这不是 KMS/Vault，也不是 DLP 或网络 sandbox。被攻陷的 backend runtime 或允许的上游仍需要最小权限、网络边界和 Secret 管理系统配合保护。
