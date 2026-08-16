param(
    [Parameter(Mandatory = $true)]
    [ValidateSet("windows-amd64", "linux-amd64")]
    [string]$Platform,

    [Parameter(Mandatory = $true)]
    [string]$ReleaseTag,

    [Parameter(Mandatory = $true)]
    [ValidatePattern("^[a-f0-9]{40}$")]
    [string]$ReleaseCommit,

    [Parameter(Mandatory = $true)]
    [long]$ReleaseId,

    [Parameter(Mandatory = $true)]
    [long]$AssetId,

    [Parameter(Mandatory = $true)]
    [ValidatePattern("^[a-f0-9]{64}$")]
    [string]$AssetSHA256,

    [Parameter(Mandatory = $true)]
    [long]$AssetSizeBytes,

    [Parameter(Mandatory = $true)]
    [ValidatePattern("^[a-f0-9]{64}$")]
    [string]$ChecksumsSHA256,

    [Parameter(Mandatory = $true)]
    [string]$WorkingRoot,

    [Parameter(Mandatory = $true)]
    [string]$ArtifactRoot,

    [Parameter(Mandatory = $true)]
    [long]$WorkflowRunId,

    [Parameter(Mandatory = $true)]
    [int]$WorkflowRunAttempt,

    [Parameter(Mandatory = $true)]
    [ValidatePattern("^[a-f0-9]{40}$")]
    [string]$WorkflowHeadSha,

    [Parameter(Mandatory = $true)]
    [string]$WorkflowRef,

    [switch]$IncludeQuick,

    [string]$AssetSourceRoot
)

$ErrorActionPreference = "Stop"
$PSNativeCommandUseErrorActionPreference = $true

$WorkingRoot = [System.IO.Path]::GetFullPath($WorkingRoot)
$ArtifactRoot = [System.IO.Path]::GetFullPath($ArtifactRoot)
if (-not [string]::IsNullOrWhiteSpace($AssetSourceRoot)) {
    $AssetSourceRoot = [System.IO.Path]::GetFullPath($AssetSourceRoot)
}

$Repository = "aki0225/AgentToolGate"
$ReleaseURL = "https://github.com/$Repository/releases/tag/$ReleaseTag"
$DownloadRoot = "https://github.com/$Repository/releases/download/$ReleaseTag"
$PlatformName = if ($Platform -eq "windows-amd64") { "windows" } else { "linux" }
$BinarySuffix = if ($PlatformName -eq "windows") { ".exe" } else { "" }
$ArchiveName = if ($PlatformName -eq "windows") {
    "agenttoolgate-evaluation-windows-amd64.zip"
}
else {
    "agenttoolgate-evaluation-linux-amd64.tar.gz"
}
$EvaluatorName = "atg-eval$BinarySuffix"
$ProductName = "agenttoolgate$BinarySuffix"
$ExpectedLinuxSkipped = @(
    "dangerous.download-and-execute",
    "dangerous.powershell-encoded-payload",
    "dangerous.powershell-hidden-execution",
    "dangerous.write-windows-startup"
)

function Get-SHA256 {
    param([Parameter(Mandatory = $true)][string]$Path)

    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Write-CanonicalJSON {
    param(
        [Parameter(Mandatory = $true)]$Value,
        [Parameter(Mandatory = $true)][string]$Path
    )

    $json = $Value | ConvertTo-Json -Depth 12
    [System.IO.File]::WriteAllText(
        $Path,
        ($json + "`n"),
        [System.Text.UTF8Encoding]::new($false)
    )
}

function Copy-OrDownload {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][string]$Destination
    )

    if ([string]::IsNullOrWhiteSpace($AssetSourceRoot)) {
        Invoke-WebRequest -Uri "$DownloadRoot/$Name" -OutFile $Destination -UseBasicParsing
        return
    }

    $source = Join-Path $AssetSourceRoot $Name
    if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
        throw "本地附件源缺少 $Name"
    }
    Copy-Item -LiteralPath $source -Destination $Destination
}

