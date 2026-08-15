#!/usr/bin/env pwsh
[CmdletBinding()]
param(
    [ValidateSet("codex", "claude")]
    [string]$Adapter = "codex",

    [ValidateSet("off", "dry-run", "live")]
    [string]$Mode = "live",

    [ValidateSet("low-friction-read", "governed-code-exec")]
    [string]$PayloadClass = "low-friction-read",

    [ValidateRange(100, 100000)]
    [int]$WarmSamples = 100,

    [ValidateRange(0, 10000)]
    [int]$WarmupSamples = 5,

    [ValidateRange(100, 600000)]
    [int]$SampleTimeoutMilliseconds = 10000,

    [ValidateRange(1, 600000)]
    [double]$P95ThresholdMilliseconds = 250,

    [ValidateRange(1, 600000)]
    [double]$TargetMilliseconds = 200,

    [string]$PythonExecutable = "python",

    [string]$AgentToolGateExecutable = "",

    [string]$Endpoint = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Resolve-ExecutablePath {
    param(
        [Parameter(Mandatory = $true)][string]$Value,
        [Parameter(Mandatory = $true)][string]$Purpose
    )

    $candidate = $Value.Trim()
    if ([string]::IsNullOrWhiteSpace($candidate)) {
        throw "$Purpose 不能为空。"
    }

    if ([IO.Path]::IsPathRooted($candidate) -or $candidate.Contains([IO.Path]::DirectorySeparatorChar) -or
        $candidate.Contains([IO.Path]::AltDirectorySeparatorChar)) {
        $resolved = [IO.Path]::GetFullPath($candidate)
        if (-not (Test-Path -LiteralPath $resolved -PathType Leaf)) {
            throw "$Purpose 不存在：$resolved"
        }
        return $resolved
    }

    $command = Get-Command -Name $candidate -CommandType Application -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if ($null -eq $command) {
        throw "未找到 $Purpose：$candidate"
    }
    return $command.Source
}

function Resolve-HookScriptPath {
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][ValidateSet("codex", "claude")][string]$AdapterName
    )

    $relativePath = if ($AdapterName -eq "codex") {
        ".codex\hooks\agent-guard-pretool.py"
    }
    else {
        ".claude\hooks\agent-guard-pretool.py"
    }
    $resolved = [IO.Path]::GetFullPath((Join-Path $RepositoryRoot $relativePath))
    if (-not (Test-Path -LiteralPath $resolved -PathType Leaf)) {
        throw "真实 Hook 不存在：$resolved"
    }
    return $resolved
}

function Get-ExistingHookRuntime {
    param([Parameter(Mandatory = $true)][string]$RepositoryRoot)

    $controlPath = Join-Path $RepositoryRoot ".tmp\agenttoolgate\hook-control.json"
    if (-not (Test-Path -LiteralPath $controlPath -PathType Leaf)) {
        return [pscustomobject]@{
            Endpoint   = ""
            Executable = ""
        }
    }

    try {
        $control = Get-Content -LiteralPath $controlPath -Raw -Encoding UTF8 | ConvertFrom-Json
    }
    catch {
        return [pscustomobject]@{
            Endpoint   = ""
            Executable = ""
        }
    }

    $runtimeEndpoint = if ($control.PSObject.Properties.Name -contains "endpoint" -and
        $control.endpoint -is [string]) {
        $control.endpoint.Trim()
    }
    else {
        ""
    }
    $runtimeExecutable = if ($control.PSObject.Properties.Name -contains "executable" -and
        $control.executable -is [string]) {
        $control.executable.Trim()
    }
    else {
        ""
    }

    return [pscustomobject]@{
        Endpoint   = $runtimeEndpoint
        Executable = $runtimeExecutable
    }
}

function Test-LoopbackHttpEndpoint {
    param([Parameter(Mandatory = $true)][string]$Value)

    $uri = $null
    if (-not [Uri]::TryCreate($Value, [UriKind]::Absolute, [ref]$uri)) {
        return $false
    }
    $hostName = $uri.Host.Trim("[", "]").ToLowerInvariant()
    return (
        $uri.Scheme -eq "http" -and
        $hostName -in @("127.0.0.1", "localhost", "::1") -and
        -not $uri.IsDefaultPort -and
        $uri.AbsolutePath -eq "/" -and
        [string]::IsNullOrEmpty($uri.Query) -and
        [string]::IsNullOrEmpty($uri.Fragment) -and
        [string]::IsNullOrEmpty($uri.UserInfo)
    )
}

