[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$ReleaseTag,

    [Parameter(Mandatory = $true)]
    [ValidatePattern("^[a-f0-9]{40}$")]
    [string]$ReleaseCommit,

    [Parameter(Mandatory = $true)]
    [string]$ReleaseJsonPath,

    [Parameter(Mandatory = $true)]
    [string]$GitHubOutputPath
)

$ErrorActionPreference = "Stop"

# 正式证据只绑定稳定版 Release；预发布版本应先完成 RC 验收，不能混入稳定版证据入口。
$StableSemVerPattern = '^v(?<major>0|[1-9][0-9]*)\.(?<minor>0|[1-9][0-9]*)\.(?<patch>0|[1-9][0-9]*)(?<metadata>\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$'
$StableSemVerRegex = [regex]::new(
    $StableSemVerPattern,
    [Text.RegularExpressions.RegexOptions]::CultureInvariant
)
$DigestRegex = [regex]::new(
    '^sha256:(?<sha256>[a-f0-9]{64})$',
    [Text.RegularExpressions.RegexOptions]::CultureInvariant -bor
    [Text.RegularExpressions.RegexOptions]::IgnoreCase
)
$RequiredAssetNames = @(
    "agenttoolgate-windows-amd64.zip",
    "agenttoolgate-linux-amd64.tar.gz",
    "agenttoolgate-evaluation-windows-amd64.zip",
    "agenttoolgate-evaluation-linux-amd64.tar.gz",
    "SHA256SUMS"
)

function Get-RequiredProperty {
    param(
        [Parameter(Mandatory = $true)]$Object,
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][string]$Context
    )

    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) {
        throw "$Context 缺少字段：$Name"
    }
    return $property.Value
}

function Get-PositiveInt64 {
    param(
        [Parameter(Mandatory = $true)]$Value,
        [Parameter(Mandatory = $true)][string]$Context
    )

    [long]$parsed = 0
    $parsedSuccessfully = [long]::TryParse(
        [string]$Value,
        [Globalization.NumberStyles]::Integer,
        [Globalization.CultureInfo]::InvariantCulture,
        [ref]$parsed
    )
    if (-not $parsedSuccessfully -or $parsed -le 0) {
        throw "$Context 必须是正整数。"
    }
    return $parsed
}

function Get-Asset {
    param(
        [Parameter(Mandatory = $true)][object[]]$Assets,
        [Parameter(Mandatory = $true)][string]$Name
    )

    $matches = @(
        $Assets |
            Where-Object {
                [string]::Equals(
                    [string]$_.name,
                    $Name,
                    [StringComparison]::Ordinal
                )
            }
    )
    if ($matches.Count -ne 1) {
        throw "正式 Release 必须且只能包含一个附件：$Name"
    }

    $asset = $matches[0]
    $assetId = Get-PositiveInt64 `
        -Value (Get-RequiredProperty -Object $asset -Name "id" -Context "附件 $Name") `
        -Context "附件 $Name 的 id"
    $assetSize = Get-PositiveInt64 `
        -Value (Get-RequiredProperty -Object $asset -Name "size" -Context "附件 $Name") `
        -Context "附件 $Name 的 size"
    $assetState = [string](Get-RequiredProperty -Object $asset -Name "state" -Context "附件 $Name")
    if (-not [string]::Equals($assetState, "uploaded", [StringComparison]::Ordinal)) {
        throw "附件 $Name 尚未处于 uploaded 状态。"
    }
    $digest = [string](Get-RequiredProperty -Object $asset -Name "digest" -Context "附件 $Name")
    $digestMatch = $DigestRegex.Match($digest)
    if (-not $digestMatch.Success) {
        throw "附件 $Name 缺少可验证的 SHA256 digest。"
    }

    return [ordered]@{
        id = $assetId
        size = $assetSize
        sha256 = $digestMatch.Groups["sha256"].Value.ToLowerInvariant()
    }
}

function Write-GitHubOutput {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][string]$Value
    )

    [IO.File]::AppendAllText(
        $ResolvedGitHubOutputPath,
        "$Name=$Value`n",
        [Text.UTF8Encoding]::new($false)
    )
}

