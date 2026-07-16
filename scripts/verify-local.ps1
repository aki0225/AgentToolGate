# AgentToolGate 本地验证脚本。
# 默认保持轻量：不启动 Docker、不自动启动 PostgreSQL、不跑 E2E。
[CmdletBinding()]
param(
    [switch]$WithPostgres,
    [switch]$WithE2E,
    [switch]$WithMultiActorE2E,
    [switch]$WithDemoSeed,
    [int]$FrontendPort = 5173,
    [int]$E2EHTTPPort = 18080
)

$ErrorActionPreference = "Stop"

$RepoRoot = Split-Path -Parent $PSScriptRoot
function Resolve-GoExe {
    if ($env:AGT_GO_EXE) {
        if (-not (Test-Path -LiteralPath $env:AGT_GO_EXE)) {
            throw "AGT_GO_EXE 指向的 Go 可执行文件不存在：$env:AGT_GO_EXE"
        }
        return $env:AGT_GO_EXE
    }

    $goCommand = Get-Command go -ErrorAction SilentlyContinue
    if ($null -eq $goCommand) {
        throw "未找到 Go 可执行文件。请把 go 加入 PATH，或设置 AGT_GO_EXE 指向 go 可执行文件。"
    }
    return $goCommand.Source
}

function Resolve-RequiredPath {
    param(
        [Parameter(Mandatory = $true)][string]$EnvName,
        [Parameter(Mandatory = $true)][string]$Purpose
    )

    $value = [Environment]::GetEnvironmentVariable($EnvName, "Process")
    if ([string]::IsNullOrWhiteSpace($value)) {
        throw "使用 $Purpose 前请先设置 $EnvName。"
    }
    if (-not (Test-Path -LiteralPath $value)) {
        throw "$EnvName 指向的路径不存在：$value"
    }
    return $value
}

$GoExe = Resolve-GoExe
$PgCtl = if ($WithPostgres -or $WithMultiActorE2E) { Resolve-RequiredPath -EnvName "AGT_PG_CTL" -Purpose "本地 PostgreSQL 验证" } else { $env:AGT_PG_CTL }
$PgData = if ($WithPostgres -or $WithMultiActorE2E) { Resolve-RequiredPath -EnvName "AGT_PG_DATA" -Purpose "本地 PostgreSQL 验证" } else { $env:AGT_PG_DATA }
$TestDatabaseUrl = if ($env:TEST_DATABASE_URL) {
    $env:TEST_DATABASE_URL
} else {
    "postgres://agenttoolgate:agenttoolgate@127.0.0.1:5432/agenttoolgate?sslmode=disable"
}
$TmpDir = Join-Path $RepoRoot ".tmp"
$BackendLog = Join-Path $TmpDir "verify-local-backend.log"
$BackendRequesterLog = Join-Path $TmpDir "verify-local-backend-requester.log"
$BackendReviewerLog = Join-Path $TmpDir "verify-local-backend-reviewer.log"
$FrontendLog = Join-Path $TmpDir "verify-local-frontend.log"
$PowerShellExe = (Get-Process -Id $PID).Path

$backendProcess = $null
$frontendProcess = $null
$pgShouldStop = $false
$verificationSucceeded = $false
$managedHTTPMockPort = $E2EHTTPPort
$frontendBaseUrl = "http://127.0.0.1:$FrontendPort"
$frontendAllowedOrigins = "http://localhost:$FrontendPort,http://127.0.0.1:$FrontendPort"

function Invoke-Step {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][string]$Directory,
        [Parameter(Mandatory = $true)][scriptblock]$Command
    )

    Write-Host "`n==> $Name" -ForegroundColor Cyan
    Push-Location $Directory
    try {
        $global:LASTEXITCODE = 0
        & $Command
        if ($global:LASTEXITCODE -ne 0) {
            throw "步骤失败：$Name，退出码 $global:LASTEXITCODE"
        }
    }
    finally {
        Pop-Location
    }
}

function Wait-HttpReady {
    param(
        [Parameter(Mandatory = $true)][string]$Url,
        [int]$TimeoutSeconds = 45
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        try {
            $response = Invoke-WebRequest -Method GET -Uri $Url -UseBasicParsing -TimeoutSec 3
            if ($response.StatusCode -ge 200 -and $response.StatusCode -lt 500) {
                return
            }
        }
        catch {
            Start-Sleep -Milliseconds 500
        }
    } while ((Get-Date) -lt $deadline)

    throw "等待服务就绪超时：$Url"
}

