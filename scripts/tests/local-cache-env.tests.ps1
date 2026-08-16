$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$RepoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$CacheScript = Join-Path $RepoRoot "scripts\local-cache-env.ps1"
$Failures = [System.Collections.Generic.List[string]]::new()
$Names = @(
    "GOCACHE",
    "GOMODCACHE",
    "GOPATH",
    "GOTMPDIR",
    "NPM_CONFIG_CACHE",
    "npm_config_cache",
    "PIP_CACHE_DIR",
    "PLAYWRIGHT_BROWSERS_PATH",
    "TEMP",
    "TMP",
    "TMPDIR",
    "PYTHONPYCACHEPREFIX"
)
$PreviousProcess = @{}
$PreviousUser = @{}
$PreviousMachine = @{}

foreach ($name in $Names) {
    $PreviousProcess[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
    $PreviousUser[$name] = [Environment]::GetEnvironmentVariable($name, "User")
    $PreviousMachine[$name] = [Environment]::GetEnvironmentVariable($name, "Machine")
}

function Assert-True {
    param(
        [Parameter(Mandatory = $true)][bool]$Condition,
        [Parameter(Mandatory = $true)][string]$Message
    )

    if (-not $Condition) {
        throw $Message
    }
}

function Assert-Equal {
    param(
        [AllowNull()]$Expected,
        [AllowNull()]$Actual,
        [Parameter(Mandatory = $true)][string]$Message
    )

    if ($Expected -ne $Actual) {
        throw "$Message expected=$Expected actual=$Actual"
    }
}

function Invoke-Case {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][scriptblock]$Body
    )

    try {
        & $Body
        Write-Host "[PASS] $Name" -ForegroundColor Green
    }
    catch {
        $Failures.Add("$Name`: $($_.Exception.Message)")
        Write-Host "[FAIL] $Name`: $($_.Exception.Message)" -ForegroundColor Red
    }
}

try {
    & $CacheScript -Quiet

    $repoPrefix = [IO.Path]::GetFullPath($RepoRoot).TrimEnd("\", "/") + [IO.Path]::DirectorySeparatorChar
    $workspaceParent = Split-Path -Parent $RepoRoot
    $workspacePrefix = [IO.Path]::GetFullPath($workspaceParent).TrimEnd("\", "/") +
        [IO.Path]::DirectorySeparatorChar

    Invoke-Case -Name "heavy caches stay under repository tmp" -Body {
        foreach ($name in @(
            "GOCACHE",
            "GOMODCACHE",
            "GOPATH",
            "NPM_CONFIG_CACHE",
            "PIP_CACHE_DIR",
            "PLAYWRIGHT_BROWSERS_PATH"
        )) {
            $value = [Environment]::GetEnvironmentVariable($name, "Process")
            Assert-True -Condition $value.StartsWith(
                $repoPrefix,
                [StringComparison]::OrdinalIgnoreCase
            ) -Message "$name 必须位于仓库内"
            Assert-True -Condition (Test-Path -LiteralPath $value -PathType Container) `
                -Message "$name 目录必须存在"
        }
    }

    Invoke-Case -Name "runtime temp stays on workspace drive but outside repository" -Body {
        foreach ($name in @("GOTMPDIR", "TEMP", "TMP", "TMPDIR", "PYTHONPYCACHEPREFIX")) {
            $value = [Environment]::GetEnvironmentVariable($name, "Process")
            Assert-True -Condition $value.StartsWith(
                $workspacePrefix,
                [StringComparison]::OrdinalIgnoreCase
            ) -Message "$name 必须位于工作区所在盘"
            Assert-True -Condition (-not $value.StartsWith(
                $repoPrefix,
                [StringComparison]::OrdinalIgnoreCase
            )) -Message "$name 不能位于被保护仓库内部"
            Assert-True -Condition (Test-Path -LiteralPath $value -PathType Container) `
                -Message "$name 目录必须存在"
        }
    }

    Invoke-Case -Name "npm lowercase and uppercase cache variables agree" -Body {
        Assert-Equal `
            -Expected $env:NPM_CONFIG_CACHE `
            -Actual $env:npm_config_cache `
            -Message "npm cache 环境变量不一致"
    }

    Invoke-Case -Name "npm explicit cache uses repository tmp" -Body {
        $actual = (& npm --cache $env:NPM_CONFIG_CACHE config get cache).Trim()
        Assert-Equal `
            -Expected ([IO.Path]::GetFullPath($env:NPM_CONFIG_CACHE)) `
            -Actual ([IO.Path]::GetFullPath($actual)) `
            -Message "npm 实际缓存目录不一致"
    }

    Invoke-Case -Name "user and machine environment remain unchanged" -Body {
        foreach ($name in $Names) {
            Assert-Equal `
                -Expected $PreviousUser[$name] `
                -Actual ([Environment]::GetEnvironmentVariable($name, "User")) `
                -Message "$name 用户级环境被修改"
            Assert-Equal `
                -Expected $PreviousMachine[$name] `
                -Actual ([Environment]::GetEnvironmentVariable($name, "Machine")) `
                -Message "$name 机器级环境被修改"
        }
    }
}
finally {
    foreach ($name in $Names) {
        [Environment]::SetEnvironmentVariable($name, $PreviousProcess[$name], "Process")
    }
}

if ($Failures.Count -gt 0) {
    throw ($Failures -join [Environment]::NewLine)
}

Write-Host "local cache environment tests passed" -ForegroundColor Green