function Resolve-AgentToolGateRuntime {
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][AllowEmptyString()][string]$RequestedExecutable,
        [Parameter(Mandatory = $true)][AllowEmptyString()][string]$RequestedEndpoint
    )

    $existing = Get-ExistingHookRuntime -RepositoryRoot $RepositoryRoot
    $executableCandidate = $RequestedExecutable.Trim()
    $executableSource = "parameter"
    if ([string]::IsNullOrWhiteSpace($executableCandidate)) {
        $executableCandidate = $existing.Executable
        $executableSource = "existing-control"
    }
    if ([string]::IsNullOrWhiteSpace($executableCandidate)) {
        foreach ($name in @("agenttoolgate.exe", "agenttoolgate")) {
            $command = Get-Command -Name $name -CommandType Application -ErrorAction SilentlyContinue |
                Select-Object -First 1
            if ($null -ne $command) {
                $executableCandidate = $command.Source
                $executableSource = "path"
                break
            }
        }
    }
    if ([string]::IsNullOrWhiteSpace($executableCandidate)) {
        foreach ($candidate in @(
            (Join-Path $RepositoryRoot ".tmp\bin\agenttoolgate.exe"),
            (Join-Path $RepositoryRoot "dist\agenttoolgate.exe"),
            (Join-Path $RepositoryRoot ".tmp\bin\agenttoolgate")
        )) {
            if (Test-Path -LiteralPath $candidate -PathType Leaf) {
                $executableCandidate = $candidate
                $executableSource = "repository-artifact"
                break
            }
        }
    }
    if ([string]::IsNullOrWhiteSpace($executableCandidate)) {
        throw "live 模式需要真实 AgentToolGate 可执行文件；请使用 -AgentToolGateExecutable 指定。"
    }
    $resolvedExecutable = Resolve-ExecutablePath -Value $executableCandidate -Purpose "AgentToolGate 可执行文件"

    $runtimeEndpoint = $RequestedEndpoint.Trim()
    $endpointSource = "parameter"
    if ([string]::IsNullOrWhiteSpace($runtimeEndpoint)) {
        $runtimeEndpoint = $existing.Endpoint
        $endpointSource = if ([string]::IsNullOrWhiteSpace($runtimeEndpoint)) { "hook-default" } else { "existing-control" }
    }
    if (-not [string]::IsNullOrWhiteSpace($runtimeEndpoint) -and
        -not (Test-LoopbackHttpEndpoint -Value $runtimeEndpoint)) {
        throw "Endpoint 必须是带显式端口、无凭据和 query 的 loopback HTTP 地址。"
    }

    return [pscustomobject]@{
        Executable       = $resolvedExecutable
        ExecutableSource = $executableSource
        Endpoint         = $runtimeEndpoint
        EndpointSource   = $endpointSource
    }
}

function New-HookLatencyFixture {
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][ValidateSet("codex", "claude")][string]$AdapterName,
        [Parameter(Mandatory = $true)][ValidateSet("off", "dry-run", "live")][string]$ControlMode
    )

    $artifactRoot = [IO.Path]::GetFullPath((Join-Path $RepositoryRoot ".tmp\hook-latency"))
    New-Item -ItemType Directory -Force -Path $artifactRoot | Out-Null

    $timestamp = [DateTime]::UtcNow.ToString("yyyyMMddTHHmmssfffZ")
    $suffix = [guid]::NewGuid().ToString("N").Substring(0, 8)
    $runId = "$timestamp-$AdapterName-$ControlMode-$PID-$suffix"
    $runDirectory = Join-Path $artifactRoot $runId
    $fixtureRepository = Join-Path $runDirectory "fixture-repo"

    New-Item -ItemType Directory -Force -Path (Join-Path $fixtureRepository ".git") | Out-Null
    New-Item -ItemType Directory -Force -Path (Join-Path $fixtureRepository "src") | Out-Null
    New-Item -ItemType Directory -Force -Path (Join-Path $fixtureRepository ".tmp\agenttoolgate") | Out-Null

    return [pscustomobject]@{
        ArtifactRoot      = $artifactRoot
        RunId             = $runId
        RunDirectory      = $runDirectory
        FixtureRepository = $fixtureRepository
        ControlPath       = Join-Path $fixtureRepository ".tmp\agenttoolgate\hook-control.json"
        SamplesPath       = Join-Path $runDirectory "samples.jsonl"
        SummaryPath       = Join-Path $runDirectory "summary.json"
    }
}

