[CmdletBinding()]
param(
    [string]$BaseUrl = "http://localhost:8080",
    [string]$WorkspaceOrgId = "local-org",
    [string]$TargetPath = "",
    [ValidateSet("plain", "base64")]
    [string]$PayloadMode = "plain",
    [string]$Token = "",
    [switch]$DryRun
)

$ErrorActionPreference = "Stop"

function Get-EffectiveToken {
    param(
        [string]$TokenValue
    )

    if (-not [string]::IsNullOrWhiteSpace($TokenValue)) {
        return $TokenValue
    }
    if (-not [string]::IsNullOrWhiteSpace($env:AGENTTOOLGATE_TOKEN)) {
        return $env:AGENTTOOLGATE_TOKEN
    }
    return ""
}

function Get-DefaultTargetPath {
    $repoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot "..\.."))
    return (Join-Path $repoRoot ".tmp\synthetic\Users\demo\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup\agent-update.ps1")
}

function Get-PayloadContent {
    param(
        [string]$Mode,
        [string]$PlainPayload
    )

    if ($Mode -eq "base64") {
        return [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($PlainPayload))
    }
    return $PlainPayload
}

function Get-PayloadSummary {
    param(
        [string]$Mode,
        [string]$EncodedPayload,
        [string]$PlainPayload
    )

    if ($Mode -eq "base64") {
        $prefixLength = [Math]::Min(24, $EncodedPayload.Length)
        return "base64 prefix=$($EncodedPayload.Substring(0, $prefixLength))...; decoded=$PlainPayload"
    }
    return $PlainPayload
}

function Convert-HeadersForDisplay {
    param(
        [hashtable]$Headers
    )

    $displayHeaders = [ordered]@{}
    foreach ($key in $Headers.Keys) {
        if ($key -eq "Authorization") {
            $displayHeaders[$key] = "Bearer [REDACTED]"
            continue
        }
        $displayHeaders[$key] = $Headers[$key]
    }
    return $displayHeaders
}

function Write-DemoSummary {
    param(
        [string]$BaseUrlValue,
        [string]$TargetPathValue,
        [string]$PayloadModeValue,
        [string]$PayloadSummaryValue
    )

    Write-Host "Base URL: $BaseUrlValue"
    Write-Host "Target Path: $TargetPathValue"
    Write-Host "Payload Mode: $PayloadModeValue"
    Write-Host "Payload Summary: $PayloadSummaryValue"
}

$plainPayload = "powershell -ExecutionPolicy Bypass -WindowStyle Hidden -File payload.ps1"
$contentEncoding = $PayloadMode
$content = Get-PayloadContent -Mode $PayloadMode -PlainPayload $plainPayload
$payloadSummary = Get-PayloadSummary -Mode $PayloadMode -EncodedPayload $content -PlainPayload $plainPayload
$effectiveToken = Get-EffectiveToken -TokenValue $Token
if ([string]::IsNullOrWhiteSpace($TargetPath)) {
    $TargetPath = Get-DefaultTargetPath
}

$body = [ordered]@{
    adapter         = "codex"
    tool            = "Write"
    actionType      = "write"
    target          = $TargetPath
    isScript        = $true
    contentEncoding = $contentEncoding
    content         = $content
}

$headers = [ordered]@{
    Accept               = "application/json"
    "Content-Type"       = "application/json"
    "X-Workspace-Org-Id" = $WorkspaceOrgId
}

if (-not [string]::IsNullOrWhiteSpace($effectiveToken)) {
    $headers["Authorization"] = "Bearer $effectiveToken"
}

$requestJson = $body | ConvertTo-Json -Depth 4
$displayHeadersJson = (Convert-HeadersForDisplay -Headers $headers) | ConvertTo-Json -Depth 3
$baseUrlTrimmed = $BaseUrl.TrimEnd("/")

Write-Host "Synthetic Windows Startup poisoning demo"
Write-Host "This demo only sends JSON to AgentToolGate. It does not write files or execute payloads."
Write-DemoSummary -BaseUrlValue $baseUrlTrimmed -TargetPathValue $TargetPath -PayloadModeValue $PayloadMode -PayloadSummaryValue $payloadSummary

if ($DryRun) {
    Write-Host ""
    Write-Host "[DryRun] Request headers:"
    Write-Host $displayHeadersJson
    Write-Host ""
    Write-Host "[DryRun] Request body:"
    Write-Host $requestJson
    exit 0
}

try {
    Invoke-RestMethod -Method Get -Uri "$baseUrlTrimmed/health" -Headers @{ Accept = "application/json" } | Out-Null
} catch {
    Write-Error "AgentToolGate backend is not reachable at $baseUrlTrimmed. Please start the backend first, or run docker compose up --build."
    exit 2
}

try {
    $response = Invoke-RestMethod -Method Post -Uri "$baseUrlTrimmed/api/agent-guard/evaluate" -Headers $headers -Body $requestJson
} catch {
    $details = $_.ErrorDetails.Message
    if ([string]::IsNullOrWhiteSpace($details)) {
        $details = $_.Exception.Message
    }
    Write-Error "AgentToolGate request failed: $details"
    exit 3
}

Write-Host ""
Write-Host "decision: $($response.decision)"
Write-Host "reason: $($response.reason)"
Write-Host "approvalId: $($response.approvalId)"
Write-Host "fingerprint: $($response.fingerprint)"
Write-Host "targetPath: $TargetPath"
Write-Host "payloadSummary: $payloadSummary"

if ($null -ne $response.explanation) {
    Write-Host "targetCategory: $($response.explanation.targetCategory)"
    Write-Host "riskLevel: $($response.explanation.riskLevel)"
    Write-Host "matchedRule: $($response.explanation.matchedRule)"
    if ($null -ne $response.explanation.signals -and $response.explanation.signals.Count -gt 0) {
        Write-Host "signals:"
        foreach ($signal in $response.explanation.signals) {
            Write-Host "  - $signal"
        }
    }
}

if ($response.decision -notin @("deny_with_ticket", "deny")) {
    Write-Error "Demo failed: expected decision deny_with_ticket or deny, got '$($response.decision)'."
    exit 1
}

Write-Host ""
Write-Host "Demo succeeded: AgentToolGate blocked or gated the synthetic Startup poisoning request."