function Test-TcpPortOpen {
    param(
        [Parameter(Mandatory = $true)][string]$HostName,
        [Parameter(Mandatory = $true)][int]$Port
    )

    $client = [System.Net.Sockets.TcpClient]::new()
    try {
        $asyncResult = $client.BeginConnect($HostName, $Port, $null, $null)
        if (-not $asyncResult.AsyncWaitHandle.WaitOne(300)) {
            return $false
        }
        try {
            $client.EndConnect($asyncResult)
            return $true
        }
        catch {
            return $false
        }
    }
    finally {
        $client.Close()
    }
}

function Assert-TcpPortAvailable {
    param(
        [Parameter(Mandatory = $true)][string]$HostName,
        [Parameter(Mandatory = $true)][int]$Port,
        [Parameter(Mandatory = $true)][string]$Purpose
    )

    if (Test-TcpPortOpen -HostName $HostName -Port $Port) {
        throw "端口已被占用：$HostName`:$Port（$Purpose）。请先停止占用该端口的进程，避免 E2E 误连到旧服务。"
    }
}

function Wait-TcpPortClosed {
    param(
        [Parameter(Mandatory = $true)][string]$HostName,
        [Parameter(Mandatory = $true)][int]$Port,
        [int]$TimeoutSeconds = 15
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if (-not (Test-TcpPortOpen -HostName $HostName -Port $Port)) {
            return
        }
        Start-Sleep -Milliseconds 250
    }

    throw "端口仍然占用：$HostName`:$Port"
}

function Get-FreeTcpPort {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    try {
        $listener.Start()
        return ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
    }
    finally {
        $listener.Stop()
    }
}

function Resolve-E2EHTTPMockPort {
    if (-not (Test-TcpPortOpen -HostName "127.0.0.1" -Port $E2EHTTPPort)) {
        return $E2EHTTPPort
    }

    if ($PSBoundParameters.ContainsKey("E2EHTTPPort")) {
        throw "端口已被占用：127.0.0.1:$E2EHTTPPort（E2E mock HTTP 上游）。请换一个 -E2EHTTPPort。"
    }

    $freePort = Get-FreeTcpPort
    Write-Host "默认 mock HTTP 端口 18080 已被占用，改用空闲端口：$freePort"
    return $freePort
}

function Test-PostgresRunning {
    if (-not (Test-Path $PgCtl)) {
        throw "未找到 pg_ctl：$PgCtl。可通过 AGT_PG_CTL 覆盖路径。"
    }
    if (-not (Test-Path $PgData)) {
        throw "未找到 PostgreSQL data dir：$PgData。可通过 AGT_PG_DATA 覆盖路径。"
    }

    & $PgCtl status -D $PgData *> $null
    return $LASTEXITCODE -eq 0
}

function Start-LocalPostgresIfNeeded {
    if (Test-PostgresRunning) {
        Write-Host "检测到本地 PostgreSQL 已在运行；按 -WithPostgres 验收要求，脚本结束时会停止该实例。"
        $script:pgShouldStop = $true
        return
    }

    Write-Host "启动本地 PostgreSQL：$PgData"
    & $PgCtl start -D $PgData
    if ($LASTEXITCODE -ne 0) {
        throw "启动本地 PostgreSQL 失败。"
    }
    $script:pgShouldStop = $true
}

function Stop-LocalPostgresIfStarted {
    if (-not $script:pgShouldStop) {
        return
    }

    Write-Host "`n==> 停止本地 PostgreSQL" -ForegroundColor Cyan
    & $PgCtl stop -D $PgData
    if ($LASTEXITCODE -ne 0) {
        throw "停止本地 PostgreSQL 失败。"
    }
    & $PgCtl status -D $PgData
    if ($LASTEXITCODE -eq 0) {
        throw "PostgreSQL 仍在运行，未达到 no server running。"
    }
}

