#!/usr/bin/env pwsh
[CmdletBinding()]
param(
    [string]$RepositoryRoot = "",
    [switch]$Quiet
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Resolve-LocalCacheRoot {
    param(
        [Parameter(Mandatory = $true)]
        [AllowEmptyString()]
        [string]$RequestedRoot
    )

    $candidate = $RequestedRoot.Trim()
    if ([string]::IsNullOrWhiteSpace($candidate)) {
        $candidate = Split-Path -Parent $PSScriptRoot
    }
    $resolved = [IO.Path]::GetFullPath($candidate)
    if (-not (Test-Path -LiteralPath $resolved -PathType Container)) {
        throw "项目目录不存在：$resolved"
    }
    return $resolved
}

function Assert-LocalCachePath {
    param(
        [Parameter(Mandatory = $true)][string]$BasePath,
        [Parameter(Mandatory = $true)][string]$CandidatePath
    )

    $base = [IO.Path]::GetFullPath($BasePath).TrimEnd(
        [IO.Path]::DirectorySeparatorChar,
        [IO.Path]::AltDirectorySeparatorChar
    )
    $candidate = [IO.Path]::GetFullPath($CandidatePath)
    $prefix = $base + [IO.Path]::DirectorySeparatorChar
    if (-not $candidate.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "缓存路径超出预期目录：$candidate"
    }
}

function New-LocalCacheDirectory {
    param(
        [Parameter(Mandatory = $true)][string]$BasePath,
        [Parameter(Mandatory = $true)][string]$Path
    )

    Assert-LocalCachePath -BasePath $BasePath -CandidatePath $Path
    New-Item -ItemType Directory -Force -Path $Path | Out-Null
    $item = Get-Item -LiteralPath $Path -Force
    if ($item.Attributes.HasFlag([IO.FileAttributes]::ReparsePoint)) {
        throw "缓存目录不能是链接或重解析点：$Path"
    }
}

$repoRoot = Resolve-LocalCacheRoot -RequestedRoot $RepositoryRoot
$repoName = Split-Path -Leaf $repoRoot
$repoTmp = Join-Path $repoRoot ".tmp"
$cacheRoot = Join-Path $repoTmp "cache"
$workspaceParent = Split-Path -Parent $repoRoot
$workspaceTmp = Join-Path $workspaceParent ".tmp"
$runtimeRoot = Join-Path $workspaceTmp "$repoName-runtime"
$goTmpRoot = Join-Path $workspaceTmp "$repoName-go"
$pythonCacheRoot = Join-Path $workspaceTmp "$repoName-pycache"

$paths = [ordered]@{
    GOCACHE                  = Join-Path $cacheRoot "go-build"
    GOMODCACHE               = Join-Path $cacheRoot "go-mod"
    GOPATH                   = Join-Path $cacheRoot "gopath"
    NPM_CONFIG_CACHE         = Join-Path $cacheRoot "npm"
    PIP_CACHE_DIR            = Join-Path $cacheRoot "pip"
    PLAYWRIGHT_BROWSERS_PATH = Join-Path $cacheRoot "playwright"
    GOTMPDIR                 = $goTmpRoot
    TEMP                     = $runtimeRoot
    TMP                      = $runtimeRoot
    TMPDIR                   = $runtimeRoot
    PYTHONPYCACHEPREFIX      = $pythonCacheRoot
}

foreach ($path in @(
    $repoTmp,
    $cacheRoot,
    $paths.GOCACHE,
    $paths.GOMODCACHE,
    $paths.GOPATH,
    $paths.NPM_CONFIG_CACHE,
    $paths.PIP_CACHE_DIR,
    $paths.PLAYWRIGHT_BROWSERS_PATH
)) {
    New-LocalCacheDirectory -BasePath $repoRoot -Path $path
}

foreach ($path in @($workspaceTmp, $runtimeRoot, $goTmpRoot, $pythonCacheRoot)) {
    New-LocalCacheDirectory -BasePath $workspaceParent -Path $path
}

foreach ($entry in $paths.GetEnumerator()) {
    [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, "Process")
}
# npm 在类 Unix 环境使用小写前缀；Windows 环境变量不区分大小写。
[Environment]::SetEnvironmentVariable("npm_config_cache", $paths.NPM_CONFIG_CACHE, "Process")

if (-not $Quiet) {
    Write-Host "AgentToolGate 本地缓存环境已启用（仅当前 PowerShell 进程）。" -ForegroundColor Green
    Write-Host "Go cache: $($paths.GOCACHE)"
    Write-Host "Go modules: $($paths.GOMODCACHE)"
    Write-Host "Go temp: $($paths.GOTMPDIR)"
    Write-Host "Runtime temp: $($paths.TEMP)"
}
