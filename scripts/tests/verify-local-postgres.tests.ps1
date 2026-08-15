$ErrorActionPreference = "Stop"

$RepoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$VerifyScript = Join-Path $RepoRoot "scripts\verify-local.ps1"
# 只加载 PostgreSQL 生命周期函数，避免测试触发整套本地验证。
$Tokens = $null
$ParseErrors = $null
$VerifyAst = [System.Management.Automation.Language.Parser]::ParseFile(
    $VerifyScript,
    [ref]$Tokens,
    [ref]$ParseErrors
)

if ($ParseErrors.Count -gt 0) {
    throw "verify-local.ps1 parse failed: $($ParseErrors[0].Message)"
}

foreach ($FunctionName in @(
    "Test-PostgresRunning",
    "Start-LocalPostgresIfNeeded",
    "Stop-LocalPostgresIfStarted"
)) {
    $FunctionAst = $VerifyAst.Find({
        param($Node)
        $Node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
            $Node.Name -eq $FunctionName
    }, $true)
    if ($null -eq $FunctionAst) {
        throw "Function not found in verify-local.ps1: $FunctionName"
    }
    . ([scriptblock]::Create($FunctionAst.Extent.Text))
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
        [Parameter(Mandatory = $true)]$Expected,
        [Parameter(Mandatory = $true)]$Actual,
        [Parameter(Mandatory = $true)][string]$Message
    )

    if ($Expected -ne $Actual) {
        throw "$Message expected=$Expected actual=$Actual"
    }
}

$TmpRoot = Join-Path $RepoRoot ".tmp\verify-local-postgres-tests-$PID-$([guid]::NewGuid().ToString('N'))"
$script:PgData = Join-Path $TmpRoot "data"
$script:PgCtl = Join-Path $TmpRoot "fake-pg-ctl.ps1"
$StateFile = Join-Path $TmpRoot "running"
$LogFile = Join-Path $TmpRoot "actions.log"
$Failures = [System.Collections.Generic.List[string]]::new()
$PreviousLog = $env:AGT_VERIFY_PG_LOG
$PreviousState = $env:AGT_VERIFY_PG_STATE
$PreviousStartExit = $env:AGT_VERIFY_PG_START_EXIT
$PreviousStopExit = $env:AGT_VERIFY_PG_STOP_EXIT

function Reset-FakePostgres {
    Remove-Item -Force -ErrorAction SilentlyContinue $StateFile, $LogFile
    $script:pgStartedByScript = $false
    $env:AGT_VERIFY_PG_START_EXIT = "0"
    $env:AGT_VERIFY_PG_STOP_EXIT = "0"
}

function Get-ActionCount {
    param([Parameter(Mandatory = $true)][string]$Action)

    if (-not (Test-Path -LiteralPath $LogFile)) {
        return 0
    }
    return @(
        Get-Content -LiteralPath $LogFile |
            Where-Object { $_ -eq $Action }
    ).Count
}

function Invoke-Case {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][scriptblock]$Body
    )

    try {
        Reset-FakePostgres
        & $Body
        Write-Host "[PASS] $Name" -ForegroundColor Green
    }
    catch {
        $Failures.Add("$Name`: $($_.Exception.Message)")
        Write-Host "[FAIL] $Name`: $($_.Exception.Message)" -ForegroundColor Red
    }
}

try {
    New-Item -ItemType Directory -Force -Path $PgData | Out-Null
    @'
param(
    [Parameter(Position = 0)][string]$Action,
    [Parameter(ValueFromRemainingArguments = $true)][string[]]$Remaining
)

Add-Content -LiteralPath $env:AGT_VERIFY_PG_LOG -Value $Action

switch ($Action) {
    "status" {
        if (Test-Path -LiteralPath $env:AGT_VERIFY_PG_STATE) {
            exit 0
        }
        exit 1
    }
    "start" {
        $exitCode = [int]$env:AGT_VERIFY_PG_START_EXIT
        if ($exitCode -eq 0) {
            New-Item -ItemType File -Force -Path $env:AGT_VERIFY_PG_STATE | Out-Null
        }
        exit $exitCode
    }
    "stop" {
        $exitCode = [int]$env:AGT_VERIFY_PG_STOP_EXIT
        if ($exitCode -eq 0) {
            Remove-Item -Force -ErrorAction SilentlyContinue $env:AGT_VERIFY_PG_STATE
        }
        exit $exitCode
    }
    default {
        exit 2
    }
}
'@ | Set-Content -LiteralPath $PgCtl -Encoding UTF8

    $env:AGT_VERIFY_PG_LOG = $LogFile
    $env:AGT_VERIFY_PG_STATE = $StateFile

    Invoke-Case -Name "existing instance is preserved" -Body {
        New-Item -ItemType File -Force -Path $StateFile | Out-Null

        Start-LocalPostgresIfNeeded
        Stop-LocalPostgresIfStarted

        Assert-True -Condition (-not $script:pgStartedByScript) -Message "existing instance must not be owned by the script"
        Assert-True -Condition (Test-Path -LiteralPath $StateFile) -Message "existing instance must remain running"
        Assert-Equal -Expected 0 -Actual (Get-ActionCount -Action "stop") -Message "existing instance must not receive stop"
    }

    Invoke-Case -Name "script-started instance stops once" -Body {
        Start-LocalPostgresIfNeeded
        Start-LocalPostgresIfNeeded
        Stop-LocalPostgresIfStarted
        Stop-LocalPostgresIfStarted

        Assert-True -Condition (-not $script:pgStartedByScript) -Message "successful cleanup must release ownership"
        Assert-True -Condition (-not (Test-Path -LiteralPath $StateFile)) -Message "script-started instance must stop"
        Assert-Equal -Expected 1 -Actual (Get-ActionCount -Action "start") -Message "repeated checks must not restart"
        Assert-Equal -Expected 1 -Actual (Get-ActionCount -Action "stop") -Message "cleanup must stop only once"
    }

    Invoke-Case -Name "failed start is not stopped" -Body {
        $env:AGT_VERIFY_PG_START_EXIT = "1"
        $Thrown = $false

        try {
            Start-LocalPostgresIfNeeded
        }
        catch {
            $Thrown = $true
        }
        Stop-LocalPostgresIfStarted

        Assert-True -Condition $Thrown -Message "failed start must return an error"
        Assert-True -Condition (-not $script:pgStartedByScript) -Message "failed start must not claim ownership"
        Assert-Equal -Expected 0 -Actual (Get-ActionCount -Action "stop") -Message "failed start must not be stopped"
    }

    if ($Failures.Count -gt 0) {
        throw ($Failures -join [Environment]::NewLine)
    }

    Write-Host "PostgreSQL lifecycle regression tests passed." -ForegroundColor Green
}
finally {
    $env:AGT_VERIFY_PG_LOG = $PreviousLog
    $env:AGT_VERIFY_PG_STATE = $PreviousState
    $env:AGT_VERIFY_PG_START_EXIT = $PreviousStartExit
    $env:AGT_VERIFY_PG_STOP_EXIT = $PreviousStopExit

    $TmpBase = [IO.Path]::GetFullPath((Join-Path $RepoRoot ".tmp")) + [IO.Path]::DirectorySeparatorChar
    $ResolvedTmpRoot = [IO.Path]::GetFullPath($TmpRoot)
    if ($ResolvedTmpRoot.StartsWith($TmpBase, [StringComparison]::OrdinalIgnoreCase)) {
        Remove-Item -LiteralPath $ResolvedTmpRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}
