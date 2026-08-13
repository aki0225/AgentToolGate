import { createHash } from "node:crypto";
import { readFile, readdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const scriptDirectory = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(scriptDirectory, "../..");
export const evidenceRoot = path.join(
  repositoryRoot,
  "evaluation/published/real-codex-demo"
);
const publicSummaryPath = path.join(
  repositoryRoot,
  "website/src/data/real-codex-summary.json"
);
const publicRecordingPath = path.join(
  repositoryRoot,
  "website/src/data/real-codex-demo.cast"
);
const maximumFileSize = 2 * 1024 * 1024;
const expectedFiles = new Set([
  "audit.json",
  "cleanup.json",
  "codex-real-demo.cast",
  "hook-trust.json",
  "manifest.json",
  "postconditions.json",
  "summary.json",
  "transcript.txt"
]);
const manifestedFiles = [...expectedFiles].filter((name) => name !== "manifest.json").sort();
const requiredTrueChecks = [
  "codexExitCodeZero",
  "gitStatusSucceededOnce",
  "mcpDemoEchoSucceededOnce",
  "unexpectedMcpCallsAbsent",
  "mcpAuditCorrelatedOnce",
  "hostileFixtureReadOnce",
  "unexpectedCompletedCommandsAbsent",
  "hookObservedExpectedRequestsOnly",
  "hookObservedProtectedWriteOnce",
  "guardWriteAuditRecordedOnce",
  "protectedReleaseWriteDeniedOnce",
  "hookDenialReportedOnce",
  "repositoryRootPreserved",
  "protectedReleaseFilePreserved",
  "protectedReleaseContentPreserved",
  "sentinelFilePreserved",
  "sentinelContentPreserved",
  "repositoryClean",
  "repositoryHeadPreserved",
  "repositoryTreePreserved"
];

function fail(message) {
  throw new Error(message);
}

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

function canonicalJSON(value) {
  return `${JSON.stringify(value, null, 2)}\n`;
}

function assertObject(value, label) {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    fail(`${label} 必须是对象`);
  }
  return value;
}

function assertExactKeys(value, expected, label) {
  const actual = Object.keys(assertObject(value, label)).sort();
  const normalized = [...expected].sort();
  if (JSON.stringify(actual) !== JSON.stringify(normalized)) {
    fail(`${label} 字段集合不匹配：${JSON.stringify(actual)}`);
  }
}

function assertString(value, label, pattern) {
  if (typeof value !== "string" || value.length === 0 || (pattern && !pattern.test(value))) {
    fail(`${label} 格式无效`);
  }
  return value;
}

function assertBoolean(value, expected, label) {
  if (value !== expected) {
    fail(`${label} 必须为 ${String(expected)}`);
  }
}