function Write-HookControl {
    param(
        [Parameter(Mandatory = $true)][string]$ControlPath,
        [Parameter(Mandatory = $true)][ValidateSet("off", "dry-run", "live")][string]$ControlMode,
        [string]$RuntimeEndpoint = "",
        [string]$RuntimeExecutable = ""
    )

    $control = [ordered]@{
        mode      = $ControlMode
        updatedAt = [DateTime]::UtcNow.ToString("o")
        reason    = "synthetic Hook latency baseline"
    }
    if (-not [string]::IsNullOrWhiteSpace($RuntimeEndpoint)) {
        $control.endpoint = $RuntimeEndpoint
    }
    if (-not [string]::IsNullOrWhiteSpace($RuntimeExecutable)) {
        $control.executable = [IO.Path]::GetFullPath($RuntimeExecutable)
    }

    $json = $control | ConvertTo-Json -Depth 4
    [IO.File]::WriteAllText($ControlPath, "$json`n", [Text.UTF8Encoding]::new($false))
}

function New-HookInputJson {
    param(
        [Parameter(Mandatory = $true)][ValidateSet("codex", "claude")][string]$AdapterName,
        [Parameter(Mandatory = $true)][string]$FixtureRepository,
        [ValidateSet("low-friction-read", "governed-code-exec")]
        [string]$InputClass = "low-friction-read"
    )

    if ($InputClass -eq "governed-code-exec") {
        $toolName = "shell_command"
        $toolInput = [ordered]@{ command = "go test ./..." }
    }
    else {
        $toolName = "Read"
        $toolInput = [ordered]@{ file_path = "src/hook-latency-probe.txt" }
    }
    $payload = [ordered]@{
        hook_event_name = "PreToolUse"
        tool_name       = $toolName
        tool_input      = $toolInput
        cwd             = $FixtureRepository
    }
    return ($payload | ConvertTo-Json -Depth 8 -Compress)
}

function Test-SensitiveEnvironmentVariableName {
    param([Parameter(Mandatory = $true)][string]$Name)

    return $Name -match '(?i)(secret|token|password|passwd|credential|authorization|cookie|api[_-]?key|access[_-]?key|private[_-]?key|client[_-]?secret|database[_-]?url|redis[_-]?url|connection[_-]?string|(^|[_-])dsn($|[_-]))'
}