function Start-ManagedServices {
    New-Item -ItemType Directory -Force -Path $TmpDir | Out-Null
    Remove-Item -Force -ErrorAction SilentlyContinue $BackendLog, $FrontendLog

    Assert-TcpPortAvailable -HostName "127.0.0.1" -Port 8080 -Purpose "backend"
    Assert-TcpPortAvailable -HostName "127.0.0.1" -Port $FrontendPort -Purpose "frontend"
    $script:managedHTTPMockPort = Resolve-E2EHTTPMockPort

    $backendEnv = @{
        PORT = "8080"
        STORE_DRIVER = "memory"
        DATABASE_URL = ""
        DATABASE_QUERY_URL = ""
        DATABASE_QUERY_ALLOWED_TABLES = "public.tools"
        POLICY_CONFIG_PATH = "../configs/policies.yaml"
        AUTH_MODE = "local"
        DEFAULT_WORKSPACE_ORG_ID = "local-org"
        LOCAL_ROLE = "owner"
        HTTP_ALLOWED_HOSTS = "127.0.0.1:$script:managedHTTPMockPort,localhost:$script:managedHTTPMockPort"
        HTTP_ALLOWED_METHODS = "GET,HEAD,OPTIONS,POST,PUT,PATCH,DELETE"
        CORS_ALLOWED_ORIGINS = $frontendAllowedOrigins
        OTEL_EXPORTER_OTLP_ENDPOINT = ""
        AGT_DEMO_HTTP_API_KEY = "local-demo-secret-for-e2e"
    }

    $frontendEnv = @{
        VITE_API_BASE_URL = "http://127.0.0.1:8080"
        VITE_AUTH_MODE = "local"
    }

    Write-Host "`n==> 启动真实 backend" -ForegroundColor Cyan
    $script:backendProcess = Start-ProcessWithEnv -FilePath $GoExe `
        -Arguments @("run", "./cmd/server") `
        -WorkingDirectory (Join-Path $RepoRoot "backend") `
        -Environment $backendEnv `
        -LogPath $BackendLog
    Wait-HttpReady -Url "http://127.0.0.1:8080/health" -TimeoutSeconds 60

    Write-Host "`n==> 启动真实 frontend" -ForegroundColor Cyan
    $script:frontendProcess = Start-ProcessWithEnv -FilePath "npm" `
        -Arguments @("run", "dev", "--", "--host", "127.0.0.1", "--port", [string]$FrontendPort) `
        -WorkingDirectory (Join-Path $RepoRoot "frontend") `
        -Environment $frontendEnv `
        -LogPath $FrontendLog
    Wait-HttpReady -Url $frontendBaseUrl -TimeoutSeconds 60
}

function Start-ProcessWithEnv {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][hashtable]$Environment,
        [Parameter(Mandatory = $true)][string]$LogPath
    )

    $envScript = $Environment.GetEnumerator() | ForEach-Object {
        "`$env:$($_.Key) = '$($_.Value -replace "'", "''")'"
    }
    $quotedFilePath = "'$($FilePath -replace "'", "''")'"
    $quotedArgs = $Arguments | ForEach-Object { "'$($_ -replace "'", "''")'" }
    $command = @(
        $envScript
        "Set-Location '$($WorkingDirectory -replace "'", "''")'"
        "& $quotedFilePath $($quotedArgs -join ' ') *>&1 | Tee-Object -FilePath '$($LogPath -replace "'", "''")'"
    ) -join "; "

    return Start-Process -FilePath $PowerShellExe `
        -ArgumentList @("-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", $command) `
        -WindowStyle Hidden `
        -PassThru `
        -WorkingDirectory $WorkingDirectory
}