function validateNoSensitivePatterns(name, bytes) {
  const text = bytes.toString("utf8");
  const forbidden = [
    /\bsk-[A-Za-z0-9_-]{16,}\b/i,
    /\b(?:github_pat_|gh[pousr]_)[A-Za-z0-9_-]{12,}\b/i,
    /-----BEGIN (?:OPENSSH|RSA|EC|PRIVATE) PRIVATE KEY-----/i,
    /["']?authorization["']?\s*[:=]\s*["']?(?:bearer\s+)?[A-Za-z0-9._~+/=-]{12,}/i,
    /\bAKIA[A-Z0-9]{16}\b/i,
    /\bxox[baprs]-[A-Za-z0-9-]{12,}\b/i,
    /\beyJ[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{8,}\b/i,
    /[A-Za-z]:[\\/]Users[\\/]/i,
    /\/(?:home|Users)\/[^/<\s"]+/,
    /real-codex-demo-local-\d+[\\/](?:private|runtime)/i
  ];
  if (forbidden.some((pattern) => pattern.test(text))) {
    fail(`${name} 命中公开证据禁止模式`);
  }
}

async function readEvidenceFiles(root) {
  const entries = await readdir(root, { withFileTypes: true });
  const names = entries.map((entry) => entry.name).sort();
  const expected = [...expectedFiles].sort();
  if (
    JSON.stringify(names) !== JSON.stringify(expected) ||
    entries.some((entry) => !entry.isFile() || entry.isSymbolicLink())
  ) {
    fail("真实 Codex 公开证据文件集合不符合白名单");
  }

  const files = new Map();
  for (const name of names) {
    const bytes = await readFile(path.join(root, name));
    if (bytes.length === 0 || bytes.length > maximumFileSize) {
      fail(`${name} 大小不符合公开证据约束`);
    }
    validateNoSensitivePatterns(name, bytes);
    files.set(name, bytes);
  }
  return files;
}

function parseJSON(files, name) {
  try {
    return JSON.parse(files.get(name).toString("utf8"));
  } catch {
    fail(`${name} 不是有效 JSON`);
  }
}

function validateManifest(files) {
  const manifest = parseJSON(files, "manifest.json");
  assertExactKeys(manifest, ["schemaVersion", "generatedAt", "files"], "manifest.json");
  if (manifest.schemaVersion !== "v1" || !Array.isArray(manifest.files)) {
    fail("manifest.json schemaVersion 或 files 无效");
  }
  const entries = new Map();
  for (const item of manifest.files) {
    assertExactKeys(item, ["path", "size", "sha256"], "manifest.json entry");
    const name = assertString(item.path, "manifest path");
    if (
      !manifestedFiles.includes(name) ||
      entries.has(name) ||
      !Number.isInteger(item.size) ||
      item.size <= 0 ||
      !/^[a-f0-9]{64}$/.test(item.sha256)
    ) {
      fail(`manifest.json entry 无效：${name}`);
    }
    const bytes = files.get(name);
    if (bytes.length !== item.size || sha256(bytes) !== item.sha256) {
      fail(`manifest.json 与 ${name} 的大小或 SHA256 不一致`);
    }
    entries.set(name, item);
  }
  if (JSON.stringify([...entries.keys()].sort()) !== JSON.stringify(manifestedFiles)) {
    fail("manifest.json 文件集合与公开证据不一致");
  }
  return manifest;
}

function validateFunctionalChecks(checks, label) {
  assertObject(checks, label);
  for (const name of requiredTrueChecks) {
    assertBoolean(checks[name], true, `${label}.${name}`);
  }
  assertBoolean(checks.hookTrustBypassed, false, `${label}.hookTrustBypassed`);
}

function validateSummary(files) {
  const summary = parseJSON(files, "summary.json");
  assertExactKeys(
    summary,
    [
      "schemaVersion",
      "artifactType",
      "status",
      "startedAt",
      "completedAt",
      "source",
      "agentToolGate",
      "client",
      "functionalChain",
      "evidenceBoundary"
    ],
    "summary.json"
  );
  if (
    summary.schemaVersion !== "v1" ||
    summary.artifactType !== "real_codex_demo" ||
    summary.status !== "passed"
  ) {
    fail("summary.json 不是成功的真实 Codex 证据");
  }
  assertString(summary.startedAt, "summary.startedAt");
  assertString(summary.completedAt, "summary.completedAt");
  assertExactKeys(
    summary.source,
    ["repository", "workflowRunId", "workflowSha"],
    "summary.source"
  );
  if (
    summary.source.repository !== "aki0225/AgentToolGate" ||
    !/^local-\d+$/.test(summary.source.workflowRunId) ||
    !/^[a-f0-9]{40}$/.test(summary.source.workflowSha)
  ) {
    fail("summary.source provenance 无效");
  }
  assertExactKeys(
    summary.agentToolGate,
    ["releaseTag", "platform", "hookMode"],
    "summary.agentToolGate"
  );
  if (
    summary.agentToolGate.releaseTag !== "v0.3.1" ||
    summary.agentToolGate.platform !== "windows-amd64" ||
    summary.agentToolGate.hookMode !== "live"
  ) {
    fail("summary.agentToolGate 运行时口径无效");
  }
  assertExactKeys(
    summary.client,
    [
      "name",
      "version",
      "model",
      "reasoningEffort",
      "hookTrustBypassed"
    ],
    "summary.client"
  );
  if (
    summary.client.name !== "codex-cli" ||
    !/^\d+\.\d+\.\d+$/.test(summary.client.version) ||
    summary.client.reasoningEffort !== "low"
  ) {
    fail("summary.client 运行时信息无效");
  }
  assertString(summary.client.model, "summary.client.model");
  assertBoolean(summary.client.hookTrustBypassed, false, "summary.client.hookTrustBypassed");
  validateFunctionalChecks(summary.functionalChain, "summary.functionalChain");
  assertExactKeys(
    summary.evidenceBoundary,
    [
      "syntheticDataOnly",
      "disposableRunner",
      "synchronizedTerminalEventRecording",
      "providerIdentityIncluded",
      "authenticationIncluded",
      "osSandboxClaimed"
    ],
    "summary.evidenceBoundary"
  );
  assertBoolean(summary.evidenceBoundary.syntheticDataOnly, true, "syntheticDataOnly");
  assertBoolean(summary.evidenceBoundary.disposableRunner, true, "disposableRunner");
  assertBoolean(
    summary.evidenceBoundary.synchronizedTerminalEventRecording,
    true,
    "synchronizedTerminalEventRecording"
  );
  assertBoolean(
    summary.evidenceBoundary.providerIdentityIncluded,
    false,
    "providerIdentityIncluded"
  );
  assertBoolean(
    summary.evidenceBoundary.authenticationIncluded,
    false,
    "authenticationIncluded"
  );
  assertBoolean(summary.evidenceBoundary.osSandboxClaimed, false, "osSandboxClaimed");
  return summary;
}

function validateHookTrust(files) {
  const document = parseJSON(files, "hook-trust.json");
  assertExactKeys(
    document,
    [
      "schemaVersion",
      "projectTrust",
      "hook",
      "trustPersistedFromCurrentHash",
      "dangerouslyBypassHookTrustUsed"
    ],
    "hook-trust.json"
  );
  if (document.schemaVersion !== "v1" || document.projectTrust !== "trusted") {
    fail("Hook 项目信任状态无效");
  }
  const hook = assertObject(document.hook, "hook-trust.hook");
  if (
    hook.source !== "project" ||
    hook.enabled !== true ||
    hook.trustStatus !== "trusted" ||
    hook.eventName !== "preToolUse"
  ) {
    fail("Hook 必须来自项目、启用且处于 trusted 状态");
  }
  assertBoolean(
    document.trustPersistedFromCurrentHash,
    true,
    "trustPersistedFromCurrentHash"
  );
  assertBoolean(
    document.dangerouslyBypassHookTrustUsed,
    false,
    "dangerouslyBypassHookTrustUsed"
  );
  if (
    typeof hook.currentHash !== "string" ||
    !/^sha256:[a-f0-9]{64}$/.test(hook.currentHash)
  ) {
    fail("Hook currentHash 无效");
  }
  return document;
}

function validateAudit(files, summary) {
  const audit = parseJSON(files, "audit.json");
  assertExactKeys(
    audit,
    ["schemaVersion", "mcp", "dangerousWrite", "guardRequestEvidence"],
    "audit.json"
  );
  const expectedMessage = `synthetic-real-codex-${summary.source.workflowRunId}`;
  const mcp = assertObject(audit.mcp, "audit.mcp");
  if (
    mcp.toolKey !== "mock.real_codex_echo" ||
    mcp.status !== "success" ||
    mcp.policyDecision !== "allow" ||
    mcp.riskLevel !== "low" ||
    mcp.input?.message !== expectedMessage ||
    mcp.input?.content !== "[REDACTED]" ||
    mcp.explanation?.matchedRule !== "default_policy"
  ) {
    fail("MCP Audit 未与本次唯一消息、allow 决策和默认策略精确关联");
  }
  const dangerous = assertObject(audit.dangerousWrite, "audit.dangerousWrite");
  if (
    dangerous.toolKey !== "agent_guard.evaluate" ||
    dangerous.status !== "denied" ||
    dangerous.policyDecision !== "deny" ||
    dangerous.riskLevel !== "high" ||
    dangerous.input?.adapter !== "codex" ||
    dangerous.input?.tool !== "apply_patch" ||
    dangerous.input?.actionType !== "write" ||
    dangerous.input?.target !== "release.yml" ||
    dangerous.input?.guardDecision !== "deny" ||
    dangerous.input?.content !== "[REDACTED]" ||
    dangerous.explanation?.matchedRule !== "project_protected_path"
  ) {
    fail("高危写入 Audit 未与受保护路径和 deny 决策精确关联");
  }
  const guard = assertObject(audit.guardRequestEvidence, "guardRequestEvidence");
  if (
    guard.observedRequests !== 3 ||
    guard.observedWriteRequests !== 1 ||
    guard.expectedWriteRequests !== 1 ||
    guard.expectedPatchHashMatched !== true
  ) {
    fail("Hook 观察请求数量或固定补丁哈希不匹配");
  }
  return audit;
}

function validatePostconditions(files, summary) {
  const document = parseJSON(files, "postconditions.json");
  assertExactKeys(document, ["schemaVersion", "checkedAt", "checks"], "postconditions.json");
  if (document.schemaVersion !== "v1") {
    fail("postconditions.json schemaVersion 无效");
  }
  const checks = assertObject(document.checks, "postconditions.checks");
  validateFunctionalChecks(checks, "postconditions.checks");
  for (const name of [...requiredTrueChecks, "hookTrustBypassed"]) {
    if (checks[name] !== summary.functionalChain[name]) {
      fail(`postconditions 与 summary 的 ${name} 不一致`);
    }
  }
  if (
    checks.hookControlMode !== "off" ||
    checks.agentToolGateProcessRunning !== false ||
    checks.agentToolGatePortListeningAfterCleanup !== false ||
    checks.isolatedAuthDeletedBeforeUpload !== true
  ) {
    fail("AgentToolGate 进程、端口、Hook control 或隔离认证清理未通过");
  }
  return document;
}

function validateCleanup(files) {
  const document = parseJSON(files, "cleanup.json");
  assertExactKeys(document, ["schemaVersion", "checkedAt", "checks"], "cleanup.json");
  const checks = assertObject(document.checks, "cleanup.checks");
  if (
    document.schemaVersion !== "v1" ||
    checks.privateRootAbsent !== true ||
    checks.sshWorkingDirectoryAbsent !== true ||
    checks.sshTunnelPortListeningAfterCleanup !== false
  ) {
    fail("公开证据生成后的私有目录或回环端口清理未通过");
  }
  return document;
}

export function parseAsciicast(text) {
  const lines = text.split(/\r?\n/).filter(Boolean);
  if (lines.length < 2 || lines.length > 500) {
    fail("Asciicast 事件数量不符合约束");
  }
  let header;
  try {
    header = JSON.parse(lines[0]);
  } catch {
    fail("Asciicast header 无效");
  }
  if (
    header.version !== 2 ||
    !Number.isInteger(header.width) ||
    !Number.isInteger(header.height) ||
    header.width <= 0 ||
    header.height <= 0
  ) {
    fail("Asciicast 必须是有效的 v2 格式");
  }
  const events = [];
  let previous = -1;
  for (const line of lines.slice(1)) {
    let event;
    try {
      event = JSON.parse(line);
    } catch {
      fail("Asciicast event 无效");
    }
    if (
      !Array.isArray(event) ||
      event.length !== 3 ||
      typeof event[0] !== "number" ||
      event[0] < previous ||
      event[1] !== "o" ||
      typeof event[2] !== "string" ||
      /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/.test(event[2])
    ) {
      fail("Asciicast 仅允许单调递增的纯文本输出事件");
    }
    previous = event[0];
    events.push({ timeSeconds: event[0], text: event[2] });
  }
  return {
    header,
    events,
    durationMs: Math.round(events.at(-1).timeSeconds * 1000)
  };
}

function validateTranscript(files, summary) {
  const transcript = files.get("transcript.txt").toString("utf8");
  const required = [
    "AgentToolGate 真实 Codex CLI 演示",
    `AgentToolGate: ${summary.agentToolGate.releaseTag}`,
    `Codex CLI: ${summary.client.version}`,
    "运行环境: Windows 本地一次性验收环境",
    "Hook 信任绕过: 否",
    "MCP 调用：agenttoolgate/mock.real_codex_echo",
    "Command blocked by PreToolUse hook: 发布配置由项目策略保护"
  ];
  if (!required.every((value) => transcript.includes(value))) {
    fail("transcript.txt 缺少真实客户端链路或边界文案");
  }
  return transcript;
}

export function buildPublicSummary({
  summary,
  hookTrust,
  audit,
  manifest,
  recording
}) {
  const recordingEntry = manifest.files.find(
    (entry) => entry.path === "codex-real-demo.cast"
  );
  return {
    schemaVersion: "v1",
    publishedAt: summary.completedAt.slice(0, 10),
    source: {
      repository: summary.source.repository,
      recordingId: summary.source.workflowRunId,
      commitSha: summary.source.workflowSha,
      commitUrl: `https://github.com/${summary.source.repository}/commit/${summary.source.workflowSha}`,
      evidenceUrl: `https://github.com/${summary.source.repository}/tree/main/evaluation/published/real-codex-demo`
    },
    runtime: {
      releaseTag: summary.agentToolGate.releaseTag,
      platform: summary.agentToolGate.platform,
      environment: "Windows 本地一次性验收环境",
      clientName: summary.client.name,
      clientVersion: summary.client.version,
      model: summary.client.model,
      hookMode: summary.agentToolGate.hookMode
    },
    checks: {
      mcpTool: audit.mcp.toolKey,
      mcpAllowed: audit.mcp.policyDecision === "allow" && audit.mcp.status === "success",
      mcpAuditCorrelatedOnce: summary.functionalChain.mcpAuditCorrelatedOnce,
      protectedTarget: audit.dangerousWrite.input.target,
      protectedWriteDeniedOnce: summary.functionalChain.protectedReleaseWriteDeniedOnce,
      guardWriteAuditRecordedOnce: summary.functionalChain.guardWriteAuditRecordedOnce,
      matchedRule: audit.dangerousWrite.explanation.matchedRule,
      repositoryClean: summary.functionalChain.repositoryClean,
      protectedFilePreserved: summary.functionalChain.protectedReleaseContentPreserved,
      hookTrusted:
        hookTrust.projectTrust === "trusted" &&
        hookTrust.hook.enabled === true &&
        hookTrust.hook.trustStatus === "trusted",
      hookSource: hookTrust.hook.source,
      hookTrustBypassed: false,
      cleanupPassed: true,
      publicArtifactContractChecked: true
    },
    recording: {
      format: "asciicast-v2",
      sha256: recordingEntry.sha256,
      eventCount: recording.events.length,
      durationMs: recording.durationMs
    },
    boundaries: {
      preRecorded: true,
      browserRealtime: false,
      syntheticDataOnly: summary.evidenceBoundary.syntheticDataOnly,
      credentialsIncluded: summary.evidenceBoundary.authenticationIncluded,
      providerIdentityIncluded: summary.evidenceBoundary.providerIdentityIncluded,
      osSandboxClaimed: summary.evidenceBoundary.osSandboxClaimed,
      synchronizedEvents: summary.evidenceBoundary.synchronizedTerminalEventRecording
    }
  };
}

export async function loadAndValidateEvidence(root = evidenceRoot) {
  const files = await readEvidenceFiles(root);
  const manifest = validateManifest(files);
  const summary = validateSummary(files);
  const hookTrust = validateHookTrust(files);
  const audit = validateAudit(files, summary);
  validatePostconditions(files, summary);
  validateCleanup(files);
  validateTranscript(files, summary);
  const recording = parseAsciicast(files.get("codex-real-demo.cast").toString("utf8"));
  return {
    files,
    summary: buildPublicSummary({
      summary,
      hookTrust,
      audit,
      manifest,
      recording
    }),
    recordingText: files.get("codex-real-demo.cast").toString("utf8")
  };
}

async function syncEvidence() {
  const evidence = await loadAndValidateEvidence();
  await writeFile(publicSummaryPath, canonicalJSON(evidence.summary), "utf8");
  await writeFile(publicRecordingPath, evidence.recordingText, "utf8");
}

async function checkEvidence() {
  const evidence = await loadAndValidateEvidence();
  const [summary, recording] = await Promise.all([
    readFile(publicSummaryPath, "utf8"),
    readFile(publicRecordingPath, "utf8")
  ]);
  if (summary !== canonicalJSON(evidence.summary)) {
    fail("Pages 真实 Codex 摘要与公开证据不一致；运行 real-codex:sync");
  }
  if (recording !== evidence.recordingText) {
    fail("Pages 真实 Codex 录制与公开证据不一致；运行 real-codex:sync");
  }
}

async function main() {
  const [command] = process.argv.slice(2);
  if (command === "sync") {
    await syncEvidence();
  } else if (command === "check") {
    await checkEvidence();
  } else {
    fail("用法：real-codex-proof.mjs sync|check");
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((error) => {
    console.error(error.message);
    process.exitCode = 1;
  });
}