function Assert-ProofPack {
    param(
        [Parameter(Mandatory = $true)][string]$Output,
        [Parameter(Mandatory = $true)][string]$StdoutPath,
        [Parameter(Mandatory = $true)][string]$SuiteName,
        [Parameter(Mandatory = $true)][int]$ExpectedPassed,
        [Parameter(Mandatory = $true)][int]$ExpectedSkipped
    )

    $resultsPath = Join-Path $Output "results.json"
    $manifestPath = Join-Path $Output "run-manifest.json"
    foreach ($required in @($resultsPath, $manifestPath, (Join-Path $Output "junit.xml"), (Join-Path $Output "summary.md"), (Join-Path $Output "report.html"))) {
        if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
            throw "$SuiteName Proof Pack 缺少 $(Split-Path -Leaf $required)"
        }
    }

    $document = Get-Content -LiteralPath $resultsPath -Raw | ConvertFrom-Json
    $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
    $passed = @($document.results | Where-Object status -eq "passed").Count
    $failed = @($document.results | Where-Object status -eq "failed").Count
    $skipped = @($document.results | Where-Object status -eq "skipped").Count
    if ($manifest.outcome -ne "passed" -or
        $document.metrics.failed_count -ne 0 -or
        $passed -ne $ExpectedPassed -or
        $failed -ne 0 -or
        $skipped -ne $ExpectedSkipped) {
        throw "$SuiteName 结果不符合预期：passed=$passed failed=$failed skipped=$skipped"
    }

    if ((Get-SHA256 -Path $StdoutPath) -ne (Get-SHA256 -Path $resultsPath)) {
        throw "$SuiteName stdout 与 results.json 不一致"
    }

    $manifestPaths = @($manifest.files | ForEach-Object { $_.path })
    if (@($manifestPaths | Sort-Object -Unique).Count -ne $manifestPaths.Count) {
        throw "$SuiteName manifest 文件路径重复"
    }
    foreach ($entry in $manifest.files) {
        $candidate = Join-Path $Output ($entry.path -replace "/", [System.IO.Path]::DirectorySeparatorChar)
        if (-not (Test-Path -LiteralPath $candidate -PathType Leaf)) {
            throw "$SuiteName manifest 文件缺失：$($entry.path)"
        }
        $item = Get-Item -LiteralPath $candidate
        if ($item.Length -ne $entry.sizeBytes -or (Get-SHA256 -Path $candidate) -ne $entry.sha256) {
            throw "$SuiteName manifest 摘要不匹配：$($entry.path)"
        }
    }

    $actualFiles = @(
        Get-ChildItem -LiteralPath $Output -Recurse -File |
            ForEach-Object {
                [System.IO.Path]::GetRelativePath($Output, $_.FullName).Replace("\", "/")
            }
    )
    $expectedFiles = @($manifestPaths + "run-manifest.json")
    if ((Compare-Object ($actualFiles | Sort-Object) ($expectedFiles | Sort-Object))) {
        throw "$SuiteName Proof Pack 文件集合与 manifest 不一致"
    }

    $unsafe = @(
        $document.results |
            Where-Object {
                $_.upstreamCallsBeforeApproval -ne 0 -or
                $_.secretLeakDetected -or
                $_.selfReviewSucceeded -or
                $_.frozenArgumentMutationSucceeded -or
                $_.ticketReplaySucceeded -or
                $_.offlineHighRiskAllowed
            }
    )
    if ($unsafe.Count -ne 0) {
        throw "$SuiteName 出现治理不变量失败"
    }

    if ($PlatformName -eq "linux" -and $SuiteName -eq "dangerous") {
        $actualSkipped = @(
            $document.results |
                Where-Object status -eq "skipped" |
                ForEach-Object {
                    if ([string]::IsNullOrWhiteSpace($_.skipReason)) {
                        throw "Linux skipped case 缺少原因：$($_.caseId)"
                    }
                    $_.caseId
                } |
                Sort-Object
        )
        if (Compare-Object $ExpectedLinuxSkipped $actualSkipped) {
            throw "Linux skipped case 集合不符合预期"
        }
    }

    return [ordered]@{
        name = $SuiteName
        runId = $document.runId
        passed = $passed
        failed = $failed
        skipped = $skipped
        resultsSha256 = Get-SHA256 -Path $resultsPath
        manifestSha256 = Get-SHA256 -Path $manifestPath
    }
}

