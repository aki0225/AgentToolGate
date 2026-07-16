#!/usr/bin/env pwsh
[CmdletBinding()]
param(
    [ValidateSet("auto", "windows-amd64", "linux-amd64")]
    [string]$Platform = "auto",
    [switch]$SkipSmoke
)

$ErrorActionPreference = "Stop"

$RepoRoot = Split-Path -Parent $PSScriptRoot
$FrontendDir = Join-Path $RepoRoot "frontend"
$BackendDir = Join-Path $RepoRoot "backend"
$FrontendDist = Join-Path $FrontendDir "dist"
$EmbedDist = Join-Path (Join-Path $BackendDir "internal") (Join-Path "static" "site")
$DistDir = Join-Path $RepoRoot "dist"
$ReleaseDir = Join-Path $DistDir "release"
$ChecksumFile = Join-Path $ReleaseDir "SHA256SUMS"
$SmokeDir = Join-Path (Join-Path $RepoRoot ".tmp") "release-smoke"
$SmokeStdout = Join-Path $SmokeDir "agenttoolgate.out.log"
$SmokeStderr = Join-Path $SmokeDir "agenttoolgate.err.log"
$PlaceholderIndex = @"
<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>AgentToolGate</title>
  </head>
  <body>
    <div id="root">请通过 scripts/build-local.ps1 构建嵌入式前端。</div>
  </body>
</html>
"@

function Resolve-GoExe {
    if ($env:AGT_GO_EXE) {
        if (-not (Test-Path -LiteralPath $env:AGT_GO_EXE)) {
            throw "AGT_GO_EXE 指向的 Go 可执行文件不存在：$env:AGT_GO_EXE"
        }
        return $env:AGT_GO_EXE
    }

    $goCommand = Get-Command go -ErrorAction SilentlyContinue
    if ($null -ne $goCommand) {
        return $goCommand.Source
    }

    throw "未找到 Go 可执行文件。请把 go 加入 PATH，或设置 AGT_GO_EXE 指向 go 可执行文件。"
}

function Resolve-Platform {
    param([string]$RequestedPlatform)

    if ($RequestedPlatform -ne "auto") {
        return $RequestedPlatform
    }

    if ($IsLinux) {
        return "linux-amd64"
    }

    return "windows-amd64"
}

function Assert-SmokePlatformSupported {
    param([string]$TargetPlatform)

    if ($SkipSmoke) {
        return
    }

    if ($TargetPlatform -eq "windows-amd64" -and -not $IsWindows) {
        throw "Windows release smoke 需要在 Windows runner 上执行；如只做交叉打包请显式传入 -SkipSmoke。"
    }
    if ($TargetPlatform -eq "linux-amd64" -and -not $IsLinux) {
        throw "Linux release smoke 需要在 Linux runner 上执行；如只做交叉打包请显式传入 -SkipSmoke。"
    }
}