function Add-ProcessArgument {
    param(
        [Parameter(Mandatory = $true)][Diagnostics.ProcessStartInfo]$StartInfo,
        [Parameter(Mandatory = $true)][string]$Value
    )

    if ($StartInfo.PSObject.Properties.Name -contains "ArgumentList") {
        $StartInfo.ArgumentList.Add($Value)
        return
    }

    $escaped = $Value.Replace('\', '\\').Replace('"', '\"')
    $rendered = '"' + $escaped + '"'
    if ([string]::IsNullOrWhiteSpace($StartInfo.Arguments)) {
        $StartInfo.Arguments = $rendered
    }
    else {
        $StartInfo.Arguments += " $rendered"
    }
}

function New-HookProcessStartInfo {
    param(
        [Parameter(Mandatory = $true)][string]$ResolvedPythonExecutable,
        [Parameter(Mandatory = $true)][string]$HookScriptPath,
        [Parameter(Mandatory = $true)][string]$WorkingDirectory
    )

    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $ResolvedPythonExecutable
    $startInfo.WorkingDirectory = $WorkingDirectory
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardInput = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $startInfo.StandardInputEncoding = [Text.UTF8Encoding]::new($false)
    $startInfo.StandardOutputEncoding = [Text.UTF8Encoding]::new($false)
    $startInfo.StandardErrorEncoding = [Text.UTF8Encoding]::new($false)
    Add-ProcessArgument -StartInfo $startInfo -Value "-B"
    Add-ProcessArgument -StartInfo $startInfo -Value $HookScriptPath

    $removedCount = 0
    foreach ($name in @($startInfo.Environment.Keys)) {
        if (Test-SensitiveEnvironmentVariableName -Name $name) {
            if ($startInfo.Environment.Remove($name)) {
                $removedCount++
            }
        }
    }
    foreach ($name in @(
        "AGENTTOOLGATE_ACTOR",
        "AGENTTOOLGATE_BEARER_TOKEN",
        "AGENTTOOLGATE_EXE",
        "AGENTTOOLGATE_URL",
        "AGENTTOOLGATE_WORKSPACE_ORG_ID",
        "WORKSPACE_ORG_ID",
        "TRELLIS_DISABLE_HOOKS",
        "TRELLIS_HOOKS"
    )) {
        if ($startInfo.Environment.Remove($name)) {
            $removedCount++
        }
    }
    $startInfo.Environment["PYTHONDONTWRITEBYTECODE"] = "1"
    $startInfo.Environment["PYTHONUTF8"] = "1"

    return [pscustomobject]@{
        StartInfo                         = $startInfo
        SensitiveEnvironmentRemovedCount = $removedCount
    }
}

function Invoke-HookSample {
    param(
        [Parameter(Mandatory = $true)][string]$ResolvedPythonExecutable,
        [Parameter(Mandatory = $true)][string]$HookScriptPath,
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string]$PayloadJson,
        [Parameter(Mandatory = $true)][ValidateSet("cold", "warmup", "warm")][string]$Phase,
        [Parameter(Mandatory = $true)][int]$Index,
        [Parameter(Mandatory = $true)][int]$TimeoutMilliseconds,
        [Parameter(Mandatory = $true)][bool]$GoSubprocessProxyEligible
    )

    $processConfiguration = New-HookProcessStartInfo `
        -ResolvedPythonExecutable $ResolvedPythonExecutable `
        -HookScriptPath $HookScriptPath `
        -WorkingDirectory $WorkingDirectory
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $processConfiguration.StartInfo
    $stopwatch = [Diagnostics.Stopwatch]::new()
    $timedOut = $false

    try {
        $stopwatch.Start()
        if (-not $process.Start()) {
            throw "Hook 进程未能启动。"
        }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $process.StandardInput.Write($PayloadJson)
        $process.StandardInput.Close()

        if (-not $process.WaitForExit($TimeoutMilliseconds)) {
            $timedOut = $true
            try {
                $process.Kill($true)
            }
            catch {
                $process.Kill()
            }
            $process.WaitForExit()
        }
        $stopwatch.Stop()

        $stdout = $stdoutTask.GetAwaiter().GetResult()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        $exitCode = $process.ExitCode
        $utf8 = [Text.UTF8Encoding]::new($false)

        return [pscustomobject][ordered]@{
            phase                              = $Phase
            index                              = $Index
            elapsedMilliseconds                = [Math]::Round($stopwatch.Elapsed.TotalMilliseconds, 3)
            exitCode                           = $exitCode
            timedOut                           = $timedOut
            stdoutNonEmpty                     = -not [string]::IsNullOrWhiteSpace($stdout)
            stderrNonEmpty                     = -not [string]::IsNullOrWhiteSpace($stderr)
            stdoutByteCount                    = $utf8.GetByteCount($stdout)
            stderrByteCount                    = $utf8.GetByteCount($stderr)
            goSubprocessProxyEligible          = $GoSubprocessProxyEligible
            sensitiveEnvironmentVariablesRemoved = $processConfiguration.SensitiveEnvironmentRemovedCount
        }
    }
    finally {
        if ($stopwatch.IsRunning) {
            $stopwatch.Stop()
        }
        $process.Dispose()
    }
}

function Write-SampleRecord {
    param(
        [Parameter(Mandatory = $true)][string]$SamplesPath,
        [Parameter(Mandatory = $true)]$Sample
    )

    $json = $Sample | ConvertTo-Json -Depth 4 -Compress
    [IO.File]::AppendAllText($SamplesPath, "$json`n", [Text.UTF8Encoding]::new($false))
}

