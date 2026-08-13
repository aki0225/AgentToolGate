import { describe, expect, it } from "vitest";

import {
  getRealCodexScenario,
  parseRealCodexProofDocument,
  parseRealCodexRecording,
  parseRealCodexScenarioRecordings,
  realCodexProof,
  realCodexRecordings,
  realCodexScenarioIds,
  realCodexScenarios
} from "./realCodexProof";

function cloneProof() {
  return structuredClone(realCodexProof);
}

describe("真实 Codex 多场景证据", () => {
  it("严格保留五场景、默认顺序和安全语义", () => {
    expect(realCodexProof.schemaVersion).toBe("v2");
    expect(realCodexProof.scenarios.map((scenario) => scenario.id)).toEqual(
      realCodexScenarioIds
    );
    expect(realCodexProof.scenarios).toHaveLength(5);
    expect(getRealCodexScenario("low-friction").decision).toBe("allow");
    expect(getRealCodexScenario("low-friction").riskLevel).toBe("low");
    expect(["correlated", "not-applicable"]).toContain(
      getRealCodexScenario("low-friction").auditStatus
    );
    expect(
      realCodexProof.scenarios
        .filter((scenario) => scenario.id !== "low-friction")
        .every(
          (scenario) =>
            scenario.decision === "deny" && scenario.auditStatus === "correlated"
        )
    ).toBe(true);
    expect(getRealCodexScenario("destructive-delete").riskLevel).toBe("critical");
    expect(new Set(realCodexProof.scenarios.map((scenario) => scenario.sessionId)).size).toBe(5);
    expect(
      realCodexProof.scenarios.map((scenario) => scenario.recordingFile)
    ).toEqual(realCodexScenarioIds.map((id) => `scenario-${id}.cast`));
    expect(getRealCodexScenario("sensitive-read").guardSignal).toBe("sensitive_read");
    expect(getRealCodexScenario("sensitive-read").matchedRule).toBe(
      "guard-core-deny-floor"
    );
    expect(getRealCodexScenario("sensitive-read").actionEvidence).toMatchObject({
      source: "validated_contract_reconstruction",
      display: "$ Get-Content .ssh/id_rsa",
      execution: "blocked_before_execution",
      observed: false,
      riskExplanationSource: "scenario_contract"
    });
    expect(getRealCodexScenario("destructive-delete").actionEvidence.display).toBe(
      "$ Remove-Item -Recurse ."
    );
  });

  it("每个场景的同步录制都与派生摘要一致", () => {
    for (const scenario of realCodexScenarios) {
      expect(scenario.recordingData.header.version).toBe(2);
      expect(scenario.recordingData.events).toHaveLength(scenario.recording.eventCount);
      expect(scenario.recordingData.durationMs).toBe(scenario.recording.durationMs);
      expect(realCodexRecordings[scenario.id]).toBe(scenario.recordingData);
    }
  });

  it("保留 Hook trust、清理与产品能力边界", () => {
    expect(realCodexProof.runtime.clientName).toBe("codex-cli");
    expect(realCodexProof.runtime.hookMode).toBe("live");
    expect(realCodexProof.sharedChecks.hookTrusted).toBe(true);
    expect(realCodexProof.sharedChecks.hookSource).toBe("project");
    expect(realCodexProof.sharedChecks.hookTrustBypassed).toBe(false);
    expect(realCodexProof.sharedChecks.cleanupPassed).toBe(true);
    expect(realCodexProof.boundaries.preRecorded).toBe(true);
    expect(realCodexProof.boundaries.browserRealtime).toBe(false);
    expect(realCodexProof.boundaries.syntheticDataOnly).toBe(true);
    expect(realCodexProof.boundaries.credentialsIncluded).toBe(false);
    expect(realCodexProof.boundaries.providerIdentityIncluded).toBe(false);
    expect(realCodexProof.boundaries.osSandboxClaimed).toBe(false);
    expect(realCodexProof.boundaries.completeDlpClaimed).toBe(false);
    expect(realCodexProof.boundaries.codexInteractiveApprovalClaimed).toBe(false);
    expect(realCodexProof.boundaries.codexAskMapping).toBe("conservative_deny");
  });

  it("拒绝缺场景、重复场景和放松后的场景决策", () => {
    const missing = cloneProof();
    missing.scenarios.pop();
    expect(() => parseRealCodexProofDocument(missing)).toThrow(/恰好包含 5 个场景/);

    const duplicate = cloneProof();
    duplicate.scenarios[4].id = "low-friction";
    expect(() => parseRealCodexProofDocument(duplicate)).toThrow(/重复 id/);

    const unsafe = cloneProof();
    unsafe.scenarios[1].decision = "allow";
    expect(() => parseRealCodexProofDocument(unsafe)).toThrow(/安全语义不一致/);

    const missingAudit = cloneProof();
    missingAudit.scenarios[1].auditStatus = "not-applicable";
    expect(() => parseRealCodexProofDocument(missingAudit)).toThrow(/证据语义不一致/);

    const weakDeleteRisk = cloneProof();
    weakDeleteRisk.scenarios[2].riskLevel = "high";
    expect(() => parseRealCodexProofDocument(weakDeleteRisk)).toThrow(/critical 风险语义/);

    const duplicateSession = cloneProof();
    duplicateSession.scenarios[1].sessionId = duplicateSession.scenarios[0].sessionId;
    expect(() => parseRealCodexProofDocument(duplicateSession)).toThrow(
      /sessionId 必须全部唯一/
    );

    const mismatchedRecording = cloneProof();
    mismatchedRecording.scenarios[0].recordingFile = "scenario-sensitive-read.cast";
    expect(() => parseRealCodexProofDocument(mismatchedRecording)).toThrow(
      /recordingFile 必须/
    );

    const interactiveAsk = cloneProof();
    interactiveAsk.boundaries.codexAskMapping = "interactive_ask" as "conservative_deny";
    expect(() => parseRealCodexProofDocument(interactiveAsk)).toThrow(
      /codexAskMapping 必须为 conservative_deny/
    );

    const forgedAction = cloneProof();
    forgedAction.scenarios[2].actionEvidence.source =
      "codex_event" as "hook_request_match";
    expect(() => parseRealCodexProofDocument(forgedAction)).toThrow(
      /source 不在允许范围内/
    );

    const inconsistentExecution = cloneProof();
    inconsistentExecution.scenarios[1].actionEvidence.execution = "completed";
    expect(() => parseRealCodexProofDocument(inconsistentExecution)).toThrow(
      /execution 与 Guard 决策不一致/
    );

    const inconsistentSource = cloneProof();
    inconsistentSource.scenarios[2].actionEvidence.source = "hook_request_match";
    expect(() => parseRealCodexProofDocument(inconsistentSource)).toThrow(
      /observed 与来源不一致/
    );

    const forgedRiskSource = cloneProof();
    forgedRiskSource.scenarios[2].actionEvidence.riskExplanationSource =
      "backend_reason" as "scenario_contract";
    expect(() => parseRealCodexProofDocument(forgedRiskSource)).toThrow(
      /riskExplanationSource 必须为 scenario_contract/
    );
  });

  it("拒绝摘要与录制事件数或时长不一致", () => {
    const rawRecordings = Object.fromEntries(
      realCodexScenarioIds.map((id) => [
        id,
        [
          JSON.stringify({ version: 2, width: 80, height: 24, title: id }),
          JSON.stringify([0.1, "o", "开始\r\n"]),
          JSON.stringify([0.2, "o", "完成\r\n"])
        ].join("\n")
      ])
    ) as Record<(typeof realCodexScenarioIds)[number], string>;

    expect(() => parseRealCodexScenarioRecordings(realCodexProof, rawRecordings)).toThrow(
      /派生摘要与录制文件不一致/
    );
  });

  it("拒绝逆序时间、输入事件、控制字符和超长输出", () => {
    const header = JSON.stringify({
      version: 2,
      width: 80,
      height: 24,
      title: "invalid"
    });
    expect(() =>
      parseRealCodexRecording(
        [header, JSON.stringify([1, "o", "第一步\r\n"]), JSON.stringify([0.5, "i", "\u0007"])]
          .join("\n")
      )
    ).toThrow(/有界纯文本输出事件/);

    expect(() =>
      parseRealCodexRecording(
        [header, JSON.stringify([0.1, "o", "x".repeat(8_193)])].join("\n")
      )
    ).toThrow(/有界纯文本输出事件/);
  });

  it("允许标准 asciicast 可选元数据，但拒绝未知 header 字段", () => {
    const validHeader = JSON.stringify({
      version: 2,
      width: 80,
      height: 24,
      title: "valid",
      timestamp: 1,
      env: { TERM: "xterm-256color" }
    });
    expect(
      parseRealCodexRecording(
        [validHeader, JSON.stringify([0.1, "o", "完成\r\n"])].join("\n")
      ).events
    ).toHaveLength(1);

    const invalidHeader = JSON.stringify({
      version: 2,
      width: 80,
      height: 24,
      title: "invalid",
      unexpected: true
    });
    expect(() =>
      parseRealCodexRecording(
        [invalidHeader, JSON.stringify([0.1, "o", "完成\r\n"])].join("\n")
      )
    ).toThrow(/header 字段集合/);
  });
});