function Invoke-MultiActorApprovalE2E {
    New-Item -ItemType Directory -Force -Path $TmpDir | Out-Null
    Remove-Item -Force -ErrorAction SilentlyContinue $BackendRequesterLog, $BackendReviewerLog, $FrontendLog

    Assert-TcpPortAvailable -HostName "127.0.0.1" -Port 8080 -Purpose "backend"
    Assert-TcpPortAvailable -HostName "127.0.0.1" -Port $FrontendPort -Purpose "frontend"

    Start-LocalPostgresIfNeeded

    $runId = ([guid]::NewGuid().ToString("N")).Substring(0, 12)
    $toolName = "write_$runId"
    $toolDisplayName = "多 Actor 审批工具 $runId"
    $toolKey = "mock.$toolName"
    $approvalReason = "reviewer approve $runId"
    $toolArguments = @{
        message = "multi-actor payload $runId"
        runId   = $runId
        stage   = "requester"
    }

    $requesterEnv = @{
        PORT                       = "8080"
        STORE_DRIVER               = "postgres"
        DATABASE_URL               = $TestDatabaseUrl
        POLICY_CONFIG_PATH         = "../configs/policies.yaml"
        AUTH_MODE                  = "local"
        DEFAULT_WORKSPACE_ORG_ID   = "local-org"
        LOCAL_SUBJECT              = "requester-$runId"
        LOCAL_EMAIL                = "requester+$runId@agenttoolgate.local"
        LOCAL_NAME                 = "Requester $runId"
        LOCAL_ROLE                 = "owner"
        CORS_ALLOWED_ORIGINS       = $frontendAllowedOrigins
        OTEL_EXPORTER_OTLP_ENDPOINT = ""
    }

    $reviewerEnv = @{
        PORT                       = "8080"
        STORE_DRIVER               = "postgres"
        DATABASE_URL               = $TestDatabaseUrl
        POLICY_CONFIG_PATH         = "../configs/policies.yaml"
        AUTH_MODE                  = "local"
        DEFAULT_WORKSPACE_ORG_ID   = "local-org"
        LOCAL_SUBJECT              = "reviewer-$runId"
        LOCAL_EMAIL                = "reviewer+$runId@agenttoolgate.local"
        LOCAL_NAME                 = "Reviewer $runId"
        LOCAL_ROLE                 = "approver"
        CORS_ALLOWED_ORIGINS       = $frontendAllowedOrigins
        OTEL_EXPORTER_OTLP_ENDPOINT = ""
    }

    Write-Host "`n==> 启动多 Actor requester backend" -ForegroundColor Cyan
    $script:backendProcess = Start-ProcessWithEnv -FilePath $GoExe `
        -Arguments @("run", "./cmd/server") `
        -WorkingDirectory (Join-Path $RepoRoot "backend") `
        -Environment $requesterEnv `
        -LogPath $BackendRequesterLog
    Wait-HttpReady -Url "http://127.0.0.1:8080/health" -TimeoutSeconds 60

    Write-Host "`n==> 启动真实 frontend" -ForegroundColor Cyan
    $script:frontendProcess = Start-ProcessWithEnv -FilePath "npm" `
        -Arguments @("run", "dev", "--", "--host", "127.0.0.1", "--port", [string]$FrontendPort) `
        -WorkingDirectory (Join-Path $RepoRoot "frontend") `
        -Environment @{
            VITE_API_BASE_URL = "http://127.0.0.1:8080"
            VITE_AUTH_MODE    = "local"
        } `
        -LogPath $FrontendLog
    Wait-HttpReady -Url $frontendBaseUrl -TimeoutSeconds 60

    Invoke-Step -Name "多 Actor requester 阶段" -Directory (Join-Path $RepoRoot "frontend") -Command {
        $previousApi = $env:E2E_API_BASE_URL
        $previousBaseUrl = $env:E2E_BASE_URL
        $previousWorkspace = $env:E2E_WORKSPACE_ORG_ID
        $previousPhase = $env:E2E_MULTI_ACTOR_PHASE
        $previousEnabled = $env:E2E_MULTI_ACTOR_APPROVAL
        $previousRunId = $env:E2E_MULTI_ACTOR_RUN_ID
        $previousBrowserChannel = $env:E2E_BROWSER_CHANNEL
        try {
            $env:E2E_API_BASE_URL = "http://127.0.0.1:8080"
            $env:E2E_BASE_URL = $frontendBaseUrl
            $env:E2E_WORKSPACE_ORG_ID = "local-org"
            $env:E2E_MULTI_ACTOR_APPROVAL = "1"
            $env:E2E_MULTI_ACTOR_PHASE = "requester"
            $env:E2E_MULTI_ACTOR_RUN_ID = $runId
            if (-not $env:E2E_BROWSER_CHANNEL -and (Test-Path "C:\Program Files\Google\Chrome\Application\chrome.exe")) {
                $env:E2E_BROWSER_CHANNEL = "chrome"
            }
            npm run e2e -- e2e/multi-actor-approval.spec.ts
        }
        finally {
            $env:E2E_API_BASE_URL = $previousApi
            $env:E2E_BASE_URL = $previousBaseUrl
            $env:E2E_WORKSPACE_ORG_ID = $previousWorkspace
            $env:E2E_MULTI_ACTOR_PHASE = $previousPhase
            $env:E2E_MULTI_ACTOR_APPROVAL = $previousEnabled
            $env:E2E_MULTI_ACTOR_RUN_ID = $previousRunId
            $env:E2E_BROWSER_CHANNEL = $previousBrowserChannel
        }
    }

    Write-Host "`n==> 切换为 reviewer backend" -ForegroundColor Cyan
    Stop-ProcessTree -Process $backendProcess
    $script:backendProcess = $null
    Wait-TcpPortClosed -HostName "127.0.0.1" -Port 8080

    $script:backendProcess = Start-ProcessWithEnv -FilePath $GoExe `
        -Arguments @("run", "./cmd/server") `
        -WorkingDirectory (Join-Path $RepoRoot "backend") `
        -Environment $reviewerEnv `
        -LogPath $BackendReviewerLog
    Wait-HttpReady -Url "http://127.0.0.1:8080/health" -TimeoutSeconds 60

    Invoke-Step -Name "多 Actor reviewer 阶段" -Directory (Join-Path $RepoRoot "frontend") -Command {
        $previousApi = $env:E2E_API_BASE_URL
        $previousBaseUrl = $env:E2E_BASE_URL
        $previousWorkspace = $env:E2E_WORKSPACE_ORG_ID
        $previousPhase = $env:E2E_MULTI_ACTOR_PHASE
        $previousEnabled = $env:E2E_MULTI_ACTOR_APPROVAL
        $previousRunId = $env:E2E_MULTI_ACTOR_RUN_ID
        $previousBrowserChannel = $env:E2E_BROWSER_CHANNEL
        try {
            $env:E2E_API_BASE_URL = "http://127.0.0.1:8080"
            $env:E2E_BASE_URL = $frontendBaseUrl
            $env:E2E_WORKSPACE_ORG_ID = "local-org"
            $env:E2E_MULTI_ACTOR_APPROVAL = "1"
            $env:E2E_MULTI_ACTOR_PHASE = "reviewer"
            $env:E2E_MULTI_ACTOR_RUN_ID = $runId
            if (-not $env:E2E_BROWSER_CHANNEL -and (Test-Path "C:\Program Files\Google\Chrome\Application\chrome.exe")) {
                $env:E2E_BROWSER_CHANNEL = "chrome"
            }
            npm run e2e -- e2e/multi-actor-approval.spec.ts
        }
        finally {
            $env:E2E_API_BASE_URL = $previousApi
            $env:E2E_BASE_URL = $previousBaseUrl
            $env:E2E_WORKSPACE_ORG_ID = $previousWorkspace
            $env:E2E_MULTI_ACTOR_PHASE = $previousPhase
            $env:E2E_MULTI_ACTOR_APPROVAL = $previousEnabled
            $env:E2E_MULTI_ACTOR_RUN_ID = $previousRunId
            $env:E2E_BROWSER_CHANNEL = $previousBrowserChannel
        }
    }

    Write-Host "多 Actor 审批验收完成。`n" -ForegroundColor Green
}

