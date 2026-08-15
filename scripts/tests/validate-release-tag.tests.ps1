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
            -Condition (
                $Workflow.Contains('$currentRelease.draft -and') -and
                $Workflow.Contains('[string]$currentRelease.tag_name')
            ) `
            -Message "失败清理只能删除本轮仍为 draft 且 tag 一致的 Release。"
        Assert-True `
            -Condition (-not $Workflow.Contains("softprops/action-gh-release")) `
            -Message "Release workflow 不得重新引入会复用既有 Release 的 softprops action。"
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
