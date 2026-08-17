$ErrorActionPreference = "Stop"

$RepoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$ResolverScript = Join-Path $RepoRoot "scripts\resolve-release-evidence.ps1"
$CIWorkflow = Join-Path $RepoRoot ".github\workflows\ci.yml"
$PowerShellExe = (Get-Process -Id $PID).Path
$TmpRoot = Join-Path $RepoRoot ".tmp\resolve-release-evidence-tests-$PID-$([guid]::NewGuid().ToString("N"))"
$Failures = [System.Collections.Generic.List[string]]::new()
$ReleaseCommit = "a" * 40

function Assert-Equal {
    param(
        [Parameter(Mandatory = $true)]$Expected,
        [Parameter(Mandatory = $true)]$Actual,
        [Parameter(Mandatory = $true)][string]$Message
    )

    if ($Expected -ne $Actual) {
        throw "$Message 预期=$Expected 实际=$Actual"
    }
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

function Invoke-Case {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][scriptblock]$Body
    )

    try {
        & $Body
        Write-Host "[通过] $Name" -ForegroundColor Green
    }
    catch {
        $Failures.Add("$Name`: $($_.Exception.Message)")
        Write-Host "[失败] $Name`: $($_.Exception.Message)" -ForegroundColor Red
    }
}

function New-Asset {
    param(
        [Parameter(Mandatory = $true)][long]$Id,
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][long]$Size,
        [Parameter(Mandatory = $true)][char]$DigestCharacter
    )

    return [ordered]@{
        id = $Id
        name = $Name
        size = $Size
        state = "uploaded"
        digest = "sha256:$([string]$DigestCharacter * 64)"
    }
}

function New-ReleaseFixture {
    return [ordered]@{
        id = 987654321
        tag_name = "v9.8.7"
        draft = $false
        prerelease = $false
        assets = @(
            (New-Asset -Id 101 -Name "agenttoolgate-windows-amd64.zip" -Size 1001 -DigestCharacter "1"),
            (New-Asset -Id 102 -Name "agenttoolgate-linux-amd64.tar.gz" -Size 1002 -DigestCharacter "2"),
            (New-Asset -Id 103 -Name "agenttoolgate-evaluation-windows-amd64.zip" -Size 1003 -DigestCharacter "3"),
            (New-Asset -Id 104 -Name "agenttoolgate-evaluation-linux-amd64.tar.gz" -Size 1004 -DigestCharacter "4"),
            (New-Asset -Id 105 -Name "SHA256SUMS" -Size 1005 -DigestCharacter "5")
        )
    }
}

function Write-JSON {
    param(
        [Parameter(Mandatory = $true)]$Value,
        [Parameter(Mandatory = $true)][string]$Path
    )

    [IO.File]::WriteAllText(
        $Path,
        (($Value | ConvertTo-Json -Depth 8) + "`n"),
        [Text.UTF8Encoding]::new($false)
    )
}

