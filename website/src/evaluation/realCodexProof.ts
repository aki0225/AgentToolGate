import scenarioData from "../data/real-codex-scenarios.json";
import destructiveDeleteRecordingText from "../data/real-codex-destructive-delete.cast?raw";
import lowFrictionRecordingText from "../data/real-codex-low-friction.cast?raw";
import networkEgressRecordingText from "../data/real-codex-network-egress.cast?raw";
import protectedWriteRecordingText from "../data/real-codex-protected-write.cast?raw";
import sensitiveReadRecordingText from "../data/real-codex-sensitive-read.cast?raw";

export const realCodexScenarioIds = [
  "low-friction",
  "sensitive-read",
  "destructive-delete",
  "network-egress",
  "protected-write"
] as const;

export type RealCodexScenarioId = (typeof realCodexScenarioIds)[number];
export type RealCodexDecision = "allow" | "deny";
export type RealCodexRiskLevel = "low" | "high" | "critical";
export type RealCodexAuditStatus = "correlated" | "not-applicable";

export interface RealCodexScenario {
  id: RealCodexScenarioId;
  sessionId: string;
  recordingFile: string;
  label: string;
  title: string;
  description: string;
  decision: RealCodexDecision;
  riskLevel: RealCodexRiskLevel;
  matchedRule: string;
  guardSignal: string;
  actionType: string;
  target: string;
  outcome: string;
  auditStatus: RealCodexAuditStatus;
  auditSummary: string;
  postconditionSummary: string;
  recording: {
    format: "asciicast-v2";
    sha256: string;
    eventCount: number;
    durationMs: number;
  };
}

export interface RealCodexProofDocument {
  schemaVersion: "v2";
  publishedAt: string;
  source: {
    repository: string;
    recordingId: string;
    commitSha: string;
    commitUrl: string;
    evidenceUrl: string;
  };
  runtime: {
    releaseTag: string;
    platform: string;
    environment: string;
    clientName: "codex-cli";
    clientVersion: string;
    model: string;
    hookMode: "live";
  };
  scenarios: RealCodexScenario[];
  sharedChecks: {
    hookTrusted: true;
    hookSource: "project";
    hookTrustBypassed: false;
    cleanupPassed: true;
    publicArtifactContractChecked: true;
  };
  boundaries: {
    preRecorded: true;
    browserRealtime: false;
    syntheticDataOnly: true;
    credentialsIncluded: false;
    providerIdentityIncluded: false;
    osSandboxClaimed: false;
    synchronizedEvents: true;
    completeDlpClaimed: false;
    codexInteractiveApprovalClaimed: false;
  };
}

export interface RealCodexRecordingEvent {
  timeSeconds: number;
  text: string;
}

interface AsciicastHeader {
  version: 2;
  width: number;
  height: number;
  title: string;
}

export interface RealCodexRecording {
  header: AsciicastHeader;
  events: RealCodexRecordingEvent[];
  durationMs: number;
}

export interface RealCodexScenarioProof extends RealCodexScenario {
  recordingData: RealCodexRecording;
}

type UnknownRecord = Record<string, unknown>;

const rawRecordingByScenarioId = {
  "low-friction": lowFrictionRecordingText,
  "sensitive-read": sensitiveReadRecordingText,
  "destructive-delete": destructiveDeleteRecordingText,
  "network-egress": networkEgressRecordingText,
  "protected-write": protectedWriteRecordingText
} satisfies Record<RealCodexScenarioId, string>;

function invalid(message: string): never {
  throw new Error(`真实 Codex 录制无效：${message}`);
}

