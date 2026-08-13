import { describe, expect, it } from "vitest";

import {
  parseRealCodexRecording,
  realCodexProof,
  realCodexRecording
} from "./realCodexProof";

describe("真实 Codex 预录证据", () => {
  it("保留真实客户端、Hook 和边界口径", () => {
    expect(realCodexProof.runtime.clientName).toBe("codex-cli");
    expect(realCodexProof.runtime.platform).toBe("windows-amd64");
    expect(realCodexProof.checks.mcpAllowed).toBe(true);
    expect(realCodexProof.checks.protectedWriteDeniedOnce).toBe(true);
    expect(realCodexProof.checks.hookTrusted).toBe(true);
    expect(realCodexProof.checks.hookTrustBypassed).toBe(false);
    expect(realCodexProof.boundaries.preRecorded).toBe(true);
    expect(realCodexProof.boundaries.browserRealtime).toBe(false);
    expect(realCodexProof.boundaries.credentialsIncluded).toBe(false);
    expect(realCodexProof.boundaries.osSandboxClaimed).toBe(false);
  });

  it("解析同步事件并与派生摘要一致", () => {
    expect(realCodexRecording.header.version).toBe(2);
    expect(realCodexRecording.events).toHaveLength(realCodexProof.recording.eventCount);
    expect(realCodexRecording.durationMs).toBe(realCodexProof.recording.durationMs);
    expect(
      realCodexRecording.events.some((event) =>
        event.text.includes("MCP 调用：agenttoolgate/mock.real_codex_echo")
      )
    ).toBe(true);
    expect(realCodexRecording.events.some((event) => event.text.includes("git status --short"))).toBe(
      true
    );
  });

  it("拒绝逆序时间、非输出事件和控制字符", () => {
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
    ).toThrow(/纯文本输出事件/);
  });
});
