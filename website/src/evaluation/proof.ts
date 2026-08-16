import summaryData from "../data/evaluation-summary.json";

export interface EvaluationSummary {
  id: string;
  kind: "quick" | "full";
  platform: "windows" | "linux";
  caseCount: number;
  passedCount: number;
  failedCount: number;
  skippedCount: number;
  dangerousGovernedRate: number;
  benignInterruptionRate: number;
  decisionLatencyP95Ms: number;
  approvalPreUpstreamCalls: number;
  secretLeakCount: number;
  ticketReplaySuccessCount: number;
}

interface EvaluationProofDocument {
  schemaVersion: "v2";
  publishedAt: string;
  subject: {
    type: "github-release";
    releaseId: number;
    releaseTag: string;
    commitSha: string;
    releaseUrl: string;
    checksums: {
      name: string;
      sha256: string;
      url: string;
    };
    assets: Array<{
      platform: "windows" | "linux";
      id: number;
      name: string;
      sizeBytes: number;
      sha256: string;
    }>;
  };
  run: {
    id: number;
    attempt: number;
    url: string;
    headSha: string;
    ref: string;
  };
  evaluations: EvaluationSummary[];
}

export const evaluationProof = summaryData as EvaluationProofDocument;

export function getEvaluationSummary(id: string): EvaluationSummary {
  const evaluation = evaluationProof.evaluations.find((item) => item.id === id);
  if (!evaluation) {
    throw new Error(`公开评估快照缺少 ${id}`);
  }
  return evaluation;
}