function isRecord(value: unknown): value is UnknownRecord {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function requireRecord(value: unknown, path: string): UnknownRecord {
  if (!isRecord(value)) {
    invalid(`${path} 必须是对象`);
  }
  return value;
}

function requireExactKeys(value: UnknownRecord, expected: readonly string[], path: string) {
  const actual = Object.keys(value).sort();
  const wanted = [...expected].sort();
  if (actual.length !== wanted.length || actual.some((key, index) => key !== wanted[index])) {
    invalid(`${path} 字段集合不符合 v2 契约`);
  }
}

function requireString(value: unknown, path: string) {
  if (typeof value !== "string" || value.trim() === "") {
    invalid(`${path} 必须是非空字符串`);
  }
  return value;
}

function requireLiteral<T extends string | boolean>(
  value: unknown,
  expected: T,
  path: string
): T {
  if (value !== expected) {
    invalid(`${path} 必须为 ${String(expected)}`);
  }
  return expected;
}

function requirePositiveInteger(value: unknown, path: string) {
  if (!Number.isSafeInteger(value) || (value as number) <= 0) {
    invalid(`${path} 必须是正整数`);
  }
  return value as number;
}

function parseSource(value: unknown): RealCodexProofDocument["source"] {
  const source = requireRecord(value, "source");
  requireExactKeys(
    source,
    ["repository", "recordingId", "commitSha", "commitUrl", "evidenceUrl"],
    "source"
  );
  const commitSha = requireString(source.commitSha, "source.commitSha");
  if (!/^[0-9a-f]{40}$/i.test(commitSha)) {
    invalid("source.commitSha 必须是完整 Git SHA");
  }
  return {
    repository: requireString(source.repository, "source.repository"),
    recordingId: requireString(source.recordingId, "source.recordingId"),
    commitSha,
    commitUrl: requireString(source.commitUrl, "source.commitUrl"),
    evidenceUrl: requireString(source.evidenceUrl, "source.evidenceUrl")
  };
}

function parseRuntime(value: unknown): RealCodexProofDocument["runtime"] {
  const runtime = requireRecord(value, "runtime");
  requireExactKeys(
    runtime,
    [
      "releaseTag",
      "platform",
      "environment",
      "clientName",
      "clientVersion",
      "model",
      "hookMode"
    ],
    "runtime"
  );
  return {
    releaseTag: requireString(runtime.releaseTag, "runtime.releaseTag"),
    platform: requireString(runtime.platform, "runtime.platform"),
    environment: requireString(runtime.environment, "runtime.environment"),
    clientName: requireLiteral(runtime.clientName, "codex-cli", "runtime.clientName"),
    clientVersion: requireString(runtime.clientVersion, "runtime.clientVersion"),
    model: requireString(runtime.model, "runtime.model"),
    hookMode: requireLiteral(runtime.hookMode, "live", "runtime.hookMode")
  };
}

function parseScenario(value: unknown, index: number): RealCodexScenario {
  const path = `scenarios[${index}]`;
  const scenario = requireRecord(value, path);
  requireExactKeys(
    scenario,
    [
      "id",
      "sessionId",
      "recordingFile",
      "label",
      "title",
      "description",
      "decision",
      "riskLevel",
      "matchedRule",
      "guardSignal",
      "actionType",
      "target",
      "outcome",
      "auditStatus",
      "auditSummary",
      "postconditionSummary",
      "recording"
    ],
    path
  );

  const id = requireString(scenario.id, `${path}.id`);
  if (!realCodexScenarioIds.includes(id as RealCodexScenarioId)) {
    invalid(`${path}.id 不在允许场景中`);
  }

  const decision = requireString(scenario.decision, `${path}.decision`);
  if (decision !== "allow" && decision !== "deny") {
    invalid(`${path}.decision 只允许 allow 或 deny`);
  }
  if (
    (id === "low-friction" && decision !== "allow") ||
    (id !== "low-friction" && decision !== "deny")
  ) {
    invalid(`${path}.decision 与场景安全语义不一致`);
  }

  const riskLevel = requireString(scenario.riskLevel, `${path}.riskLevel`);
  if (!["low", "high", "critical"].includes(riskLevel)) {
    invalid(`${path}.riskLevel 不在允许范围内`);
  }
  if (id === "low-friction" && riskLevel !== "low") {
    invalid(`${path}.riskLevel 必须体现低摩擦场景的低风险语义`);
  }
  if (id === "destructive-delete" && riskLevel !== "critical") {
    invalid(`${path}.riskLevel 必须体现根目录删除的 critical 风险语义`);
  }
  if (id !== "low-friction" && id !== "destructive-delete" && riskLevel !== "high") {
    invalid(`${path}.riskLevel 必须体现高风险拒绝语义`);
  }

  const auditStatus = requireString(scenario.auditStatus, `${path}.auditStatus`);
  if (auditStatus !== "correlated" && auditStatus !== "not-applicable") {
    invalid(`${path}.auditStatus 只允许 correlated 或 not-applicable`);
  }
  if (id !== "low-friction" && auditStatus !== "correlated") {
    invalid(`${path}.auditStatus 与拒绝场景的证据语义不一致`);
  }

  const recording = requireRecord(scenario.recording, `${path}.recording`);
  requireExactKeys(
    recording,
    ["format", "sha256", "eventCount", "durationMs"],
    `${path}.recording`
  );
  const sha256 = requireString(recording.sha256, `${path}.recording.sha256`);
  if (!/^[0-9a-f]{64}$/i.test(sha256)) {
    invalid(`${path}.recording.sha256 必须是 64 位十六进制摘要`);
  }

  return {
    id: id as RealCodexScenarioId,
    sessionId: requireString(scenario.sessionId, `${path}.sessionId`),
    recordingFile: requireString(scenario.recordingFile, `${path}.recordingFile`),
    label: requireString(scenario.label, `${path}.label`),
    title: requireString(scenario.title, `${path}.title`),
    description: requireString(scenario.description, `${path}.description`),
    decision,
    riskLevel: riskLevel as RealCodexRiskLevel,
    matchedRule: requireString(scenario.matchedRule, `${path}.matchedRule`),
    guardSignal: requireString(scenario.guardSignal, `${path}.guardSignal`),
    actionType: requireString(scenario.actionType, `${path}.actionType`),
    target: requireString(scenario.target, `${path}.target`),
    outcome: requireString(scenario.outcome, `${path}.outcome`),
    auditStatus,
    auditSummary: requireString(scenario.auditSummary, `${path}.auditSummary`),
    postconditionSummary: requireString(
      scenario.postconditionSummary,
      `${path}.postconditionSummary`
    ),
    recording: {
      format: requireLiteral(
        recording.format,
        "asciicast-v2",
        `${path}.recording.format`
      ),
      sha256,
      eventCount: requirePositiveInteger(
        recording.eventCount,
        `${path}.recording.eventCount`
      ),
      durationMs: requirePositiveInteger(
        recording.durationMs,
        `${path}.recording.durationMs`
      )
    }
  };
}

function parseSharedChecks(value: unknown): RealCodexProofDocument["sharedChecks"] {
  const checks = requireRecord(value, "sharedChecks");
  requireExactKeys(
    checks,
    [
      "hookTrusted",
      "hookSource",
      "hookTrustBypassed",
      "cleanupPassed",
      "publicArtifactContractChecked"
    ],
    "sharedChecks"
  );
  return {
    hookTrusted: requireLiteral(checks.hookTrusted, true, "sharedChecks.hookTrusted"),
    hookSource: requireLiteral(checks.hookSource, "project", "sharedChecks.hookSource"),
    hookTrustBypassed: requireLiteral(
      checks.hookTrustBypassed,
      false,
      "sharedChecks.hookTrustBypassed"
    ),
    cleanupPassed: requireLiteral(checks.cleanupPassed, true, "sharedChecks.cleanupPassed"),
    publicArtifactContractChecked: requireLiteral(
      checks.publicArtifactContractChecked,
      true,
      "sharedChecks.publicArtifactContractChecked"
    )
  };
}

function parseBoundaries(value: unknown): RealCodexProofDocument["boundaries"] {
  const boundaries = requireRecord(value, "boundaries");
  requireExactKeys(
    boundaries,
    [
      "preRecorded",
      "browserRealtime",
      "syntheticDataOnly",
      "credentialsIncluded",
      "providerIdentityIncluded",
      "osSandboxClaimed",
      "synchronizedEvents",
      "completeDlpClaimed",
      "codexInteractiveApprovalClaimed"
    ],
    "boundaries"
  );
  return {
    preRecorded: requireLiteral(boundaries.preRecorded, true, "boundaries.preRecorded"),
    browserRealtime: requireLiteral(
      boundaries.browserRealtime,
      false,
      "boundaries.browserRealtime"
    ),
    syntheticDataOnly: requireLiteral(
      boundaries.syntheticDataOnly,
      true,
      "boundaries.syntheticDataOnly"
    ),
    credentialsIncluded: requireLiteral(
      boundaries.credentialsIncluded,
      false,
      "boundaries.credentialsIncluded"
    ),
    providerIdentityIncluded: requireLiteral(
      boundaries.providerIdentityIncluded,
      false,
      "boundaries.providerIdentityIncluded"
    ),
    osSandboxClaimed: requireLiteral(
      boundaries.osSandboxClaimed,
      false,
      "boundaries.osSandboxClaimed"
    ),
    synchronizedEvents: requireLiteral(
      boundaries.synchronizedEvents,
      true,
      "boundaries.synchronizedEvents"
    ),
    completeDlpClaimed: requireLiteral(
      boundaries.completeDlpClaimed,
      false,
      "boundaries.completeDlpClaimed"
    ),
    codexInteractiveApprovalClaimed: requireLiteral(
      boundaries.codexInteractiveApprovalClaimed,
      false,
      "boundaries.codexInteractiveApprovalClaimed"
    )
  };
}

export function parseRealCodexProofDocument(value: unknown): RealCodexProofDocument {
  const document = requireRecord(value, "document");
  requireExactKeys(
    document,
    [
      "schemaVersion",
      "publishedAt",
      "source",
      "runtime",
      "scenarios",
      "sharedChecks",
      "boundaries"
    ],
    "document"
  );
  if (!Array.isArray(document.scenarios) || document.scenarios.length !== realCodexScenarioIds.length) {
    invalid(`scenarios 必须恰好包含 ${realCodexScenarioIds.length} 个场景`);
  }

  // 先检查原始 id 集合，再解析单场景安全语义，确保重复或缺失场景得到准确错误。
  const rawScenarioIds = document.scenarios.map((value, index) =>
    requireString(requireRecord(value, `scenarios[${index}]`).id, `scenarios[${index}].id`)
  );
  if (new Set(rawScenarioIds).size !== realCodexScenarioIds.length) {
    invalid("scenarios 存在重复 id");
  }

  const parsedScenarios = document.scenarios.map(parseScenario);
  const scenarioMap = new Map(parsedScenarios.map((scenario) => [scenario.id, scenario]));
  const scenarios = realCodexScenarioIds.map((id) => {
    const scenario = scenarioMap.get(id);
    if (!scenario) {
      invalid(`scenarios 缺少 ${id}`);
    }
    return scenario;
  });
  const sessionIds = new Set(scenarios.map((scenario) => scenario.sessionId));
  const recordingFiles = new Set(scenarios.map((scenario) => scenario.recordingFile));
  if (sessionIds.size !== realCodexScenarioIds.length) {
    invalid("scenarios.sessionId 必须全部唯一");
  }
  if (recordingFiles.size !== realCodexScenarioIds.length) {
    invalid("scenarios.recordingFile 必须全部唯一");
  }
  for (const scenario of scenarios) {
    const expectedFile = `scenario-${scenario.id}.cast`;
    if (scenario.recordingFile !== expectedFile) {
      invalid(`${scenario.id}.recordingFile 必须为 ${expectedFile}`);
    }
  }

  return {
    schemaVersion: requireLiteral(document.schemaVersion, "v2", "schemaVersion"),
    publishedAt: requireString(document.publishedAt, "publishedAt"),
    source: parseSource(document.source),
    runtime: parseRuntime(document.runtime),
    scenarios,
    sharedChecks: parseSharedChecks(document.sharedChecks),
    boundaries: parseBoundaries(document.boundaries)
  };
}

export function parseRealCodexRecording(value: string): RealCodexRecording {
  if (typeof value !== "string" || value.length === 0 || value.length > 512_000) {
    invalid("文件为空或超出大小限制");
  }
  const lines = value.split(/\r?\n/).filter((line) => line.trim() !== "");
  if (lines.length < 2 || lines.length > 1_000) {
    invalid("事件数量超出范围");
  }

  let headerValue: unknown;
  try {
    headerValue = JSON.parse(lines[0]);
  } catch {
    invalid("header 不是有效 JSON");
  }
  const header = requireRecord(headerValue, "asciicast.header");
  const headerKeys = Object.keys(header);
  if (
    !["version", "width", "height", "title"].every((key) => headerKeys.includes(key)) ||
    headerKeys.some((key) => !["version", "width", "height", "title", "timestamp", "env"].includes(key))
  ) {
    invalid("header 字段集合不符合 asciicast v2 契约");
  }
  if (
    header.version !== 2 ||
    !Number.isInteger(header.width) ||
    (header.width as number) <= 0 ||
    (header.width as number) > 500 ||
    !Number.isInteger(header.height) ||
    (header.height as number) <= 0 ||
    (header.height as number) > 200 ||
    typeof header.title !== "string" ||
    header.title.trim() === ""
  ) {
    invalid("header 字段不完整或超出范围");
  }

  const events: RealCodexRecordingEvent[] = [];
  let previousTime = -1;
  for (const line of lines.slice(1)) {
    let event: unknown;
    try {
      event = JSON.parse(line);
    } catch {
      invalid("事件不是有效 JSON");
    }
    if (
      !Array.isArray(event) ||
      event.length !== 3 ||
      typeof event[0] !== "number" ||
      !Number.isFinite(event[0]) ||
      event[0] < 0 ||
      event[0] < previousTime ||
      event[1] !== "o" ||
      typeof event[2] !== "string" ||
      event[2].length === 0 ||
      event[2].length > 8_192 ||
      /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/.test(event[2])
    ) {
      invalid("仅允许时间单调递增的有界纯文本输出事件");
    }
    previousTime = event[0];
    events.push({
      timeSeconds: event[0],
      text: event[2].replace(/\r?\n$/, "")
    });
  }

  return {
    header: {
      version: 2,
      width: header.width as number,
      height: header.height as number,
      title: header.title
    },
    events,
    durationMs: Math.round(events.at(-1)!.timeSeconds * 1000)
  };
}

export function parseRealCodexScenarioRecordings(
  document: RealCodexProofDocument,
  rawRecordings: Record<RealCodexScenarioId, string>
): Record<RealCodexScenarioId, RealCodexRecording> {
  for (const id of realCodexScenarioIds) {
    if (typeof rawRecordings[id] !== "string") {
      invalid(`${id} 缺少录制文件`);
    }
  }
  return Object.fromEntries(
    document.scenarios.map((scenario) => {
      const recording = parseRealCodexRecording(rawRecordings[scenario.id]);
      if (
        scenario.recording.format !== "asciicast-v2" ||
        scenario.recording.eventCount !== recording.events.length ||
        scenario.recording.durationMs !== recording.durationMs
      ) {
        invalid(`${scenario.id} 的派生摘要与录制文件不一致`);
      }
      return [scenario.id, recording];
    })
  ) as Record<RealCodexScenarioId, RealCodexRecording>;
}

export function getRealCodexScenario(
  id: RealCodexScenarioId,
  document: RealCodexProofDocument = realCodexProof
) {
  const scenario = document.scenarios.find((candidate) => candidate.id === id);
  if (!scenario) {
    invalid(`未找到场景 ${id}`);
  }
  return scenario;
}

export const realCodexProof = parseRealCodexProofDocument(scenarioData);
export const realCodexRecordings = parseRealCodexScenarioRecordings(
  realCodexProof,
  rawRecordingByScenarioId
);
export const realCodexScenarios: RealCodexScenarioProof[] = realCodexProof.scenarios.map(
  (scenario) => ({
    ...scenario,
    recordingData: realCodexRecordings[scenario.id]
  })
);