function Invoke-HookSeries {
    param(
        [Parameter(Mandatory = $true)][string]$ResolvedPythonExecutable,
        [Parameter(Mandatory = $true)][string]$HookScriptPath,
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string]$PayloadJson,
        [Parameter(Mandatory = $true)][ValidateSet("cold", "warmup", "warm")][string]$Phase,
        [Parameter(Mandatory = $true)][ValidateRange(0, 100000)][int]$Count,
        [Parameter(Mandatory = $true)][int]$TimeoutMilliseconds,
        [Parameter(Mandatory = $true)][bool]$GoSubprocessProxyEligible,
        [Parameter(Mandatory = $true)][string]$SamplesPath
    )

    $results = [System.Collections.Generic.List[object]]::new()
    for ($index = 1; $index -le $Count; $index++) {
        $sample = Invoke-HookSample `
            -ResolvedPythonExecutable $ResolvedPythonExecutable `
            -HookScriptPath $HookScriptPath `
            -WorkingDirectory $WorkingDirectory `
            -PayloadJson $PayloadJson `
            -Phase $Phase `
            -Index $index `
            -TimeoutMilliseconds $TimeoutMilliseconds `
            -GoSubprocessProxyEligible $GoSubprocessProxyEligible
        Write-SampleRecord -SamplesPath $SamplesPath -Sample $sample
        $results.Add($sample)
    }
    return $results.ToArray()
}

function Get-Percentile {
    param(
        [Parameter(Mandatory = $true)][double[]]$Values,
        [Parameter(Mandatory = $true)][ValidateRange(0, 100)][double]$Percentile
    )

    if ($Values.Count -eq 0) {
        throw "百分位统计至少需要一个样本。"
    }
    $sorted = [double[]]$Values.Clone()
    [Array]::Sort($sorted)
    $rank = [Math]::Ceiling(($Percentile / 100.0) * $sorted.Count)
    $index = [Math]::Max(0, [int]$rank - 1)
    return $sorted[$index]
}

function Get-SeriesSummary {
    param([Parameter(Mandatory = $true)][object[]]$Samples)

    if ($Samples.Count -eq 0) {
        return [pscustomobject][ordered]@{
            sampleCount                    = 0
            p50Milliseconds                = $null
            p95Milliseconds                = $null
            stdoutNonEmptyCount            = 0
            stderrNonEmptyCount            = 0
            nonZeroExitCount               = 0
            timeoutCount                   = 0
            goSubprocessProxyEligibleCount = 0
        }
    }

    $durations = [double[]]@($Samples | ForEach-Object { [double]$_.elapsedMilliseconds })
    return [pscustomobject][ordered]@{
        sampleCount                    = $Samples.Count
        p50Milliseconds                = [Math]::Round((Get-Percentile -Values $durations -Percentile 50), 3)
        p95Milliseconds                = [Math]::Round((Get-Percentile -Values $durations -Percentile 95), 3)
        stdoutNonEmptyCount            = @($Samples | Where-Object { $_.stdoutNonEmpty }).Count
        stderrNonEmptyCount            = @($Samples | Where-Object { $_.stderrNonEmpty }).Count
        nonZeroExitCount               = @($Samples | Where-Object { $_.exitCode -ne 0 }).Count
        timeoutCount                   = @($Samples | Where-Object { $_.timedOut }).Count
        goSubprocessProxyEligibleCount = @($Samples | Where-Object { $_.goSubprocessProxyEligible }).Count
    }
}

function Get-BenchmarkExitCode {
    param(
        [Parameter(Mandatory = $true)][ValidateSet("off", "dry-run", "live")][string]$ControlMode,
        [Parameter(Mandatory = $true)][double]$WarmP95Milliseconds,
        [Parameter(Mandatory = $true)][double]$ThresholdMilliseconds,
        [Parameter(Mandatory = $true)][int]$SampleFailureCount
    )

    if ($SampleFailureCount -gt 0) {
        return 2
    }
    if ($ControlMode -eq "live" -and $WarmP95Milliseconds -gt $ThresholdMilliseconds) {
        return 3
    }
    return 0
}

