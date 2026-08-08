import { describe, expect, it } from "vitest";

import { evaluationProof, getEvaluationSummary } from "./proof";

describe("公开评估数据", () => {
  it("从逐 case 状态派生三组汇总", () => {
    expect(evaluationProof.evaluations.map((item) => item.id)).toEqual([
      "quick-linux",
      "full-windows",
      "full-linux"
    ]);
    for (const evaluation of evaluationProof.evaluations) {
      expect(
        evaluation.passedCount + evaluation.failedCount + evaluation.skippedCount
      ).toBe(evaluation.caseCount);
      expect(evaluation.failedCount).toBe(0);
    }
  });

  it("保留 Linux 平台不适用语义", () => {
    const summary = getEvaluationSummary("full-linux");

    expect(summary.skippedCount).toBeGreaterThan(0);
  });
});
