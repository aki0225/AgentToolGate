import { mkdtemp, readFile, rm, writeFile, copyFile, mkdir } from "node:fs/promises";
import os from "node:os";
import path from "node:path";

import { afterEach, describe, expect, it } from "vitest";

import {
  evidenceRoot,
  loadAndValidateEvidence,
  parseAsciicast
} from "./real-codex-proof.mjs";

const temporaryRoots = [];

async function copyEvidence() {
  const root = await mkdtemp(path.join(os.tmpdir(), "atg-real-codex-proof-"));
  temporaryRoots.push(root);
  await mkdir(root, { recursive: true });
  for (const name of [
    "audit.json",
    "cleanup.json",
    "codex-real-demo.cast",
    "hook-trust.json",
    "manifest.json",
    "postconditions.json",
    "summary.json",
    "transcript.txt"
  ]) {
    await copyFile(path.join(evidenceRoot, name), path.join(root, name));
  }
  return root;
}

async function refreshManifestEntry(root, name) {
  const { createHash } = await import("node:crypto");
  const manifestPath = path.join(root, "manifest.json");
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  const bytes = await readFile(path.join(root, name));
  const entry = manifest.files.find((item) => item.path === name);
  entry.size = bytes.length;
  entry.sha256 = createHash("sha256").update(bytes).digest("hex");
  await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`, "utf8");
}

afterEach(async () => {
  await Promise.all(
    temporaryRoots.splice(0).map((root) => rm(root, { recursive: true, force: true }))
  );
});

describe("真实 Codex 公开证据", () => {
  it("从当前白名单证据派生稳定页面摘要", async () => {
    const evidence = await loadAndValidateEvidence();

    expect(evidence.summary.source.commitSha).toBe(
      "cd63620e41f11a0ef979ef412c3cbc46caf8b5f9"
    );
    expect(evidence.summary.runtime.platform).toBe("windows-amd64");
    expect(evidence.summary.checks.mcpAllowed).toBe(true);
    expect(evidence.summary.checks.protectedWriteDeniedOnce).toBe(true);
    expect(evidence.summary.checks.hookTrusted).toBe(true);
    expect(evidence.summary.boundaries.browserRealtime).toBe(false);
    expect(evidence.summary.boundaries.osSandboxClaimed).toBe(false);
    expect(evidence.summary.recording.eventCount).toBeGreaterThan(10);
  });

  it("拒绝 manifest 未覆盖的篡改", async () => {
    const root = await copyEvidence();
    const auditPath = path.join(root, "audit.json");
    const audit = JSON.parse(await readFile(auditPath, "utf8"));
    audit.mcp.status = "failed";
    await writeFile(auditPath, `${JSON.stringify(audit, null, 2)}\n`, "utf8");

    await expect(loadAndValidateEvidence(root)).rejects.toThrow(/SHA256 不一致/);
  });

  it("即使刷新 manifest 也拒绝放松后的 deny 语义", async () => {
    const root = await copyEvidence();
    const auditPath = path.join(root, "audit.json");
    const audit = JSON.parse(await readFile(auditPath, "utf8"));
    audit.dangerousWrite.policyDecision = "allow";
    await writeFile(auditPath, `${JSON.stringify(audit, null, 2)}\n`, "utf8");
    await refreshManifestEntry(root, "audit.json");

    await expect(loadAndValidateEvidence(root)).rejects.toThrow(/高危写入 Audit/);
  });

  it("拒绝白名单外文件", async () => {
    const root = await copyEvidence();
    await writeFile(path.join(root, "raw-session.jsonl"), "{}\n", "utf8");

    await expect(loadAndValidateEvidence(root)).rejects.toThrow(/文件集合/);
  });

  it("拒绝带控制字符或逆序时间的录制", () => {
    expect(() =>
      parseAsciicast(
        [
          JSON.stringify({ version: 2, width: 80, height: 24 }),
          JSON.stringify([1, "o", "第一步\r\n"]),
          JSON.stringify([0.5, "o", "第二步\u0007"])
        ].join("\n")
      )
    ).toThrow(/纯文本输出事件/);
  });
});