function Invoke-Resolver {
    param(
        [Parameter(Mandatory = $true)]$Release,
        [string]$Tag = "v9.8.7"
    )

    $caseId = [guid]::NewGuid().ToString("N")
    $releasePath = Join-Path $TmpRoot "$caseId-release.json"
    $outputPath = Join-Path $TmpRoot "$caseId-github-output.txt"
    Write-JSON -Value $Release -Path $releasePath
    [IO.File]::WriteAllText($outputPath, "", [Text.UTF8Encoding]::new($false))

    $output = & $PowerShellExe `
        -NoProfile `
        -ExecutionPolicy Bypass `
        -File $ResolverScript `
        -ReleaseTag $Tag `
        -ReleaseCommit $ReleaseCommit `
        -ReleaseJsonPath $releasePath `
        -GitHubOutputPath $outputPath 2>&1
    $exitCode = $LASTEXITCODE
    $parsedOutputs = @{}
    foreach ($line in Get-Content -LiteralPath $outputPath) {
        $separator = $line.IndexOf("=")
        if ($separator -gt 0) {
            $parsedOutputs[$line.Substring(0, $separator)] = $line.Substring($separator + 1)
        }
    }

    return @{
        ExitCode = $exitCode
        Output = ($output -join [Environment]::NewLine)
        Outputs = $parsedOutputs
    }
}

try {
    New-Item -ItemType Directory -Force -Path $TmpRoot | Out-Null

    Invoke-Case -Name "接受完整稳定版 Release 并输出动态矩阵" -Body {
        $result = Invoke-Resolver -Release (New-ReleaseFixture)
        Assert-Equal -Expected 0 -Actual $result.ExitCode -Message $result.Output
        Assert-Equal -Expected "v9.8.7" -Actual $result.Outputs["release_tag"] -Message "Release tag 输出"
        Assert-Equal -Expected $ReleaseCommit -Actual $result.Outputs["release_commit"] -Message "Release commit 输出"
        Assert-Equal -Expected "987654321" -Actual $result.Outputs["release_id"] -Message "Release id 输出"
        Assert-Equal -Expected ("5" * 64) -Actual $result.Outputs["checksums_sha256"] -Message "校验和摘要输出"

        $matrix = @($result.Outputs["matrix"] | ConvertFrom-Json)
        Assert-Equal -Expected 2 -Actual $matrix.Count -Message "矩阵平台数量"
        Assert-Equal -Expected "windows-amd64" -Actual $matrix[0].platform -Message "Windows 平台"
        Assert-Equal -Expected 103 -Actual $matrix[0].asset_id -Message "Windows 评估附件"
        Assert-Equal -Expected "false" -Actual $matrix[0].include_quick -Message "Windows 快速评估标记"
        Assert-Equal -Expected "linux-amd64" -Actual $matrix[1].platform -Message "Linux 平台"
        Assert-Equal -Expected 104 -Actual $matrix[1].asset_id -Message "Linux 评估附件"
        Assert-Equal -Expected "true" -Actual $matrix[1].include_quick -Message "Linux 快速评估标记"
    }

    Invoke-Case -Name "读取 Release 元数据前拒绝 tag 首尾空白" -Body {
        $result = Invoke-Resolver -Release (New-ReleaseFixture) -Tag " v9.8.7"
        Assert-Equal -Expected 1 -Actual $result.ExitCode -Message $result.Output
        Assert-True -Condition $result.Output.Contains("首尾空白") -Message "应返回明确的空白错误。"
    }

    Invoke-Case -Name "拒绝预发布 tag 输入" -Body {
        $release = New-ReleaseFixture
        $release.tag_name = "v9.8.7-rc.1"
        $result = Invoke-Resolver -Release $release -Tag "v9.8.7-rc.1"
        Assert-Equal -Expected 1 -Actual $result.ExitCode -Message $result.Output
        Assert-True -Condition $result.Output.Contains("稳定版 SemVer") -Message "应拒绝预发布 tag。"
    }

    Invoke-Case -Name "拒绝 draft Release" -Body {
        $release = New-ReleaseFixture
        $release.draft = $true
        $result = Invoke-Resolver -Release $release
        Assert-Equal -Expected 1 -Actual $result.ExitCode -Message $result.Output
        Assert-True -Condition $result.Output.Contains("draft Release") -Message "应拒绝 draft Release。"
    }

    Invoke-Case -Name "拒绝 prerelease 元数据" -Body {
        $release = New-ReleaseFixture
        $release.prerelease = $true
        $result = Invoke-Resolver -Release $release
        Assert-Equal -Expected 1 -Actual $result.ExitCode -Message $result.Output
        Assert-True -Condition $result.Output.Contains("prerelease") -Message "应拒绝 prerelease。"
    }

    Invoke-Case -Name "拒绝大小写不一致的 Release tag" -Body {
        $release = New-ReleaseFixture
        $release.tag_name = "V9.8.7"
        $result = Invoke-Resolver -Release $release
        Assert-Equal -Expected 1 -Actual $result.ExitCode -Message $result.Output
        Assert-True -Condition $result.Output.Contains("tag_name 与请求不一致") -Message "tag 必须精确匹配。"
    }

    Invoke-Case -Name "拒绝缺少必需主程序包的 Release" -Body {
        $release = New-ReleaseFixture
        $release.assets = @(
            $release.assets |
                Where-Object name -ne "agenttoolgate-linux-amd64.tar.gz"
        )
        $result = Invoke-Resolver -Release $release
        Assert-Equal -Expected 1 -Actual $result.ExitCode -Message $result.Output
        Assert-True -Condition $result.Output.Contains("agenttoolgate-linux-amd64.tar.gz") -Message "应指出缺失附件。"
    }

    Invoke-Case -Name "拒绝重复的必需附件" -Body {
        $release = New-ReleaseFixture
        $release.assets += New-Asset `
            -Id 106 `
            -Name "agenttoolgate-evaluation-windows-amd64.zip" `
            -Size 1006 `
            -DigestCharacter "6"
        $result = Invoke-Resolver -Release $release
        Assert-Equal -Expected 1 -Actual $result.ExitCode -Message $result.Output
        Assert-True -Condition $result.Output.Contains("必须且只能包含一个附件") -Message "应拒绝重复附件。"
    }

    Invoke-Case -Name "拒绝缺少 SHA256 digest 的附件" -Body {
        $release = New-ReleaseFixture
        $release.assets[2].digest = $null
        $result = Invoke-Resolver -Release $release
        Assert-Equal -Expected 1 -Actual $result.ExitCode -Message $result.Output
        Assert-True -Condition $result.Output.Contains("SHA256 digest") -Message "应拒绝缺失摘要。"
    }

    Invoke-Case -Name "拒绝尚未上传完成的附件" -Body {
        $release = New-ReleaseFixture
        $release.assets[1].state = "new"
        $result = Invoke-Resolver -Release $release
        Assert-Equal -Expected 1 -Actual $result.ExitCode -Message $result.Output
        Assert-True -Condition $result.Output.Contains("uploaded 状态") -Message "应拒绝未上传完成的附件。"
    }

    Invoke-Case -Name "拒绝零字节的必需附件" -Body {
        $release = New-ReleaseFixture
        $release.assets[3].size = 0
        $result = Invoke-Resolver -Release $release
        Assert-Equal -Expected 1 -Actual $result.ExitCode -Message $result.Output
        Assert-True -Condition $result.Output.Contains("必须是正整数") -Message "应拒绝零字节附件。"
    }

    Invoke-Case -Name "CI 先解析 Release 元数据再使用动态矩阵" -Body {
        $workflow = Get-Content -LiteralPath $CIWorkflow -Raw
        $resolverJob = [regex]::Match(
            $workflow,
            '(?ms)^  resolve-release-evidence:\r?\n(?<body>.*?)(?=^  [A-Za-z0-9_-]+:\r?$|\z)'
        ).Groups["body"].Value
        $evaluationJob = [regex]::Match(
            $workflow,
            '(?ms)^  release-evaluation:\r?\n(?<body>.*?)(?=^  [A-Za-z0-9_-]+:\r?$|\z)'
        ).Groups["body"].Value

        Assert-True -Condition (-not [string]::IsNullOrWhiteSpace($resolverJob)) -Message "CI 缺少 Release 证据解析 job。"
        Assert-True -Condition (-not [string]::IsNullOrWhiteSpace($evaluationJob)) -Message "CI 缺少 Release Evaluation job。"
        Assert-True -Condition $resolverJob.Contains("fetch-depth: 0") -Message "解析 job 必须获取完整 tag 历史。"
        Assert-True -Condition $resolverJob.Contains("persist-credentials: false") -Message "解析 job 不应持久化 GitHub 凭据。"
        Assert-True -Condition $resolverJob.Contains("scripts/resolve-release-evidence.ps1") -Message "CI 必须复用可测试的解析脚本。"
        Assert-True -Condition $resolverJob.Contains("GITHUB_TOKEN") -Message "解析 job 必须使用只读 GitHub token 获取 Release 元数据。"
        Assert-True -Condition $resolverJob.Contains('releases/tags/$encodedTag') -Message "解析 job 必须按请求 tag 获取正式 Release。"
        Assert-True -Condition $resolverJob.Contains("merge-base --is-ancestor") -Message "解析 job 必须确认 Release commit 位于 origin/main。"
        Assert-True -Condition $evaluationJob.Contains("needs: resolve-release-evidence") -Message "评估 job 必须依赖解析结果。"
        Assert-True `
            -Condition $evaluationJob.Contains("fromJSON(needs.resolve-release-evidence.outputs.matrix)") `
            -Message "评估 job 必须使用解析出的动态矩阵。"
        Assert-True `
            -Condition $evaluationJob.Contains("needs.resolve-release-evidence.outputs.release_tag") `
            -Message "Artifact 名和运行参数必须使用解析后的 tag。"
        Assert-True -Condition (-not $evaluationJob.Contains("v0.4.1")) -Message "评估 job 不得再硬编码 v0.4.1。"
        Assert-True -Condition (-not $evaluationJob.Contains("516783373")) -Message "评估 job 不得硬编码历史 Asset ID。"
        Assert-True -Condition (-not $evaluationJob.Contains("43868521e56c85cf074e92f572daff49121651b9")) -Message "评估 job 不得硬编码历史提交。"
    }
}
finally {
    if (Test-Path -LiteralPath $TmpRoot) {
        Remove-Item -LiteralPath $TmpRoot -Recurse -Force
    }
}

if ($Failures.Count -gt 0) {
    Write-Host ""
    Write-Host "Release 证据解析测试失败：" -ForegroundColor Red
    foreach ($failure in $Failures) {
        Write-Host " - $failure" -ForegroundColor Red
    }
    exit 1
}

Write-Host ""
Write-Host "Release 证据解析测试全部通过。" -ForegroundColor Green
