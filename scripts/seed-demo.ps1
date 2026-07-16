# AgentToolGate 本地演示数据种子脚本。
# 只通过真实后端 API 创建 workspace-scoped 元数据；不会写入或打印密钥明文。
[CmdletBinding()]
param(
    [string]$ApiBaseUrl = "http://127.0.0.1:8080",
    [string]$WorkspaceOrgId = "local-org",
    [string]$SecretName = "demo_http_api_key",
    [string]$SecretValueRef = "AGT_DEMO_HTTP_API_KEY",
    [string]$HttpConnectorName = "demo_http",
    [string]$HttpConnectorDisplayName = "Demo HTTP",
    [int]$HttpMockPort = 18080,
    [string]$PolicyName = "Demo GitHub create_issue 需要审批"
)

$ErrorActionPreference = "Stop"

function Join-ApiPath {
    param(
        [Parameter(Mandatory = $true)][string]$BaseUrl,
        [Parameter(Mandatory = $true)][string]$Path
    )

    return "$($BaseUrl.TrimEnd('/'))/$($Path.TrimStart('/'))"
}

function Invoke-DemoApi {
    param(
        [Parameter(Mandatory = $true)][string]$Method,
        [Parameter(Mandatory = $true)][string]$Path,
        [object]$Body = $null
    )

    $headers = @{
        "X-Workspace-Org-Id" = $WorkspaceOrgId
    }
    $uri = Join-ApiPath -BaseUrl $ApiBaseUrl -Path $Path
    $parameters = @{
        Method      = $Method
        Uri         = $uri
        Headers     = $headers
        ErrorAction = "Stop"
    }
    if ($null -ne $Body) {
        $parameters.ContentType = "application/json"
        $parameters.Body = ($Body | ConvertTo-Json -Depth 20)
    }

    return Invoke-RestMethod @parameters
}

function Assert-BackendReady {
    $healthUrl = Join-ApiPath -BaseUrl $ApiBaseUrl -Path "/health"
    try {
        $null = Invoke-RestMethod -Method GET -Uri $healthUrl -ErrorAction Stop
    }
    catch {
        throw "后端不可访问：$healthUrl。请先启动 backend，或用 verify-local.ps1 -WithE2E 托管启动。"
    }
}

function Get-ListItems {
    param([Parameter(Mandatory = $true)][string]$Path)

    $result = Invoke-DemoApi -Method GET -Path $Path
    if ($null -eq $result.items) {
        return @()
    }
    return @($result.items)
}

function Ensure-DemoSecret {
    $metadata = @{
        scope = "local-real-demo"
        note  = "只保存后端环境变量名，不保存密钥明文"
    }
    $payload = @{
        name        = $SecretName
        description = "本地真实演示 HTTP headerSecretRefs 使用的 env-backed Secret"
        enabled     = $true
        secretType  = "api_key"
        valueSource = "env"
        valueRef    = $SecretValueRef
        metadata    = $metadata
    }

    $existing = Get-ListItems -Path "/api/secrets" | Where-Object { $_.name -eq $SecretName } | Select-Object -First 1
    if ($existing) {
        $null = Invoke-DemoApi -Method PUT -Path "/api/secrets/$($existing.id)" -Body $payload
        Write-Host "已更新演示 Secret 元数据：$SecretName -> $SecretValueRef"
        return
    }

    $null = Invoke-DemoApi -Method POST -Path "/api/secrets" -Body $payload
    Write-Host "已创建演示 Secret 元数据：$SecretName -> $SecretValueRef"
}

function Ensure-DemoHttpConnector {
    $payload = @{
        type        = "http"
        name        = $HttpConnectorName
        displayName = $HttpConnectorDisplayName
        configJson  = @{
            mode             = "local-real-demo"
            allowedHosts     = @("127.0.0.1:$HttpMockPort", "localhost:$HttpMockPort")
            allowedMethods   = @("GET", "POST")
            headerSecretRefs = @{
                "X-Api-Key" = $SecretName
            }
        }
        enabled     = $true
    }

    $existing = Get-ListItems -Path "/api/connectors" |
        Where-Object { $_.type -eq "http" -and $_.name -eq $HttpConnectorName } |
        Select-Object -First 1
    if ($existing) {
        $updatePayload = @{
            displayName = $payload.displayName
            configJson  = $payload.configJson
            enabled     = $true
        }
        $null = Invoke-DemoApi -Method PATCH -Path "/api/connectors/$($existing.id)" -Body $updatePayload
        Write-Host "已更新演示 HTTP Connector：http.$HttpConnectorName"
        return
    }

    $null = Invoke-DemoApi -Method POST -Path "/api/connectors" -Body $payload
    Write-Host "已创建演示 HTTP Connector：http.$HttpConnectorName"
}

function Ensure-DemoPolicy {
    $payload = @{
        name            = $PolicyName
        description     = "演示 workspace 托管策略如何把 GitHub 写操作解释为需要审批"
        enabled         = $true
        priority        = 100
        effect          = "require_approval"
        connectorType   = "github"
        toolNamePattern = "github.create_issue"
        operationType   = "create"
        riskLevel       = "*"
        resourcePattern = "*"
        reason          = "演示策略：创建 GitHub Issue 属于写操作，需要人工审批。"
    }

    $existing = Get-ListItems -Path "/api/policies" | Where-Object { $_.name -eq $PolicyName } | Select-Object -First 1
    if ($existing) {
        $null = Invoke-DemoApi -Method PUT -Path "/api/policies/$($existing.id)" -Body $payload
        Write-Host "已更新演示 Policy：$PolicyName"
        return
    }

    $null = Invoke-DemoApi -Method POST -Path "/api/policies" -Body $payload
    Write-Host "已创建演示 Policy：$PolicyName"
}

Assert-BackendReady
Ensure-DemoSecret
Ensure-DemoHttpConnector
Ensure-DemoPolicy

Write-Host "本地演示数据已就绪。Secret 仅保存 valueRef，不包含密钥明文。" -ForegroundColor Green
