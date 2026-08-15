[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$TagName,
    [Parameter(Mandatory = $true)][string]$ExpectedCommitSha,
    [string]$RepositoryRoot = (Split-Path -Parent $PSScriptRoot),
    [string]$GitHubOutputPath
)

$ErrorActionPreference = "Stop"
$SemVerPattern = '^v(?<major>0|[1-9][0-9]*)\.(?<minor>0|[1-9][0-9]*)\.(?<patch>0|[1-9][0-9]*)(?<prerelease>-(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?(?<metadata>\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$'
$SemVerRegex = [regex]::new(
    $SemVerPattern,
    [Text.RegularExpressions.RegexOptions]::CultureInvariant
)

$NormalizedTag = $TagName.Trim()
$NormalizedExpectedCommit = $ExpectedCommitSha.Trim().ToLowerInvariant()
$ResolvedRepositoryRoot = [IO.Path]::GetFullPath($RepositoryRoot)

if (-not [string]::Equals($TagName, $NormalizedTag, [StringComparison]::Ordinal)) {
    throw "Release tag 不允许包含首尾空白。"
}
$SemVerMatch = $SemVerRegex.Match($NormalizedTag)
if (-not $SemVerMatch.Success) {
    throw "Release tag 必须是带 v 前缀的严格 SemVer：$NormalizedTag"
}
if ($NormalizedExpectedCommit -notmatch '^[0-9a-f]{40}$') {
    throw "ExpectedCommitSha 必须是完整的 40 位 Git commit SHA。"
}
if (-not (Test-Path -LiteralPath $ResolvedRepositoryRoot -PathType Container)) {
    throw "RepositoryRoot 不存在：$ResolvedRepositoryRoot"
}

$TagCommit = & git -C $ResolvedRepositoryRoot rev-parse --verify "refs/tags/$NormalizedTag^{commit}" 2>$null
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace(($TagCommit -join ""))) {
    throw "Release tag 不存在或不能解析为 commit：$NormalizedTag"
}
$NormalizedTagCommit = ($TagCommit -join "").Trim().ToLowerInvariant()

$CheckoutCommit = & git -C $ResolvedRepositoryRoot rev-parse --verify "HEAD^{commit}" 2>$null
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace(($CheckoutCommit -join ""))) {
    throw "当前检出内容不能解析为 commit。"
}
$NormalizedCheckoutCommit = ($CheckoutCommit -join "").Trim().ToLowerInvariant()

if ($NormalizedTagCommit -ne $NormalizedExpectedCommit) {
    throw "Release tag 指向 $NormalizedTagCommit，但预期提交是 $NormalizedExpectedCommit。"
}
if ($NormalizedCheckoutCommit -ne $NormalizedExpectedCommit) {
    throw "当前打包提交是 $NormalizedCheckoutCommit，但预期提交是 $NormalizedExpectedCommit。"
}

$IsPrerelease = $SemVerMatch.Groups["prerelease"].Success
if (-not [string]::IsNullOrWhiteSpace($GitHubOutputPath)) {
    $ResolvedOutputPath = [IO.Path]::GetFullPath($GitHubOutputPath)
    $OutputDirectory = Split-Path -Parent $ResolvedOutputPath
    if (-not (Test-Path -LiteralPath $OutputDirectory -PathType Container)) {
        throw "GitHubOutputPath 的父目录不存在：$OutputDirectory"
    }

    Add-Content -LiteralPath $ResolvedOutputPath -Value "commit_sha=$NormalizedExpectedCommit" -Encoding utf8
    Add-Content -LiteralPath $ResolvedOutputPath -Value "is_prerelease=$($IsPrerelease.ToString().ToLowerInvariant())" -Encoding utf8
}

Write-Host "Release tag validation passed: tag=$NormalizedTag commit=$NormalizedExpectedCommit prerelease=$IsPrerelease"