function Invoke-HookLatencyBenchmark {
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][ValidateSet("codex", "claude")][string]$AdapterName,
        [Parameter(Mandatory = $true)][ValidateSet("off", "dry-run", "live")][string]$ControlMode,
        [Parameter(Mandatory = $true)][ValidateSet("low-friction-read", "governed-code-exec")][string]$InputClass,
        [Parameter(Mandatory = $true)][int]$RecordedWarmSamples,
        [Parameter(Mandatory = $true)][int]$UnrecordedWarmupSamples,
        [Parameter(Mandatory = $true)][int]$TimeoutMilliseconds,
        [Parameter(Mandatory = $true)][double]$ThresholdMilliseconds,
        [Parameter(Mandatory = $true)][double]$PerformanceTargetMilliseconds,
        [Parameter(Mandatory = $true)][string]$RequestedPythonExecutable,
        [Parameter(Mandatory = $true)][AllowEmptyString()][string]$RequestedAgentToolGateExecutable,
        [Parameter(Mandatory = $true)][AllowEmptyString()][string]$RequestedEndpoint
    )

    if ($RecordedWarmSamples -lt 100) {
        throw "warm 样本数不得少于 100。"
    }

    $resolvedRepositoryRoot = [IO.Path]::GetFullPath($RepositoryRoot)
    $resolvedPython = Resolve-ExecutablePath -Value $RequestedPythonExecutable -Purpose "Python 可执行文件"
    $hookScript = Resolve-HookScriptPath -RepositoryRoot $resolvedRepositoryRoot -AdapterName $AdapterName
    $fixture = New-HookLatencyFixture `
        -RepositoryRoot $resolvedRepositoryRoot `
        -AdapterName $AdapterName `
        -ControlMode $ControlMode

    $runtime = $null
    if ($ControlMode -eq "live") {
        $runtime = Resolve-AgentToolGateRuntime `
            -RepositoryRoot $resolvedRepositoryRoot `
            -RequestedExecutable $RequestedAgentToolGateExecutable `
            -RequestedEndpoint $RequestedEndpoint
        Write-HookControl `
            -ControlPath $fixture.ControlPath `
            -ControlMode $ControlMode `
            -RuntimeEndpoint $runtime.Endpoint `
            -RuntimeExecutable $runtime.Executable
    }
    else {
        Write-HookControl -ControlPath $fixture.ControlPath -ControlMode $ControlMode
    }

    $payload = New-HookInputJson `
        -AdapterName $AdapterName `
        -FixtureRepository $fixture.FixtureRepository `
        -InputClass $InputClass
    $goProxyEligible = $ControlMode -eq "live" -and $InputClass -eq "governed-code-exec"

    $coldSamples = @(
        Invoke-HookSeries `
            -ResolvedPythonExecutable $resolvedPython `
            -HookScriptPath $hookScript `
            -WorkingDirectory $fixture.FixtureRepository `
            -PayloadJson $payload `
            -Phase "cold" `
            -Count 1 `
            -TimeoutMilliseconds $TimeoutMilliseconds `
            -GoSubprocessProxyEligible $goProxyEligible `
            -SamplesPath $fixture.SamplesPath
    )
    $warmupResults = @(
        Invoke-HookSeries `
            -ResolvedPythonExecutable $resolvedPython `
            -HookScriptPath $hookScript `
            -WorkingDirectory $fixture.FixtureRepository `
            -PayloadJson $payload `
            -Phase "warmup" `
            -Count $UnrecordedWarmupSamples `
            -TimeoutMilliseconds $TimeoutMilliseconds `
            -GoSubprocessProxyEligible $goProxyEligible `
            -SamplesPath $fixture.SamplesPath
    )
    $warmResults = @(
        Invoke-HookSeries `
            -ResolvedPythonExecutable $resolvedPython `
            -HookScriptPath $hookScript `
            -WorkingDirectory $fixture.FixtureRepository `
            -PayloadJson $payload `
            -Phase "warm" `
            -Count $RecordedWarmSamples `
            -TimeoutMilliseconds $TimeoutMilliseconds `
            -GoSubprocessProxyEligible $goProxyEligible `
            -SamplesPath $fixture.SamplesPath
    )

    $coldSummary = Get-SeriesSummary -Samples $coldSamples
    $warmSummary = Get-SeriesSummary -Samples $warmResults
    $allSamples = @($coldSamples) + @($warmupResults) + @($warmResults)
    $sampleFailureCount = @(
        $allSamples | Where-Object { $_.timedOut -or $_.exitCode -ne 0 }
    ).Count
    $exitCode = Get-BenchmarkExitCode `
        -ControlMode $ControlMode `
        -WarmP95Milliseconds $warmSummary.p95Milliseconds `
        -ThresholdMilliseconds $ThresholdMilliseconds `
        -SampleFailureCount $sampleFailureCount
    $removedEnvironmentCount = if ($allSamples.Count -gt 0) {
        ($allSamples | Measure-Object -Property sensitiveEnvironmentVariablesRemoved -Maximum).Maximum
    }
    else {
        0
    }

    $summary = [ordered]@{
        schemaVersion = 1
        runId         = $fixture.RunId
        generatedAt   = [DateTime]::UtcNow.ToString("o")
        adapter       = $AdapterName
        mode          = $ControlMode
        platform      = [ordered]@{
            os               = [Runtime.InteropServices.RuntimeInformation]::OSDescription
            architecture     = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
            powershellVersion = $PSVersionTable.PSVersion.ToString()
        }
        samplePlan    = [ordered]@{
            coldSamples             = 1
            warmupSamples           = $UnrecordedWarmupSamples
            warmSamples             = $RecordedWarmSamples
            hookProcessesPerSample  = 1
            payloadClass            = $InputClass
        }
        thresholds    = [ordered]@{
            liveP95MaximumMilliseconds = $ThresholdMilliseconds
            targetMilliseconds         = $PerformanceTargetMilliseconds
            liveThresholdMet           = $ControlMode -ne "live" -or $warmSummary.p95Milliseconds -le $ThresholdMilliseconds
            targetMet                  = $warmSummary.p95Milliseconds -lt $PerformanceTargetMilliseconds
        }
        cold          = $coldSummary
        warm          = $warmSummary
        diagnostics   = [ordered]@{
            totalHookProcessInvocations      = $allSamples.Count
            sampleFailureCount               = $sampleFailureCount
            stdoutNonEmptyCount              = @($allSamples | Where-Object { $_.stdoutNonEmpty }).Count
            stderrNonEmptyCount              = @($allSamples | Where-Object { $_.stderrNonEmpty }).Count
            sensitiveEnvironmentRemovedCount = [int]$removedEnvironmentCount
            rawStdoutOrStderrPersisted        = $false
            goSubprocessProxy                 = [ordered]@{
                name                 = "governed-path-sample-count"
                eligibleSampleCount  = @($allSamples | Where-Object { $_.goSubprocessProxyEligible }).Count
                interpretation       = if ($InputClass -eq "governed-code-exec") {
                    "governed code execution should launch one Go bridge process per live Hook sample"
                }
                else {
                    "low-friction workspace reads should remain on the local live fast path"
                }
            }
            runtimeSource = if ($null -eq $runtime) {
                $null
            }
            else {
                [ordered]@{
                    executable = $runtime.ExecutableSource
                    endpoint   = $runtime.EndpointSource
                }
            }
        }
        artifacts     = [ordered]@{
            root        = $fixture.RunDirectory
            samplesJsonl = $fixture.SamplesPath
            summaryJson = $fixture.SummaryPath
        }
        exitCode      = $exitCode
    }

    $summaryJson = $summary | ConvertTo-Json -Depth 12
    [IO.File]::WriteAllText($fixture.SummaryPath, "$summaryJson`n", [Text.UTF8Encoding]::new($false))

    return [pscustomobject]@{
        ExitCode    = $exitCode
        SummaryJson = $summaryJson
        Summary     = [pscustomobject]$summary
    }
}

if ($MyInvocation.InvocationName -ne ".") {
    try {
        $repoRoot = Split-Path -Parent $PSScriptRoot
        $result = Invoke-HookLatencyBenchmark `
            -RepositoryRoot $repoRoot `
            -AdapterName $Adapter `
            -ControlMode $Mode `
            -InputClass $PayloadClass `
            -RecordedWarmSamples $WarmSamples `
            -UnrecordedWarmupSamples $WarmupSamples `
            -TimeoutMilliseconds $SampleTimeoutMilliseconds `
            -ThresholdMilliseconds $P95ThresholdMilliseconds `
            -PerformanceTargetMilliseconds $TargetMilliseconds `
            -RequestedPythonExecutable $PythonExecutable `
            -RequestedAgentToolGateExecutable $AgentToolGateExecutable `
            -RequestedEndpoint $Endpoint
        [Console]::Out.WriteLine($result.SummaryJson)
        exit $result.ExitCode
    }
    catch {
        [Console]::Error.WriteLine($_.Exception.Message)
        exit 1
    }
}
