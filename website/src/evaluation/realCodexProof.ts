import summaryData from "../data/real-codex-summary.json";
import recordingText from "../data/real-codex-demo.cast?raw";

export interface RealCodexProofDocument {
  schemaVersion: "v1";
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
    platform: "windows-amd64";
    environment: string;
    clientName: "codex-cli";
    clientVersion: string;
    model: string;
    hookMode: "live";
  };
  checks: {
    mcpTool: string;
    mcpAllowed: true;
    mcpAuditCorrelatedOnce: true;
    protectedTarget: string;
    protectedWriteDeniedOnce: true;
    guardWriteAuditRecordedOnce: true;
    matchedRule: string;
    repositoryClean: true;
    protectedFilePreserved: true;
    hookTrusted: true;
    hookSource: "project";
    hookTrustBypassed: false;
    cleanupPassed: true;
    publicArtifactContractChecked: true;
  };
  recording: {
    format: "asciicast-v2";
    sha256: string;
    eventCount: number;
    durationMs: number;
  };
  boundaries: {
    preRecorded: true;
    browserRealtime: false;
    syntheticDataOnly: true;
    credentialsIncluded: false;
    providerIdentityIncluded: false;
    osSandboxClaimed: false;
    synchronizedEvents: true;
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

function invalid(message: string): never {
  throw new Error(`真实 Codex 录制无效：${message}`);
}

export function parseRealCodexRecording(value: string): RealCodexRecording {
  const lines = value.split(/\r?\n/).filter(Boolean);
  if (lines.length < 2 || lines.length > 500) {
    invalid("事件数量超出范围");
  }

  let header: unknown;
  try {
    header = JSON.parse(lines[0]);
  } catch {
    invalid("header 不是有效 JSON");
  }
  if (
    !header ||
    typeof header !== "object" ||
    Array.isArray(header) ||
    (header as Record<string, unknown>).version !== 2 ||
    !Number.isInteger((header as Record<string, unknown>).width) ||
    !Number.isInteger((header as Record<string, unknown>).height) ||
    typeof (header as Record<string, unknown>).title !== "string"
  ) {
    invalid("header 字段不完整");
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
      event[0] < previousTime ||
      event[1] !== "o" ||
      typeof event[2] !== "string" ||
      /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/.test(event[2])
    ) {
      invalid("仅允许时间单调递增的纯文本输出事件");
    }
    previousTime = event[0];
    events.push({
      timeSeconds: event[0],
      text: event[2].replace(/\r?\n$/, "")
    });
  }

  return {
    header: header as AsciicastHeader,
    events,
    durationMs: Math.round(events.at(-1)!.timeSeconds * 1000)
  };
}

export const realCodexProof = summaryData as RealCodexProofDocument;
export const realCodexRecording = parseRealCodexRecording(recordingText);

if (
  realCodexProof.schemaVersion !== "v1" ||
  realCodexProof.recording.format !== "asciicast-v2" ||
  realCodexProof.recording.eventCount !== realCodexRecording.events.length ||
  realCodexProof.recording.durationMs !== realCodexRecording.durationMs
) {
  invalid("派生摘要与录制文件不一致");
}
