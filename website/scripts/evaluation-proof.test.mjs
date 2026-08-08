import { describe, expect, it } from "vitest";

import {
  aggregateMetrics,
  renderReadmeBlock,
  summarizeEvaluation,
  validateProof
} from "./evaluation-proof.mjs";

function fixture() {
    const cases = [
    {
      id: "dangerous.example",
      suite: "dangerous-actions-v1",
      status: "passed",
      actualDecision: "deny",
      durationMs: 5,
      decisionSilent: false,
      sideEffectObserved: false,
      upstreamCallsBeforeApproval: 0,
      ticketReplaySucceeded: false,
      secretLeakDetected: false
    }
  ];
  return {
    schemaVersion: "v1",
    publishedAt: "2026-08-08",
    run: {
      id: 123,
      url: "https://github.com/aki0225/AgentToolGate/actions/runs/123",
      headSha: "a".repeat(40)
    },
    artifacts: [
      { id: 1, name: "agent-safety-proof-pack-quick-123" },
      { id: 2, name: "agent-safety-proof-pack-full-windows-123" },
      { id: 3, name: "agent-safety-proof-pack-full-linux-123" }
    ],
    evaluations: [
      { id: "quick-linux", kind: "quick", platform: "linux", artifactId: 1, sources: [], cases },
      { id: "full-windows", kind: "full", platform: "windows", artifactId: 2, sources: [], cases },
      { id: "full-linux", kind: "full", platform: "linux", artifactId: 3, sources: [], cases }
    ]
  };
}

describe("公开评估快照", () => {
  it("从逐 case 状态计算汇总", () => {
    const cases = [
      {
        id: "dangerous.one",
        suite: "dangerous-actions-v1",
        status: "passed",
        actualDecision: "deny",
        durationMs: 2,
        decisionSilent: false,
        sideEffectObserved: false,
        upstreamCallsBeforeApproval: 0,
        ticketReplaySucceeded: false,
        secretLeakDetected: false
      },
      {
        id: "benign.one",
        suite: "benign-development-v1",
        status: "passed",
        actualDecision: "allow",
        durationMs: 4,
        decisionSilent: true,
        sideEffectObserved: false,
        upstreamCallsBeforeApproval: 0,
        ticketReplaySucceeded: false,
        secretLeakDetected: false
      }
    ];
    const metrics = aggregateMetrics(cases);
    expect(metrics.dangerousGovernedRate).toBe(1);
    expect(metrics.benignInterruptionRate).toBe(0);
    expect(metrics.decisionLatencyP95Ms).toBeCloseTo(3.9);
    expect(summarizeEvaluation({ id: "full-linux", platform: "linux", cases })).toMatchObject({
      id: "full-linux",
      total: 2,
      passed: 2,
      failed: 0,
      skipped: 0
    });
  });

  it("拒绝缺少理由的 skipped case", () => {
    const proof = fixture();
    proof.evaluations[0].cases[0] = {
      id: "dangerous.example",
      suite: "dangerous-actions-v1",
      status: "skipped"
    };
    expect(() => validateProof(proof)).toThrow("skipped case 语义无效");
  });

  it("README 区块包含来源和边界", () => {
    const proof = fixture();
    proof.evaluations = proof.evaluations.map((evaluation) => ({
      ...evaluation,
      cases: []
    }));
    const block = renderReadmeBlock(proof);
    expect(block).toContain("GitHub Actions run 123");
    expect(block).toContain("公开评估快照");
    expect(block).toContain("不替代真实 Codex / Claude Code 客户端验收");
  });
});
