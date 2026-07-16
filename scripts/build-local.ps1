#!/usr/bin/env pwsh
[CmdletBinding()]
param()

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

$GoExe = Resolve-GoExe
$FrontendDir = Join-Path $RepoRoot "frontend"
$BackendDir = Join-Path $RepoRoot "backend"
$FrontendDist = Join-Path $FrontendDir "dist"
$EmbedDist = Join-Path (Join-Path $BackendDir "internal") (Join-Path "static" "site")
$OutputDir = Join-Path $RepoRoot "dist"
$OutputExe = Join-Path $OutputDir "agenttoolgate.exe"
$PlaceholderIndex = @'
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
'@

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

try {
    Write-Host "==> 构建单二进制" -ForegroundColor Cyan
    New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null
    Push-Location $BackendDir
    try {
        & $GoExe build -o $OutputExe .\cmd\server
        if ($LASTEXITCODE -ne 0) {
            throw "Go 构建失败，退出码 $LASTEXITCODE"
        }
    }
    finally {
        Pop-Location
    }
}
finally {
    # 构建后恢复占位前端，避免把 Vite 哈希产物长期留在源码树里。
    Get-ChildItem -Path $EmbedDist -Force | Remove-Item -Recurse -Force -ErrorAction SilentlyContinue
    $placeholderPath = Join-Path $EmbedDist "index.html"
    $utf8NoBom = [System.Text.UTF8Encoding]::new($false)
    [System.IO.File]::WriteAllText($placeholderPath, (($PlaceholderIndex -replace "`r`n", "`n") + "`n"), $utf8NoBom)
}

if (-not (Test-Path $OutputExe)) {
    throw "构建产物不存在：$OutputExe"
}

Write-Host "构建完成：$OutputExe" -ForegroundColor Green