function Stop-ProcessTree {
    param([System.Diagnostics.Process]$Process)

    if ($null -eq $Process -or $Process.HasExited) {
        return
    }

    try {
        & taskkill.exe /PID $Process.Id /T /F *> $null
    }
    catch {
        try {
            Stop-Process -Id $Process.Id -Force -ErrorAction SilentlyContinue
        }
        catch {
            # 清理阶段尽力而为，避免掩盖主验证结果。
        }
    }
}

try {
    if (-not (Test-Path $GoExe)) {
        throw "未找到 Go 可执行文件：$GoExe。可通过 AGT_GO_EXE 覆盖路径。"
    }

    if ($WithE2E -and $WithMultiActorE2E) {
        throw "WithE2E 和 WithMultiActorE2E 不能同时使用，请分开运行。"
    }

    Invoke-Step -Name "后端 go test ./..." -Directory (Join-Path $RepoRoot "backend") -Command {
        & $GoExe test ./...
    }

    if ($WithPostgres) {
        Start-LocalPostgresIfNeeded
        Invoke-Step -Name "后端 PostgreSQL 集成 go test ./..." -Directory (Join-Path $RepoRoot "backend") -Command {
            $previous = $env:TEST_DATABASE_URL
            try {
                $env:TEST_DATABASE_URL = $TestDatabaseUrl
                & $GoExe test ./...
            }
            finally {
                $env:TEST_DATABASE_URL = $previous
            }
        }
    }

    Invoke-Step -Name "前端 npm run check" -Directory (Join-Path $RepoRoot "frontend") -Command {
        npm run check
    }

    Invoke-Step -Name "前端 npm run build" -Directory (Join-Path $RepoRoot "frontend") -Command {
        npm run build
    }

    if ($WithE2E) {
        Start-ManagedServices
        if ($WithDemoSeed) {
            Invoke-Step -Name "Demo seed" -Directory $RepoRoot -Command {
                & pwsh -NoProfile -ExecutionPolicy Bypass -File ".\scripts\seed-demo.ps1" `
                    -ApiBaseUrl "http://127.0.0.1:8080" `
                    -WorkspaceOrgId "local-org" `
                    -HttpMockPort $script:managedHTTPMockPort
            }
        }

        Invoke-Step -Name "真实本地演示 E2E" -Directory (Join-Path $RepoRoot "frontend") -Command {
            $previousApi = $env:E2E_API_BASE_URL
            $previousBaseUrl = $env:E2E_BASE_URL
            $previousWorkspace = $env:E2E_WORKSPACE_ORG_ID
            $previousHTTPMockPort = $env:E2E_HTTP_MOCK_PORT
            $previousSecret = $env:AGT_DEMO_HTTP_API_KEY
            $previousBrowserChannel = $env:E2E_BROWSER_CHANNEL
            try {
                $env:E2E_API_BASE_URL = "http://127.0.0.1:8080"
                $env:E2E_BASE_URL = $frontendBaseUrl
                $env:E2E_WORKSPACE_ORG_ID = "local-org"
                $env:E2E_HTTP_MOCK_PORT = [string]$script:managedHTTPMockPort
                $env:AGT_DEMO_HTTP_API_KEY = "local-demo-secret-for-e2e"
                if (-not $env:E2E_BROWSER_CHANNEL -and (Test-Path "C:\Program Files\Google\Chrome\Application\chrome.exe")) {
                    $env:E2E_BROWSER_CHANNEL = "chrome"
                }
                npm run e2e -- e2e/local-real-demo.spec.ts
            }
            finally {
                $env:E2E_API_BASE_URL = $previousApi
                $env:E2E_BASE_URL = $previousBaseUrl
                $env:E2E_WORKSPACE_ORG_ID = $previousWorkspace
                $env:E2E_HTTP_MOCK_PORT = $previousHTTPMockPort
                $env:AGT_DEMO_HTTP_API_KEY = $previousSecret
                $env:E2E_BROWSER_CHANNEL = $previousBrowserChannel
            }
        }
    }
    elseif ($WithDemoSeed) {
        Invoke-Step -Name "Demo seed" -Directory $RepoRoot -Command {
            & pwsh -NoProfile -ExecutionPolicy Bypass -File ".\scripts\seed-demo.ps1" `
                -ApiBaseUrl "http://127.0.0.1:8080" `
                -WorkspaceOrgId "local-org"
        }
    }

    if ($WithMultiActorE2E) {
        Invoke-MultiActorApprovalE2E
    }

    Invoke-Step -Name "Git diff whitespace check" -Directory $RepoRoot -Command {
        git diff --check
    }

    $script:verificationSucceeded = $true
}
finally {
    Stop-ProcessTree -Process $frontendProcess
    Stop-ProcessTree -Process $backendProcess
    Stop-LocalPostgresIfStarted
    if ($verificationSucceeded) {
        Remove-Item -Force -ErrorAction SilentlyContinue $BackendLog, $FrontendLog
    }
}

if ($verificationSucceeded) {
    Write-Host "`n本地验证通过。" -ForegroundColor Green
}