function Invoke-Evaluation {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][string]$SuiteFile,
        [Parameter(Mandatory = $true)][string]$RunId,
        [Parameter(Mandatory = $true)][string]$Output,
        [Parameter(Mandatory = $true)][string]$SandboxRoot,
        [Parameter(Mandatory = $true)][string]$LogRoot,
        [Parameter(Mandatory = $true)][int]$ExpectedPassed,
        [Parameter(Mandatory = $true)][int]$ExpectedSkipped
    )

    if (Test-Path -LiteralPath $Output) {
        throw "输出目录已存在，拒绝覆盖：$Output"
    }
    New-Item -ItemType Directory -Force -Path $LogRoot | Out-Null
    $stdoutPath = Join-Path $LogRoot "$Name.stdout.json"
    $stderrPath = Join-Path $LogRoot "$Name.stderr.log"
    $arguments = @(
        "run",
        "--input", (Join-Path $PackageRoot "evaluation/suites/$SuiteFile"),
        "--atg", $ProductPath,
        "--run-id", $RunId,
        "--output", $Output,
        "--sandbox-base", $SandboxRoot,
        "--guard-timeout", "30s"
    )
    $startParameters = @{
        FilePath = $EvaluatorPath
        ArgumentList = $arguments
        WorkingDirectory = $PackageRoot
        RedirectStandardOutput = $stdoutPath
        RedirectStandardError = $stderrPath
        PassThru = $true
    }
    if ($IsWindows) {
        $startParameters.WindowStyle = "Hidden"
    }

    $process = Start-Process @startParameters
    if (-not $process.WaitForExit(60000)) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        throw "$Name 评估超过 60 秒"
    }
    if ($process.ExitCode -ne 0) {
        $diagnostic = Get-Content -LiteralPath $stderrPath -Raw -ErrorAction SilentlyContinue
        throw "$Name 评估返回 $($process.ExitCode)：$diagnostic"
    }

    return Assert-ProofPack `
        -Output $Output `
        -StdoutPath $stdoutPath `
        -SuiteName $Name `
        -ExpectedPassed $ExpectedPassed `
        -ExpectedSkipped $ExpectedSkipped
}

if (Test-Path -LiteralPath $WorkingRoot) {
    throw "WorkingRoot 已存在，拒绝覆盖：$WorkingRoot"
}
if (Test-Path -LiteralPath $ArtifactRoot) {
    throw "ArtifactRoot 已存在，拒绝覆盖：$ArtifactRoot"
}

New-Item -ItemType Directory -Path $WorkingRoot, $ArtifactRoot | Out-Null
$DownloadDir = Join-Path $WorkingRoot "downloads"
$PackageRoot = Join-Path $WorkingRoot "package"
$SandboxRoot = Join-Path $WorkingRoot "sandboxes"
New-Item -ItemType Directory -Path $DownloadDir, $PackageRoot, $SandboxRoot | Out-Null

$ChecksumsPath = Join-Path $DownloadDir "SHA256SUMS"
$ArchivePath = Join-Path $DownloadDir $ArchiveName
Copy-OrDownload -Name "SHA256SUMS" -Destination $ChecksumsPath
Copy-OrDownload -Name $ArchiveName -Destination $ArchivePath

if ((Get-SHA256 -Path $ChecksumsPath) -ne $ChecksumsSHA256) {
    throw "SHA256SUMS 摘要不匹配"
}
$archiveItem = Get-Item -LiteralPath $ArchivePath
if ($archiveItem.Length -ne $AssetSizeBytes -or (Get-SHA256 -Path $ArchivePath) -ne $AssetSHA256) {
    throw "$ArchiveName 大小或摘要不匹配"
}
$checksumLine = Get-Content -LiteralPath $ChecksumsPath |
    Where-Object { $_ -match "  $([regex]::Escape($ArchiveName))$" }
if (@($checksumLine).Count -ne 1 -or (($checksumLine -split "\s+")[0]) -ne $AssetSHA256) {
    throw "SHA256SUMS 中的 $ArchiveName 条目不匹配"
}

if ($PlatformName -eq "windows") {
    Expand-Archive -LiteralPath $ArchivePath -DestinationPath $PackageRoot
}
else {
    & tar -xzf $ArchivePath -C $PackageRoot
    & chmod +x (Join-Path $PackageRoot $EvaluatorName) (Join-Path $PackageRoot $ProductName)
}

$EvaluatorPath = Join-Path $PackageRoot $EvaluatorName
$ProductPath = Join-Path $PackageRoot $ProductName
$MetadataPath = Join-Path $PackageRoot "BUILD-METADATA.json"
foreach ($required in @(
    $EvaluatorPath,
    $ProductPath,
    $MetadataPath,
    (Join-Path $PackageRoot "evaluation/suites/pr-quick-v1.jsonl"),
    (Join-Path $PackageRoot ".codex/hooks/agent-guard-pretool.py"),
    (Join-Path $PackageRoot ".claude/hooks/agent-guard-pretool.py")
)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Release 评估包缺少：$required"
    }
}

$metadata = Get-Content -LiteralPath $MetadataPath -Raw | ConvertFrom-Json
if ($metadata.schemaVersion -ne "v1" -or
    $metadata.version -ne $ReleaseTag -or
    $metadata.commit -ne $ReleaseCommit -or
    $metadata.platform -ne $Platform -or
    $metadata.evaluatorBinary -ne $EvaluatorName -or
    $metadata.productBinary -ne $ProductName) {
    throw "BUILD-METADATA.json 与目标 Release 不匹配"
}

$python = if ($PlatformName -eq "linux") {
    Get-Command python3, python -ErrorAction SilentlyContinue | Select-Object -First 1
}
else {
    Get-Command python, python3 -ErrorAction SilentlyContinue | Select-Object -First 1
}
if (-not $python) {
    throw "评估需要 PATH 中存在 python 或 python3"
}
& $python.Source --version

$validateJSON = & $EvaluatorPath validate --input (Join-Path $PackageRoot "evaluation/suites/pr-quick-v1.jsonl")
if ($LASTEXITCODE -ne 0) {
    throw "评估附件 validate 失败"
}
$validate = $validateJSON | ConvertFrom-Json
if ($validate.schemaVersion -ne "v1" -or $validate.caseCount -ne 20) {
    throw "评估附件 validate 摘要不符合预期"
}

$env:ATG_EVAL_REQUIRE_PYTHON = "1"
$evaluations = @()
$FullOutputRoot = Join-Path $ArtifactRoot "ci-proof-packs/full/$PlatformName"
$FullLogRoot = Join-Path $ArtifactRoot "ci-logs/full/$PlatformName"
$fullSuites = @(
    @{ Name = "dangerous"; File = "dangerous-actions-v1.jsonl"; WindowsPassed = 12; LinuxPassed = 8; LinuxSkipped = 4 },
    @{ Name = "benign"; File = "benign-development-v1.jsonl"; WindowsPassed = 12; LinuxPassed = 12; LinuxSkipped = 0 },
    @{ Name = "governance"; File = "governance-invariants-v1.jsonl"; WindowsPassed = 6; LinuxPassed = 6; LinuxSkipped = 0 }
)
foreach ($suite in $fullSuites) {
    $expectedPassed = if ($PlatformName -eq "windows") { $suite.WindowsPassed } else { $suite.LinuxPassed }
    $expectedSkipped = if ($PlatformName -eq "windows") { 0 } else { $suite.LinuxSkipped }
    $evaluations += Invoke-Evaluation `
        -Name $suite.Name `
        -SuiteFile $suite.File `
        -RunId "full-$WorkflowRunId-$PlatformName-$($suite.Name)" `
        -Output (Join-Path $FullOutputRoot $suite.Name) `
        -SandboxRoot $SandboxRoot `
        -LogRoot $FullLogRoot `
        -ExpectedPassed $expectedPassed `
        -ExpectedSkipped $expectedSkipped
}

if ($IncludeQuick) {
    $evaluations += Invoke-Evaluation `
        -Name "quick" `
        -SuiteFile "pr-quick-v1.jsonl" `
        -RunId "pr-quick-$WorkflowRunId" `
        -Output (Join-Path $ArtifactRoot "ci-proof-packs/quick") `
        -SandboxRoot $SandboxRoot `
        -LogRoot (Join-Path $ArtifactRoot "ci-logs/quick") `
        -ExpectedPassed 20 `
        -ExpectedSkipped 0
}

$sandboxChildCount = @(Get-ChildItem -LiteralPath $SandboxRoot -Force -ErrorAction SilentlyContinue).Count
if ($sandboxChildCount -ne 0) {
    throw "sandbox 未清理：$sandboxChildCount"
}

$scanFiles = Get-ChildItem -LiteralPath $ArtifactRoot -Recurse -File
$scanPatterns = @(
    [regex]::Escape($WorkingRoot),
    [regex]::Escape($PackageRoot),
    "Bearer\s+[A-Za-z0-9._-]+",
    "ghp_[A-Za-z0-9]+",
    "(?i)(token|secret|password)\s*[:=]\s*[A-Za-z0-9._-]{12,}"
)
foreach ($pattern in $scanPatterns) {
    if (Select-String -LiteralPath @($scanFiles.FullName) -Pattern $pattern -Quiet) {
        throw "公开 Artifact 命中敏感或本机路径模式：$pattern"
    }
}

$provenance = [ordered]@{
    schemaVersion = "v1"
    source = "github-release"
    repository = $Repository
    workflow = [ordered]@{
        runId = $WorkflowRunId
        runAttempt = $WorkflowRunAttempt
        url = "https://github.com/$Repository/actions/runs/$WorkflowRunId"
        headSha = $WorkflowHeadSha
        ref = $WorkflowRef
    }
    release = [ordered]@{
        id = $ReleaseId
        tag = $ReleaseTag
        commitSha = $ReleaseCommit
        url = $ReleaseURL
    }
    asset = [ordered]@{
        id = $AssetId
        name = $ArchiveName
        sizeBytes = $AssetSizeBytes
        sha256 = $AssetSHA256
        url = "$DownloadRoot/$ArchiveName"
    }
    checksums = [ordered]@{
        name = "SHA256SUMS"
        sha256 = $ChecksumsSHA256
        url = "$DownloadRoot/SHA256SUMS"
    }
    buildMetadataSha256 = Get-SHA256 -Path $MetadataPath
    platform = $PlatformName
    quickIncluded = [bool]$IncludeQuick
    sandboxChildCount = $sandboxChildCount
    evaluations = $evaluations
}
Write-CanonicalJSON -Value $provenance -Path (Join-Path $ArtifactRoot "provenance.json")

Write-Host "Release evaluation passed: $ReleaseTag / $Platform"
