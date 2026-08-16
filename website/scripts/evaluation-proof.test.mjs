import { describe, expect, it } from "vitest";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

import {
  aggregateMetrics,
  buildPublicSummary,
  expectedQuickSuites,
  renderReadmeBlock,
  summarizeEvaluation,
  validateProof,
  validateReleaseProof
} from "./evaluation-proof.mjs";

const scriptDirectory = path.dirname(fileURLToPath(import.meta.url));
const quickSuitePath = path.resolve(scriptDirectory, "../../evaluation/suites/pr-quick-v1.jsonl");
const historicalProofPath = path.resolve(
  scriptDirectory,
  "../../evaluation/published/agent-safety-proof.json"
);

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

async function releaseFixture() {
  const historical = JSON.parse(await readFile(historicalProofPath, "utf8"));
  const runId = 123;
  const artifactIds = {
    "quick-linux": 101,
    "full-windows": 102,
    "full-linux": 103
  };
  return {
    schemaVersion: "v2",
    publishedAt: "2026-08-16",
    subject: {
      type: "github-release",
      releaseId: 371316925,
      releaseTag: "v0.4.1",
      commitSha: "43868521e56c85cf074e92f572daff49121651b9",
      releaseUrl: "https://github.com/aki0225/AgentToolGate/releases/tag/v0.4.1",
      checksums: {
        name: "SHA256SUMS",
        sha256: "b203ec978d7da9b4add09c80e41cdef4971be8d590f601131f75012a65763e6e",
        url: "https://github.com/aki0225/AgentToolGate/releases/download/v0.4.1/SHA256SUMS"
      },
      assets: [
        {
          platform: "windows",
          id: 516783373,
          name: "agenttoolgate-evaluation-windows-amd64.zip",
          sizeBytes: 29876035,
          sha256: "cc39b6af9dfde8c9958bdf012d6bfdd9ec7a093b212760557f83e040321da246"
        },
        {
          platform: "linux",
          id: 516783402,
          name: "agenttoolgate-evaluation-linux-amd64.tar.gz",
          sizeBytes: 29129053,
          sha256: "dcd4d2f85a499036cead94611d7209f9166c29ffbb61fd3431fa4e111216bfbc"
        }
      ]
    },
    run: {
      id: runId,
      attempt: 1,
      url: `https://github.com/aki0225/AgentToolGate/actions/runs/${runId}`,
      headSha: "b".repeat(40),
      ref: "refs/heads/codex/v041-evidence-refresh"
    },
    artifacts: [
      {
        id: artifactIds["quick-linux"],
        name: `agent-safety-release-proof-pack-quick-v0.4.1-${runId}`,
        kind: "quick",
        platform: "linux",
        provenanceSha256: "1".repeat(64)
      },
      {
        id: artifactIds["full-windows"],
        name: `agent-safety-release-proof-pack-full-windows-v0.4.1-${runId}`,
        kind: "full",
        platform: "windows",
        provenanceSha256: "2".repeat(64)
      },
      {
        id: artifactIds["full-linux"],
        name: `agent-safety-release-proof-pack-full-linux-v0.4.1-${runId}`,
        kind: "full",
        platform: "linux",
        provenanceSha256: "3".repeat(64)
      }
    ],
    evaluations: historical.evaluations.map((evaluation) => ({
      ...evaluation,
      artifactId: artifactIds[evaluation.id]
    }))
  };
}

describe("公开评估快照", () => {
  it("与仓库当前 quick suite 组成保持一致", async () => {
    const lines = (await readFile(quickSuitePath, "utf8"))
      .split(/\r?\n/)
      .filter((line) => line.trim())
      .map((line) => JSON.parse(line));
    const actual = lines.reduce((counts, item) => {
      counts[item.suite] = (counts[item.suite] ?? 0) + 1;
      return counts;
    }, {});

    expect(actual).toEqual(expectedQuickSuites);
  });

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

  it("Release 证据绑定正式附件与 workflow provenance", async () => {
    const proof = await releaseFixture();

    expect(validateReleaseProof(proof)).toBe(proof);
    expect(buildPublicSummary(proof).subject.releaseTag).toBe("v0.4.1");
    const block = renderReadmeBlock(proof);
    expect(block).toContain("v0.4.1");
    expect(block).toContain("正式评估附件");
    expect(block).toContain("版本化公开证据");
  });

  it("拒绝 Release 附件摘要漂移", async () => {
    const proof = await releaseFixture();
    proof.subject.assets[0].sha256 = "f".repeat(64);

    expect(() => validateReleaseProof(proof)).toThrow("冻结契约");
  });
});