function Invoke-HealthSmoke {
    param(
        [Parameter(Mandatory = $true)][string]$BinaryPath,
        [Parameter(Mandatory = $true)][string]$TargetPlatform
    )

    if ($SkipSmoke) {
        Write-Host "==> 已跳过 $TargetPlatform release smoke" -ForegroundColor Yellow
        return
    }

    $doctorOutput = & $BinaryPath doctor --port 18089 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "$TargetPlatform doctor 验证失败，退出码 $LASTEXITCODE"
    }
    if (($doctorOutput -join "`n") -notmatch "AgentToolGate 本地诊断") {
        throw "$TargetPlatform doctor 输出缺少诊断标题"
    }

    $oldEnv = @{}
    foreach ($key in @("PORT", "HOST", "STORE_DRIVER", "AUTH_MODE", "DEFAULT_WORKSPACE_ORG_ID", "POLICY_CONFIG_PATH", "AGT_DATA_DIR")) {
        $oldEnv[$key] = [Environment]::GetEnvironmentVariable($key, "Process")
    }

    $proc = $null
    try {
        $env:PORT = "18089"
        $env:HOST = "127.0.0.1"
        $env:STORE_DRIVER = "memory"
        $env:AUTH_MODE = "local"
        $env:DEFAULT_WORKSPACE_ORG_ID = "local-org"
        $env:POLICY_CONFIG_PATH = Join-Path $RepoRoot "configs/policies.yaml"
        $env:AGT_DATA_DIR = Join-Path $SmokeDir "data"

        $proc = Start-Process -FilePath $BinaryPath `
            -ArgumentList @("--port", "18089") `
            -WorkingDirectory $RepoRoot `
            -PassThru `
            -RedirectStandardOutput $SmokeStdout `
            -RedirectStandardError $SmokeStderr

        $ready = $false
        for ($i = 0; $i -lt 60; $i++) {
            try {
                $health = Invoke-WebRequest -Uri "http://127.0.0.1:18089/health" -UseBasicParsing -TimeoutSec 3
                if ($health.StatusCode -eq 200) {
                    $ready = $true
                    break
                }
            }
            catch {
                Start-Sleep -Seconds 1
            }
        }
        if (-not $ready) {
            throw "等待 $TargetPlatform release smoke 服务就绪超时"
        }

        $root = Invoke-WebRequest -Uri "http://127.0.0.1:18089/" -UseBasicParsing -TimeoutSec 3
        if ($root.StatusCode -ne 200) {
            throw "$TargetPlatform / smoke 失败"
        }
    }
    catch {
        Write-Host "=== agenttoolgate stdout ==="
        Get-Content -LiteralPath $SmokeStdout -ErrorAction SilentlyContinue
        Write-Host "=== agenttoolgate stderr ==="
        Get-Content -LiteralPath $SmokeStderr -ErrorAction SilentlyContinue
        throw
    }
    finally {
        if ($proc -and -not $proc.HasExited) {
            Stop-Process -Id $proc.Id -Force
        }
        foreach ($entry in $oldEnv.GetEnumerator()) {
            [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, "Process")
        }
    }
}

if (-not (Test-Path $FrontendDir)) {
    throw "未找到前端目录：$FrontendDir"
}

if (-not (Test-Path $BackendDir)) {
    throw "未找到后端目录：$BackendDir"
}

$Platform = Resolve-Platform -RequestedPlatform $Platform
Assert-SmokePlatformSupported -TargetPlatform $Platform
$GoExe = Resolve-GoExe

$goOS = if ($Platform -eq "linux-amd64") { "linux" } else { "windows" }
$goArch = "amd64"
$binaryName = if ($Platform -eq "linux-amd64") { "agenttoolgate" } else { "agenttoolgate.exe" }
$packageName = if ($Platform -eq "linux-amd64") { "agenttoolgate-linux-amd64.tar.gz" } else { "agenttoolgate-windows-amd64.zip" }
$packagePath = Join-Path $ReleaseDir $packageName
$StageDir = Join-Path $SmokeDir ("stage-" + $Platform)
$PackageDir = Join-Path $StageDir "package"
$BinaryPath = Join-Path $PackageDir $binaryName
$ExtractDir = Join-Path $SmokeDir ("extract-" + $Platform)

Write-Host "==> 构建平台: $Platform" -ForegroundColor Cyan
Write-Host "==> 构建前端" -ForegroundColor Cyan
Push-Location $FrontendDir
try {
    npm run build
    if ($LASTEXITCODE -ne 0) {
        throw "前端构建失败，退出码 $LASTEXITCODE"
    }
}
finally {
    Pop-Location
}

if (-not (Test-Path $FrontendDist)) {
    throw "前端构建产物不存在：$FrontendDist"
}

Write-Host "==> 复制前端到 Go embed 目录" -ForegroundColor Cyan
New-Item -ItemType Directory -Force -Path $EmbedDist | Out-Null
Get-ChildItem -Path $EmbedDist -Force | Remove-Item -Recurse -Force -ErrorAction SilentlyContinue
Copy-Item -Path (Join-Path $FrontendDist '*') -Destination $EmbedDist -Recurse -Force

$oldGoOS = [Environment]::GetEnvironmentVariable("GOOS", "Process")
$oldGoArch = [Environment]::GetEnvironmentVariable("GOARCH", "Process")
try {
    Write-Host "==> 构建 $Platform 单二进制" -ForegroundColor Cyan
    if (Test-Path $StageDir) {
        Remove-Item -LiteralPath $StageDir -Recurse -Force
    }
    New-Item -ItemType Directory -Force -Path $PackageDir | Out-Null
    [Environment]::SetEnvironmentVariable("GOOS", $goOS, "Process")
    [Environment]::SetEnvironmentVariable("GOARCH", $goArch, "Process")
    Push-Location $BackendDir
    try {
        & $GoExe build -o $BinaryPath ./cmd/server
        if ($LASTEXITCODE -ne 0) {
            throw "Go 构建失败，退出码 $LASTEXITCODE"
        }
    }
    finally {
        Pop-Location
    }
}
finally {
    [Environment]::SetEnvironmentVariable("GOOS", $oldGoOS, "Process")
    [Environment]::SetEnvironmentVariable("GOARCH", $oldGoArch, "Process")
    # 构建后恢复占位前端，避免 release 用的 Vite 哈希产物长期留在源码树里。
    Get-ChildItem -Path $EmbedDist -Force | Remove-Item -Recurse -Force -ErrorAction SilentlyContinue
    $placeholderPath = Join-Path $EmbedDist "index.html"
    $utf8NoBom = [System.Text.UTF8Encoding]::new($false)
    [System.IO.File]::WriteAllText($placeholderPath, (($PlaceholderIndex -replace "`r`n", "`n") + "`n"), $utf8NoBom)
}

if (-not (Test-Path $BinaryPath)) {
    throw "未找到构建产物：$BinaryPath"
}

if ($Platform -eq "linux-amd64") {
    & chmod +x $BinaryPath
    if ($LASTEXITCODE -ne 0) {
        throw "chmod 失败，退出码 $LASTEXITCODE"
    }
}

Write-Host "==> 组装发布目录" -ForegroundColor Cyan
if (Test-Path $ReleaseDir) {
    Remove-Item -LiteralPath $ReleaseDir -Recurse -Force
}
New-Item -ItemType Directory -Force -Path $ReleaseDir | Out-Null

if ($Platform -eq "linux-amd64") {
    & tar -czf $packagePath -C $PackageDir $binaryName
    if ($LASTEXITCODE -ne 0) {
        throw "tar.gz 打包失败，退出码 $LASTEXITCODE"
    }
}
else {
    Compress-Archive -LiteralPath $BinaryPath -DestinationPath $packagePath -Force
}

$checksumLine = "{0}  {1}" -f ((Get-FileHash -LiteralPath $packagePath -Algorithm SHA256).Hash.ToLowerInvariant()), (Split-Path -Leaf $packagePath)
[System.IO.File]::WriteAllText($ChecksumFile, ($checksumLine + "`n"), [System.Text.UTF8Encoding]::new($false))

Write-Host "==> 验证发布产物" -ForegroundColor Cyan
foreach ($artifact in @($packagePath, $ChecksumFile)) {
    if (-not (Test-Path $artifact)) {
        throw "发布产物缺失：$artifact"
    }
}

$checksumText = Get-Content -Raw $ChecksumFile
if ($checksumText -notmatch [regex]::Escape($checksumLine)) {
    throw "SHA256SUMS 未包含正确校验值：$packageName"
}

if (Test-Path $ExtractDir) {
    Remove-Item -LiteralPath $ExtractDir -Recurse -Force
}
New-Item -ItemType Directory -Force -Path $ExtractDir | Out-Null
if ($Platform -eq "linux-amd64") {
    & tar -xzf $packagePath -C $ExtractDir
    if ($LASTEXITCODE -ne 0) {
        throw "tar.gz 解包失败，退出码 $LASTEXITCODE"
    }
}
else {
    Expand-Archive -LiteralPath $packagePath -DestinationPath $ExtractDir -Force
}
$SmokeBinary = Join-Path $ExtractDir $binaryName
if (-not (Test-Path $SmokeBinary)) {
    throw "$packageName 中未包含 $binaryName"
}
if ($Platform -eq "linux-amd64") {
    & chmod +x $SmokeBinary
}

Invoke-HealthSmoke -BinaryPath $SmokeBinary -TargetPlatform $Platform

Write-Host "==> 发布产物已生成" -ForegroundColor Green
Write-Host "    $packagePath"
Write-Host "    $ChecksumFile"
