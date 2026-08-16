$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$RepoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$MeasureScript = Join-Path $RepoRoot "scripts\measure-hook-latency.ps1"
$PowerShellExe = (Get-Process -Id $PID).Path
$TmpBase = [IO.Path]::GetFullPath((Join-Path $RepoRoot ".tmp\hook-latency"))
$TmpRoot = Join-Path $TmpBase "tests-$PID-$([guid]::NewGuid().ToString('N'))"
$Failures = [System.Collections.Generic.List[string]]::new()

. $MeasureScript

function Assert-True {
    param(
        [Parameter(Mandatory = $true)][bool]$Condition,
        [Parameter(Mandatory = $true)][string]$Message
    )

    if (-not $Condition) {
        throw $Message
    }
}

function Assert-False {
    param(
        [Parameter(Mandatory = $true)][bool]$Condition,
        [Parameter(Mandatory = $true)][string]$Message
    )

    if ($Condition) {
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

function Get-ValidateSetValues {
    param(
        [Parameter(Mandatory = $true)]$CommandMetadata,
        [Parameter(Mandatory = $true)][string]$ParameterName
    )

    $attribute = $CommandMetadata.Parameters[$ParameterName].Attributes |
        Where-Object { $_ -is [Management.Automation.ValidateSetAttribute] } |
        Select-Object -First 1
    if ($null -eq $attribute) {
        throw "缺少 ValidateSet：$ParameterName"
    }
    return @($attribute.ValidValues)
}

function New-TestRepository {
    param([Parameter(Mandatory = $true)][string]$Name)

    $root = Join-Path $TmpRoot $Name
    New-Item -ItemType Directory -Force -Path (Join-Path $root ".git") | Out-Null
    New-Item -ItemType Directory -Force -Path (Join-Path $root "src") | Out-Null
    New-Item -ItemType Directory -Force -Path (Join-Path $root ".tmp\agenttoolgate") | Out-Null
    return $root
}

function New-FakeAgentToolGate {
    param([Parameter(Mandatory = $true)][string]$Directory)

    $path = Join-Path $Directory "fake-agenttoolgate.cmd"
    @'
@echo off
setlocal
set /p payload=
>>"%ATG_HOOK_LATENCY_TEST_LOG%" echo %*
echo {"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}
exit /b 0
'@ | Set-Content -LiteralPath $path -Encoding ASCII
    return $path
}

function Get-LogLineCount {
    param([Parameter(Mandatory = $true)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return 0
    }
    return @(Get-Content -LiteralPath $Path).Count
}

try {
    New-Item -ItemType Directory -Force -Path $TmpRoot | Out-Null

    Invoke-Case -Name "parameter metadata accepts both adapters and all modes" -Body {
        $metadata = Get-Command -Name $MeasureScript
        Assert-Equal `
            -Expected "claude,codex" `
            -Actual ((Get-ValidateSetValues -CommandMetadata $metadata -ParameterName "Adapter" | Sort-Object) -join ",") `
            -Message "Adapter 参数集合错误"
        Assert-Equal `
            -Expected "dry-run,live,off" `
            -Actual ((Get-ValidateSetValues -CommandMetadata $metadata -ParameterName "Mode" | Sort-Object) -join ",") `
            -Message "Mode 参数集合错误"

        $range = $metadata.Parameters["WarmSamples"].Attributes |
            Where-Object { $_ -is [Management.Automation.ValidateRangeAttribute] } |
            Select-Object -First 1
        Assert-True -Condition ($null -ne $range) -Message "WarmSamples 缺少 ValidateRange"
        Assert-Equal -Expected 100 -Actual $range.MinRange -Message "WarmSamples 最小值必须是 100"

        $benchmarkMetadata = Get-Command -Name "Invoke-HookLatencyBenchmark"
        foreach ($parameterName in @("RequestedAgentToolGateExecutable", "RequestedEndpoint")) {
            $allowsEmpty = @(
                $benchmarkMetadata.Parameters[$parameterName].Attributes |
                    Where-Object { $_ -is [Management.Automation.AllowEmptyStringAttribute] }
            ).Count -eq 1
            Assert-True -Condition $allowsEmpty -Message "$parameterName 必须接受 off/dry-run 的空 runtime 值"
        }
    }

    Invoke-Case -Name "invalid adapter and short warm set fail during parameter binding" -Body {
        & $PowerShellExe -NoProfile -File $MeasureScript -Adapter invalid *> $null
        Assert-True -Condition ($LASTEXITCODE -ne 0) -Message "非法 Adapter 必须非零退出"

        & $PowerShellExe -NoProfile -File $MeasureScript -WarmSamples 99 *> $null
        Assert-True -Condition ($LASTEXITCODE -ne 0) -Message "少于 100 个 warm 样本必须非零退出"
    }

    Invoke-Case -Name "nearest-rank p50 and p95 statistics are stable" -Body {
        $values = [double[]](1..100)
        Assert-Equal -Expected 50 -Actual (Get-Percentile -Values $values -Percentile 50) -Message "p50 错误"
        Assert-Equal -Expected 95 -Actual (Get-Percentile -Values $values -Percentile 95) -Message "p95 错误"

        $samples = @(
            [pscustomobject]@{
                elapsedMilliseconds = 1
                exitCode = 0
                timedOut = $false
                stdoutNonEmpty = $false
                stderrNonEmpty = $false
                goSubprocessProxyEligible = $true
            },
            [pscustomobject]@{
                elapsedMilliseconds = 3
                exitCode = 0
                timedOut = $false
                stdoutNonEmpty = $true
                stderrNonEmpty = $false
                goSubprocessProxyEligible = $true
            },
            [pscustomobject]@{
                elapsedMilliseconds = 2
                exitCode = 1
                timedOut = $true
                stdoutNonEmpty = $false
                stderrNonEmpty = $true
                goSubprocessProxyEligible = $true
            }
        )
        $summary = Get-SeriesSummary -Samples $samples
        Assert-Equal -Expected 2 -Actual $summary.p50Milliseconds -Message "series p50 错误"
        Assert-Equal -Expected 3 -Actual $summary.p95Milliseconds -Message "series p95 错误"
        Assert-Equal -Expected 1 -Actual $summary.stdoutNonEmptyCount -Message "stdout 计数错误"
        Assert-Equal -Expected 1 -Actual $summary.stderrNonEmptyCount -Message "stderr 计数错误"
        Assert-Equal -Expected 1 -Actual $summary.nonZeroExitCount -Message "退出码计数错误"
        Assert-Equal -Expected 1 -Actual $summary.timeoutCount -Message "超时计数错误"
    }

    Invoke-Case -Name "live threshold is enforced without affecting off and dry-run" -Body {
        Assert-Equal -Expected 0 -Actual (Get-BenchmarkExitCode -ControlMode live -WarmP95Milliseconds 250 -ThresholdMilliseconds 250 -SampleFailureCount 0) -Message "250ms 应通过"
        Assert-Equal -Expected 3 -Actual (Get-BenchmarkExitCode -ControlMode live -WarmP95Milliseconds 250.001 -ThresholdMilliseconds 250 -SampleFailureCount 0) -Message "超过 250ms 应失败"
        Assert-Equal -Expected 0 -Actual (Get-BenchmarkExitCode -ControlMode off -WarmP95Milliseconds 999 -ThresholdMilliseconds 250 -SampleFailureCount 0) -Message "off 不应用 live 门槛"
        Assert-Equal -Expected 0 -Actual (Get-BenchmarkExitCode -ControlMode dry-run -WarmP95Milliseconds 999 -ThresholdMilliseconds 250 -SampleFailureCount 0) -Message "dry-run 不应用 live 门槛"
        Assert-Equal -Expected 2 -Actual (Get-BenchmarkExitCode -ControlMode live -WarmP95Milliseconds 1 -ThresholdMilliseconds 250 -SampleFailureCount 1) -Message "样本失败优先返回非零"
    }

    Invoke-Case -Name "sensitive environment variables are removed without reading their values" -Body {
        $nonce = "$PID-$([guid]::NewGuid().ToString('N'))"
        $apiKeyName = "ATG_HOOK_LATENCY_${nonce}_API_KEY"
        $databaseUrlName = "ATG_HOOK_LATENCY_${nonce}_DATABASE_URL"
        $normalName = "ATG_HOOK_LATENCY_${nonce}_NORMAL"
        try {
            [Environment]::SetEnvironmentVariable($apiKeyName, "synthetic-test-value", "Process")
            [Environment]::SetEnvironmentVariable($databaseUrlName, "synthetic-test-value", "Process")
            [Environment]::SetEnvironmentVariable($normalName, "keep", "Process")
            $config = New-HookProcessStartInfo `
                -ResolvedPythonExecutable (Resolve-ExecutablePath -Value "python" -Purpose "Python") `
                -HookScriptPath (Resolve-HookScriptPath -RepositoryRoot $RepoRoot -AdapterName "codex") `
                -WorkingDirectory $RepoRoot

            Assert-False -Condition $config.StartInfo.Environment.ContainsKey($apiKeyName) -Message "API key 环境变量未移除"
            Assert-False -Condition $config.StartInfo.Environment.ContainsKey($databaseUrlName) -Message "数据库 URL 环境变量未移除"
            Assert-Equal -Expected "keep" -Actual $config.StartInfo.Environment[$normalName] -Message "普通环境变量不应删除"
            Assert-True -Condition ($config.SensitiveEnvironmentRemovedCount -ge 2) -Message "敏感环境移除计数错误"
        }
        finally {
            [Environment]::SetEnvironmentVariable($apiKeyName, $null, "Process")
            [Environment]::SetEnvironmentVariable($databaseUrlName, $null, "Process")
            [Environment]::SetEnvironmentVariable($normalName, $null, "Process")
        }
    }

    Invoke-Case -Name "each live sample executes one real Hook and one Go bridge proxy" -Body {
        $caseRoot = Join-Path $TmpRoot "single-hook"
        New-Item -ItemType Directory -Force -Path $caseRoot | Out-Null
        $fakeCli = New-FakeAgentToolGate -Directory $caseRoot
        $logPath = Join-Path $caseRoot "go-invocations.log"
        $samplesPath = Join-Path $caseRoot "samples.jsonl"
        $previousLog = $env:ATG_HOOK_LATENCY_TEST_LOG
        $env:ATG_HOOK_LATENCY_TEST_LOG = $logPath
        try {
            $python = Resolve-ExecutablePath -Value "python" -Purpose "Python"
            $repo = New-TestRepository -Name "single-hook\repo"
            Write-HookControl `
                -ControlPath (Join-Path $repo ".tmp\agenttoolgate\hook-control.json") `
                -ControlMode "live" `
                -RuntimeExecutable $fakeCli
            $payload = New-HookInputJson `
                -AdapterName "codex" `
                -FixtureRepository $repo `
                -InputClass "governed-code-exec"
            $hook = Resolve-HookScriptPath -RepositoryRoot $RepoRoot -AdapterName "codex"

            $samples = @(
                Invoke-HookSeries `
                    -ResolvedPythonExecutable $python `
                    -HookScriptPath $hook `
                    -WorkingDirectory $repo `
                    -PayloadJson $payload `
                    -Phase "warm" `
                    -Count 3 `
                    -TimeoutMilliseconds 5000 `
                    -GoSubprocessProxyEligible $true `
                    -SamplesPath $samplesPath
            )

            Assert-Equal -Expected 3 -Actual $samples.Count -Message "小样本 smoke 数量错误"
            Assert-Equal -Expected 3 -Actual (Get-LogLineCount -Path $logPath) -Message "每个 Hook 样本必须只启动一次 Go bridge"
            Assert-Equal -Expected 0 -Actual @($samples | Where-Object { $_.exitCode -ne 0 -or $_.timedOut }).Count -Message "真实 Hook smoke 失败"
            Assert-False -Condition ($samples[0].PSObject.Properties.Name -contains "stdout") -Message "样本不得保存 stdout 内容"
            Assert-False -Condition ($samples[0].PSObject.Properties.Name -contains "stderr") -Message "样本不得保存 stderr 内容"
            $persistedSamples = Get-Content -LiteralPath $samplesPath -Raw
            Assert-False -Condition $persistedSamples.Contains("hookSpecificOutput") -Message "JSONL 不得保存 Hook stdout 正文"
            Assert-False -Condition $persistedSamples.Contains("permissionDecision") -Message "JSONL 不得保存 Hook decision 正文"
        }
        finally {
            $env:ATG_HOOK_LATENCY_TEST_LOG = $previousLog
        }
    }

    Invoke-Case -Name "low-friction live read stays on the local fast path" -Body {
        $caseRoot = Join-Path $TmpRoot "local-fast-path"
        New-Item -ItemType Directory -Force -Path $caseRoot | Out-Null
        $fakeCli = New-FakeAgentToolGate -Directory $caseRoot
        $logPath = Join-Path $caseRoot "go-invocations.log"
        $previousLog = $env:ATG_HOOK_LATENCY_TEST_LOG
        $env:ATG_HOOK_LATENCY_TEST_LOG = $logPath
        try {
            $repo = New-TestRepository -Name "local-fast-path\repo"
            Write-HookControl `
                -ControlPath (Join-Path $repo ".tmp\agenttoolgate\hook-control.json") `
                -ControlMode "live" `
                -RuntimeExecutable $fakeCli
            $sample = Invoke-HookSample `
                -ResolvedPythonExecutable (Resolve-ExecutablePath -Value "python" -Purpose "Python") `
                -HookScriptPath (Resolve-HookScriptPath -RepositoryRoot $RepoRoot -AdapterName "codex") `
                -WorkingDirectory $repo `
                -PayloadJson (New-HookInputJson -AdapterName "codex" -FixtureRepository $repo) `
                -Phase "cold" `
                -Index 1 `
                -TimeoutMilliseconds 5000 `
                -GoSubprocessProxyEligible $false

            Assert-Equal -Expected 0 -Actual $sample.exitCode -Message "低摩擦读取 Hook 失败"
            Assert-Equal -Expected 0 -Actual (Get-LogLineCount -Path $logPath) -Message "低摩擦读取不得启动 Go bridge"
        }
        finally {
            $env:ATG_HOOK_LATENCY_TEST_LOG = $previousLog
        }
    }

    Invoke-Case -Name "both adapters support live while off and dry-run skip the Go bridge" -Body {
        $caseRoot = Join-Path $TmpRoot "modes-and-adapters"
        New-Item -ItemType Directory -Force -Path $caseRoot | Out-Null
        $fakeCli = New-FakeAgentToolGate -Directory $caseRoot
        $logPath = Join-Path $caseRoot "go-invocations.log"
        $previousLog = $env:ATG_HOOK_LATENCY_TEST_LOG
        $env:ATG_HOOK_LATENCY_TEST_LOG = $logPath
        try {
            $python = Resolve-ExecutablePath -Value "python" -Purpose "Python"
            foreach ($adapterName in @("codex", "claude")) {
                $repo = New-TestRepository -Name "modes-and-adapters\$adapterName-live"
                Write-HookControl `
                    -ControlPath (Join-Path $repo ".tmp\agenttoolgate\hook-control.json") `
                    -ControlMode "live" `
                    -RuntimeExecutable $fakeCli
                $sample = Invoke-HookSample `
                    -ResolvedPythonExecutable $python `
                    -HookScriptPath (Resolve-HookScriptPath -RepositoryRoot $RepoRoot -AdapterName $adapterName) `
                    -WorkingDirectory $repo `
                    -PayloadJson (New-HookInputJson `
                        -AdapterName $adapterName `
                        -FixtureRepository $repo `
                        -InputClass "governed-code-exec") `
                    -Phase "cold" `
                    -Index 1 `
                    -TimeoutMilliseconds 5000 `
                    -GoSubprocessProxyEligible $true
                Assert-Equal -Expected 0 -Actual $sample.exitCode -Message "$adapterName live Hook 失败"
                Assert-False -Condition $sample.timedOut -Message "$adapterName live Hook 超时"
            }
            Assert-Equal -Expected 2 -Actual (Get-LogLineCount -Path $logPath) -Message "两套 live Hook 都应各调用一次 bridge"

            foreach ($adapterName in @("codex", "claude")) {
                foreach ($modeName in @("off", "dry-run")) {
                    $repo = New-TestRepository -Name "modes-and-adapters\$adapterName-$modeName"
                    Write-HookControl `
                        -ControlPath (Join-Path $repo ".tmp\agenttoolgate\hook-control.json") `
                        -ControlMode $modeName
                    $sample = Invoke-HookSample `
                        -ResolvedPythonExecutable $python `
                        -HookScriptPath (Resolve-HookScriptPath -RepositoryRoot $RepoRoot -AdapterName $adapterName) `
                        -WorkingDirectory $repo `
                        -PayloadJson (New-HookInputJson -AdapterName $adapterName -FixtureRepository $repo) `
                        -Phase "cold" `
                        -Index 1 `
                        -TimeoutMilliseconds 5000 `
                        -GoSubprocessProxyEligible $false
                    Assert-Equal -Expected 0 -Actual $sample.exitCode -Message "$adapterName $modeName Hook 失败"
                    Assert-False -Condition $sample.timedOut -Message "$adapterName $modeName Hook 超时"
                }
            }
            Assert-Equal -Expected 2 -Actual (Get-LogLineCount -Path $logPath) -Message "off/dry-run 不得启动 Go bridge"
        }
        finally {
            $env:ATG_HOOK_LATENCY_TEST_LOG = $previousLog
        }
    }

    if ($Failures.Count -gt 0) {
        throw ($Failures -join [Environment]::NewLine)
    }

    Write-Host "Hook latency PowerShell regression tests passed." -ForegroundColor Green
}
finally {
    $resolvedTmpBase = [IO.Path]::GetFullPath($TmpBase) + [IO.Path]::DirectorySeparatorChar
    $resolvedTmpRoot = [IO.Path]::GetFullPath($TmpRoot)
    if ($resolvedTmpRoot.StartsWith($resolvedTmpBase, [StringComparison]::OrdinalIgnoreCase)) {
        Remove-Item -LiteralPath $resolvedTmpRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}