$NormalizedTag = $ReleaseTag.Trim()
if (-not [string]::Equals($ReleaseTag, $NormalizedTag, [StringComparison]::Ordinal)) {
    throw "Release tag 不允许包含首尾空白。"
}
if (-not $StableSemVerRegex.IsMatch($NormalizedTag)) {
    throw "正式证据 tag 必须是带 v 前缀的稳定版 SemVer：$NormalizedTag"
}

$ResolvedReleaseJsonPath = [IO.Path]::GetFullPath($ReleaseJsonPath)
$ResolvedGitHubOutputPath = [IO.Path]::GetFullPath($GitHubOutputPath)
if (-not (Test-Path -LiteralPath $ResolvedReleaseJsonPath -PathType Leaf)) {
    throw "ReleaseJsonPath 不存在：$ResolvedReleaseJsonPath"
}
$OutputDirectory = Split-Path -Parent $ResolvedGitHubOutputPath
if (-not (Test-Path -LiteralPath $OutputDirectory -PathType Container)) {
    throw "GitHubOutputPath 的父目录不存在：$OutputDirectory"
}

try {
    $release = Get-Content -LiteralPath $ResolvedReleaseJsonPath -Raw | ConvertFrom-Json
}
catch {
    throw "Release 元数据不是有效 JSON：$($_.Exception.Message)"
}
if ($null -eq $release) {
    throw "Release 元数据为空。"
}

$releaseTagName = [string](Get-RequiredProperty -Object $release -Name "tag_name" -Context "Release")
if (-not [string]::Equals($releaseTagName, $NormalizedTag, [StringComparison]::Ordinal)) {
    throw "Release tag_name 与请求不一致：请求=$NormalizedTag 实际=$releaseTagName"
}

$draft = Get-RequiredProperty -Object $release -Name "draft" -Context "Release"
$prerelease = Get-RequiredProperty -Object $release -Name "prerelease" -Context "Release"
if ($draft -isnot [bool] -or $prerelease -isnot [bool]) {
    throw "Release draft/prerelease 字段必须是布尔值。"
}
if ($draft) {
    throw "正式证据不能绑定 draft Release。"
}
if ($prerelease) {
    throw "正式证据不能绑定 prerelease。"
}

$releaseId = Get-PositiveInt64 `
    -Value (Get-RequiredProperty -Object $release -Name "id" -Context "Release") `
    -Context "Release id"
$assetsProperty = $release.PSObject.Properties["assets"]
if ($null -eq $assetsProperty) {
    throw "Release 缺少字段：assets"
}
$assets = @($assetsProperty.Value | Where-Object { $null -ne $_ })
if ($assets.Count -eq 0) {
    throw "Release 没有附件。"
}

$resolvedAssets = @{}
foreach ($assetName in $RequiredAssetNames) {
    $resolvedAssets[$assetName] = Get-Asset -Assets $assets -Name $assetName
}

$windowsEvaluation = $resolvedAssets["agenttoolgate-evaluation-windows-amd64.zip"]
$linuxEvaluation = $resolvedAssets["agenttoolgate-evaluation-linux-amd64.tar.gz"]
$checksums = $resolvedAssets["SHA256SUMS"]
$matrix = @(
    [ordered]@{
        os = "windows-latest"
        platform = "windows-amd64"
        proof_platform = "windows"
        asset_id = $windowsEvaluation.id
        asset_size = $windowsEvaluation.size
        asset_sha256 = $windowsEvaluation.sha256
        include_quick = "false"
    },
    [ordered]@{
        os = "ubuntu-latest"
        platform = "linux-amd64"
        proof_platform = "linux"
        asset_id = $linuxEvaluation.id
        asset_size = $linuxEvaluation.size
        asset_sha256 = $linuxEvaluation.sha256
        include_quick = "true"
    }
)
$matrixJson = ConvertTo-Json -InputObject $matrix -Depth 4 -Compress

Write-GitHubOutput -Name "release_tag" -Value $NormalizedTag
Write-GitHubOutput -Name "release_commit" -Value $ReleaseCommit
Write-GitHubOutput -Name "release_id" -Value ([string]$releaseId)
Write-GitHubOutput -Name "checksums_sha256" -Value $checksums.sha256
Write-GitHubOutput -Name "matrix" -Value $matrixJson

Write-Host "Release 证据元数据校验通过：tag=$NormalizedTag release_id=$releaseId"
