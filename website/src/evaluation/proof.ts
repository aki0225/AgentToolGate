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
  publishedAt: string;
  run: {
    id: number;
    url: string;
    headSha: string;
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
