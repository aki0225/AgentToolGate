$ErrorActionPreference = "Stop"

$RepoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$ValidateScript = Join-Path $RepoRoot "scripts\validate-release-tag.ps1"
$ReleaseWorkflow = Join-Path $RepoRoot ".github\workflows\release.yml"
$PowerShellExe = (Get-Process -Id $PID).Path
$TmpRoot = Join-Path $RepoRoot ".tmp\validate-release-tag-tests-$PID-$([guid]::NewGuid().ToString('N'))"
$TestRepo = Join-Path $TmpRoot "repo"
$Failures = [System.Collections.Generic.List[string]]::new()

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
        Write-Host "[PASS] $Name" -ForegroundColor Green
    }
    catch {
        $Failures.Add("$Name`: $($_.Exception.Message)")
        Write-Host "[FAIL] $Name`: $($_.Exception.Message)" -ForegroundColor Red
    }
}

function Invoke-Validator {
    param(
        [Parameter(Mandatory = $true)][string]$TagName,
        [Parameter(Mandatory = $true)][string]$ExpectedCommitSha
    )

    $OutputPath = Join-Path $TmpRoot ("github-output-" + [guid]::NewGuid().ToString("N") + ".txt")
    $Output = & $PowerShellExe `
        -NoProfile `
        -ExecutionPolicy Bypass `
        -File $ValidateScript `
        -TagName $TagName `
        -ExpectedCommitSha $ExpectedCommitSha `
        -RepositoryRoot $TestRepo `
        -GitHubOutputPath $OutputPath 2>&1

    $ParsedOutputs = @{}
    if (Test-Path -LiteralPath $OutputPath -PathType Leaf) {
        foreach ($Line in Get-Content -LiteralPath $OutputPath) {
            $Separator = $Line.IndexOf("=")
            if ($Separator -gt 0) {
                $ParsedOutputs[$Line.Substring(0, $Separator)] = $Line.Substring($Separator + 1)
            }
        }
    }

    return @{
        ExitCode = $LASTEXITCODE
        Output   = ($Output -join [Environment]::NewLine)
        Outputs  = $ParsedOutputs
    }
}

try {
    New-Item -ItemType Directory -Force -Path $TestRepo | Out-Null
    git -C $TestRepo init --quiet
    git -C $TestRepo config user.name "AgentToolGate Tests"
    git -C $TestRepo config user.email "tests@agenttoolgate.local"

    "first" | Set-Content -LiteralPath (Join-Path $TestRepo "release.txt") -Encoding UTF8
    git -C $TestRepo add release.txt
    git -C $TestRepo commit --quiet -m "first"
    $FirstCommit = (git -C $TestRepo rev-parse HEAD).Trim()
    git -C $TestRepo tag v1.2.3
    git -C $TestRepo tag -a v1.2.4-rc.1+build.5 -m "annotated release"
    git -C $TestRepo tag v1.2.5+linux-amd64

    "second" | Set-Content -LiteralPath (Join-Path $TestRepo "release.txt") -Encoding UTF8
    git -C $TestRepo add release.txt
    git -C $TestRepo commit --quiet -m "second"
    $SecondCommit = (git -C $TestRepo rev-parse HEAD).Trim()
    git -C $TestRepo remote add origin $TestRepo
    git -C $TestRepo update-ref refs/remotes/origin/main $SecondCommit

    git -C $TestRepo checkout --quiet --detach $FirstCommit
    "feature" | Set-Content -LiteralPath (Join-Path $TestRepo "release.txt") -Encoding UTF8
    git -C $TestRepo add release.txt
    git -C $TestRepo commit --quiet -m "feature outside main"
    $NonMainCommit = (git -C $TestRepo rev-parse HEAD).Trim()
    git -C $TestRepo tag v1.2.6
    git -C $TestRepo checkout --quiet --detach $FirstCommit

    Invoke-Case -Name "accepts a strict SemVer lightweight tag" -Body {
        $Result = Invoke-Validator -TagName "v1.2.3" -ExpectedCommitSha $FirstCommit
        Assert-Equal -Expected 0 -Actual $Result.ExitCode -Message $Result.Output
        Assert-Equal -Expected "false" -Actual $Result.Outputs["is_prerelease"] -Message "stable tag classification"
    }

    Invoke-Case -Name "accepts prerelease and build metadata on an annotated tag" -Body {
        $Result = Invoke-Validator -TagName "v1.2.4-rc.1+build.5" -ExpectedCommitSha $FirstCommit
        Assert-Equal -Expected 0 -Actual $Result.ExitCode -Message $Result.Output
        Assert-Equal -Expected "true" -Actual $Result.Outputs["is_prerelease"] -Message "prerelease tag classification"
    }

    Invoke-Case -Name "accepts build metadata without marking a prerelease" -Body {
        $Result = Invoke-Validator -TagName "v1.2.5+linux-amd64" -ExpectedCommitSha $FirstCommit
        Assert-Equal -Expected 0 -Actual $Result.ExitCode -Message $Result.Output
        Assert-Equal -Expected "false" -Actual $Result.Outputs["is_prerelease"] -Message "build metadata classification"
    }

    Invoke-Case -Name "accepts a commit that is an ancestor of origin/main" -Body {
        $Result = Invoke-Validator -TagName "v1.2.3" -ExpectedCommitSha $FirstCommit
        Assert-Equal -Expected 0 -Actual $Result.ExitCode -Message $Result.Output
    }

    foreach ($InvalidTag in @(
        "1.2.3",
        "v1.2",
        "v01.2.3",
        "v1.02.3",
        "v1.2.03",
        "v1.2.3-01",
        "v1.2.3-",
        "v１.2.3",
        "v1.２.3",
        "v1.2.３",
        "v1.٢.3",
        "v1.२.3"
    )) {
        Invoke-Case -Name "rejects invalid tag $InvalidTag" -Body {
            $Result = Invoke-Validator -TagName $InvalidTag -ExpectedCommitSha $FirstCommit
            Assert-Equal -Expected 1 -Actual $Result.ExitCode -Message $Result.Output
        }
    }

    foreach ($WhitespaceCase in @(
        @{ Name = "leading space"; Tag = " v1.2.3" },
        @{ Name = "trailing space"; Tag = "v1.2.3 " },
        @{ Name = "leading tab"; Tag = "`tv1.2.3" },
        @{ Name = "trailing tab"; Tag = "v1.2.3`t" }
    )) {
        Invoke-Case -Name "rejects tag with $($WhitespaceCase.Name)" -Body {
            $Result = Invoke-Validator -TagName $WhitespaceCase.Tag -ExpectedCommitSha $FirstCommit
            Assert-Equal -Expected 1 -Actual $Result.ExitCode -Message $Result.Output
        }
    }

    Invoke-Case -Name "rejects a missing tag" -Body {
        $Result = Invoke-Validator -TagName "v9.9.9" -ExpectedCommitSha $FirstCommit
        Assert-Equal -Expected 1 -Actual $Result.ExitCode -Message $Result.Output
    }

    Invoke-Case -Name "rejects a checkout that differs from the validated tag commit" -Body {
        git -C $TestRepo checkout --quiet --detach $SecondCommit
        try {
            $Result = Invoke-Validator -TagName "v1.2.3" -ExpectedCommitSha $FirstCommit
            Assert-Equal -Expected 1 -Actual $Result.ExitCode -Message $Result.Output
        }
        finally {
            git -C $TestRepo checkout --quiet --detach $FirstCommit
        }
    }

    Invoke-Case -Name "rejects a tag that points to another commit" -Body {
        $Result = Invoke-Validator -TagName "v1.2.3" -ExpectedCommitSha $SecondCommit
        Assert-Equal -Expected 1 -Actual $Result.ExitCode -Message $Result.Output
    }

    Invoke-Case -Name "rejects a valid tag on a commit outside origin/main" -Body {
        git -C $TestRepo checkout --quiet --detach $NonMainCommit
        try {
            $Result = Invoke-Validator -TagName "v1.2.6" -ExpectedCommitSha $NonMainCommit
            Assert-Equal -Expected 1 -Actual $Result.ExitCode -Message $Result.Output
            Assert-True `
                -Condition $Result.Output.Contains("origin/main") `
                -Message "非 origin/main 祖先提交必须由祖先门禁拒绝。"
        }
        finally {
            git -C $TestRepo checkout --quiet --detach $FirstCommit
        }
    }

    Invoke-Case -Name "release workflow scans all releases instead of tag lookup" -Body {
        $Workflow = Get-Content -Raw -LiteralPath $ReleaseWorkflow
        Assert-True `
            -Condition (-not $Workflow.Contains("/releases/tags/")) `
            -Message "Release workflow 不得退回 releases/tags 单项查询，draft 可能被漏掉。"
        Assert-True `
            -Condition ([regex]::Matches(
                $Workflow,
                [regex]::Escape('releases?per_page=100&page=$page')
            ).Count -ge 2) `
            -Message "validate 与 release job 都必须分页扫描 releases 列表。"
        Assert-True `
            -Condition ([regex]::Matches(
                $Workflow,
                [regex]::Escape('[StringComparison]::Ordinal')
            ).Count -ge 2) `
            -Message "Release tag_name 必须按精确大小写匹配。"
        Assert-True `
            -Condition $Workflow.Contains("Assert-OnlyOwnedRelease") `
            -Message "创建后必须按 returned release id 验证本轮所有权。"
        Assert-True `
            -Condition $Workflow.Contains('-Uri "$repositoryUri/releases/$ExpectedReleaseId"') `
            -Message "本轮 Release 必须按 ID 直接查询，不能依赖列表接口立即可见。"
        Assert-True `
            -Condition (-not $Workflow.Contains('$owned.Count -ne 1')) `
            -Message "Release 所有权校验不得要求新草稿立即出现在分页列表中。"
        Assert-True `
            -Condition (
                $Workflow.Contains('$currentRelease.draft -and') -and
                $Workflow.Contains('[string]$currentRelease.tag_name')
            ) `
            -Message "失败清理只能删除本轮仍为 draft 且 tag 一致的 Release。"
        Assert-True `
            -Condition (-not $Workflow.Contains("softprops/action-gh-release")) `
            -Message "Release workflow 不得重新引入会复用既有 Release 的 softprops action。"
        Assert-True `
            -Condition $Workflow.Contains('-Uri "${uploadUri}?name=$encodedName"') `
            -Message "Release 附件上传 URI 必须使用显式变量边界，避免 PowerShell 把查询参数解析为变量名。"
        Assert-True `
            -Condition (-not $Workflow.Contains('-Uri "$uploadUri?name=$encodedName"')) `
            -Message "Release 附件上传 URI 不得使用会被 PowerShell 误解析的变量写法。"

        $ValidateJob = [regex]::Match(
            $Workflow,
            '(?ms)^  validate-release:\r?\n(?<body>.*?)(?=^  [A-Za-z0-9_-]+:\r?$|\z)'
        ).Groups["body"].Value
        $ReleaseJob = [regex]::Match(
            $Workflow,
            '(?ms)^  release:\r?\n(?<body>.*?)(?=\z)'
        ).Groups["body"].Value
        $ValidatePermissions = [regex]::Match(
            $ValidateJob,
            '(?ms)^    permissions:\r?\n(?<body>(?:^      [A-Za-z0-9_-]+: [^\r\n]+\r?\n?)+)'
        )
        $ValidatePermissionLines = @(
            $ValidatePermissions.Groups["body"].Value -split '\r?\n' |
                Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
        )
        Assert-True `
            -Condition $ValidatePermissions.Success `
            -Message "validate-release job 必须显式声明权限。"
        Assert-Equal `
            -Expected 2 `
            -Actual $ValidatePermissionLines.Count `
            -Message "validate-release job 只能声明两项只读权限。"
        Assert-True `
            -Condition (
                $ValidatePermissionLines -contains "      actions: read" -and
                $ValidatePermissionLines -contains "      contents: read"
            ) `
            -Message "validate-release job 只能声明 actions: read 与 contents: read。"
        Assert-True `
            -Condition (-not ($ValidateJob -match '(?m)^      [A-Za-z0-9_-]+: write\r?$')) `
            -Message "validate-release job 不得拥有写权限。"
        Assert-True `
            -Condition ($ReleaseJob -match '(?m)^    permissions:\r?\n      actions: read\r?\n      contents: write\r?$') `
            -Message "最终 release job 必须保留发布所需的写权限。"
        Assert-Equal `
            -Expected 1 `
            -Actual ([regex]::Matches(
                $Workflow,
                '(?m)^      [A-Za-z0-9_-]+: write\r?$'
            ).Count) `
            -Message "只有最终 release job 可以声明写权限。"
        Assert-True `
            -Condition (
                $ValidateJob.Contains("/actions/workflows/ci.yml/runs?head_sha=") -and
                $ValidateJob.Contains("status=completed") -and
                $ValidateJob.Contains('[string]$run.head_sha') -and
                $ValidateJob.Contains('[string]$run.status') -and
                $ValidateJob.Contains('[string]$run.conclusion') -and
                $ValidateJob.Contains('"success"')
            ) `
            -Message "validate-release job 必须按精确 SHA 检查 ci.yml 的 completed/success run。"
        Assert-True `
            -Condition ($Workflow -match '(?m)^  workflow_dispatch:\r?$') `
            -Message "Release workflow 必须继续支持 workflow_dispatch。"
        Assert-True `
            -Condition ($Workflow -match '(?ms)^  push:\r?\n    tags:\r?\n      - "v\*"\r?$') `
            -Message "Release workflow 必须继续支持 v* tag push。"
    }

    if ($Failures.Count -gt 0) {
        throw ($Failures -join [Environment]::NewLine)
    }

    Write-Host "Release tag validation regression tests passed." -ForegroundColor Green
}
finally {
    $TmpBase = [IO.Path]::GetFullPath((Join-Path $RepoRoot ".tmp")) + [IO.Path]::DirectorySeparatorChar
    $ResolvedTmpRoot = [IO.Path]::GetFullPath($TmpRoot)
    if ($ResolvedTmpRoot.StartsWith($TmpBase, [StringComparison]::OrdinalIgnoreCase)) {
        Remove-Item -LiteralPath $ResolvedTmpRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}
