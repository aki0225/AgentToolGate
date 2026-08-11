import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const scriptDirectory = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(scriptDirectory, "../..");
const proofPath = path.join(repositoryRoot, "evaluation/published/agent-safety-proof.json");
const summaryPath = path.join(repositoryRoot, "website/src/data/evaluation-summary.json");
const readmePath = path.join(repositoryRoot, "README.md");
const readmeStart = "<!-- agent-safety-proof:start -->";
const readmeEnd = "<!-- agent-safety-proof:end -->";
const canonicalSuites = {
  "dangerous-actions-v1": 12,
  "benign-development-v1": 12,
  "governance-invariants-v1": 6
};
export const expectedQuickSuites = {
  "dangerous-actions-v1": 6,
  "benign-development-v1": 8,
  "governance-invariants-v1": 6
};
const validDecisions = new Set(["allow", "ask", "deny", "approval_required", "deny_with_ticket"]);

function fail(message) {
  throw new Error(message);
}

async function readJSON(filePath) {
  return JSON.parse(await readFile(filePath, "utf8"));
}

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

function countBy(values, field) {
  return values.reduce((counts, value) => {
    const key = value[field];
    counts[key] = (counts[key] ?? 0) + 1;
    return counts;
  }, {});
}

function assertComposition(cases, expected, label) {
  const actual = countBy(cases, "suite");
  const keys = Object.keys(actual);
  if (
    keys.length !== Object.keys(expected).length ||
    !Object.entries(expected).every(([suite, count]) => actual[suite] === count)
  ) {
    fail(`${label} suite 组成不一致：${JSON.stringify(actual)}`);
  }
}

function normalizeCase(result) {
  const normalized = {
    id: result.caseId,
    suite: result.suite,
    status: result.status,
    durationMs: result.durationMs,
    decisionSilent: result.decisionSilent,
    sideEffectObserved: result.sideEffectObserved,
    upstreamCallsBeforeApproval: result.upstreamCallsBeforeApproval,
    ticketReplaySucceeded: result.ticketReplaySucceeded,
    secretLeakDetected: result.secretLeakDetected
  };
  if (result.actualDecision) {
    normalized.actualDecision = result.actualDecision;
  }
  if (result.skipReason) {
    normalized.skipReason = result.skipReason;
  }
  return normalized;
}

function validateCases(cases, label) {
  const ids = new Set();
  for (const item of cases) {
    if (!item.id || !item.suite || !["passed", "skipped"].includes(item.status)) {
      fail(`${label} 包含无效 case`);
    }
    if (ids.has(item.id)) {
      fail(`${label} case ID 重复：${item.id}`);
    }
    ids.add(item.id);
    if (item.status === "passed" && !item.actualDecision) {
      fail(`${label} passed case 缺少 actualDecision：${item.id}`);
    }
    if (item.status === "skipped" && (!item.skipReason || item.actualDecision)) {
      fail(`${label} skipped case 语义无效：${item.id}`);
    }
    if (
      typeof item.durationMs !== "number" ||
      item.durationMs < 0 ||
      typeof item.decisionSilent !== "boolean" ||
      typeof item.sideEffectObserved !== "boolean" ||
      !Number.isInteger(item.upstreamCallsBeforeApproval) ||
      item.upstreamCallsBeforeApproval < 0 ||
      typeof item.ticketReplaySucceeded !== "boolean" ||
      typeof item.secretLeakDetected !== "boolean"
    ) {
      fail(`${label} case 指标字段无效：${item.id}`);
    }
  }
}

function percentile(values, quantile) {
  if (values.length === 0) {
    return 0;
  }
  const sorted = [...values].sort((left, right) => left - right);
  if (sorted.length === 1) {
    return sorted[0];
  }
  const position = quantile * (sorted.length - 1);
  const lower = Math.floor(position);
  const upper = Math.ceil(position);
  const weight = position - lower;
  return sorted[lower] * (1 - weight) + sorted[upper] * weight;
}

function rate(numerator, denominator) {
  return denominator === 0 ? 0 : numerator / denominator;
}

export function aggregateMetrics(cases) {
  const counts = countBy(cases, "status");
  const dangerous = cases.filter(
    (item) => item.suite === "dangerous-actions-v1" && item.status !== "skipped"
  );
  const benign = cases.filter(
    (item) => item.suite === "benign-development-v1" && item.status !== "skipped"
  );
  const dangerousGoverned = dangerous.filter(
    (item) =>
      validDecisions.has(item.actualDecision) &&
      item.actualDecision !== "allow" &&
      !item.sideEffectObserved
  ).length;
  const benignInterrupted = benign.filter(
    (item) => validDecisions.has(item.actualDecision) && item.actualDecision !== "allow"
  ).length;
  const latencies = cases
    .filter(
      (item) =>
        item.status !== "skipped" &&
        validDecisions.has(item.actualDecision) &&
        item.durationMs >= 0
    )
    .map((item) => item.durationMs);

  return {
    caseCount: cases.length,
    passedCount: counts.passed ?? 0,
    failedCount: counts.failed ?? 0,
    skippedCount: counts.skipped ?? 0,
    dangerousSampleCount: dangerous.length,
    dangerousGovernedCount: dangerousGoverned,
    dangerousGovernedRate: rate(dangerousGoverned, dangerous.length),
    benignSampleCount: benign.length,
    benignInterruptedCount: benignInterrupted,
    benignInterruptionRate: rate(benignInterrupted, benign.length),
    decisionLatencySampleCount: latencies.length,
    decisionLatencyP95Ms: percentile(latencies, 0.95),
    approvalPreUpstreamCalls: cases.reduce(
      (total, item) => total + item.upstreamCallsBeforeApproval,
      0
    ),
    secretLeakCount: cases.filter((item) => item.secretLeakDetected).length,
    ticketReplaySuccessCount: cases.filter((item) => item.ticketReplaySucceeded).length
  };
}

async function loadRun(root, relativeRoot, expectedRunId, platform, sourceLabel) {
  const runRoot = path.join(root, ...relativeRoot.split("/"));
  const resultsPath = path.join(runRoot, "results.json");
  const manifestPath = path.join(runRoot, "run-manifest.json");
  const [resultsBytes, manifestBytes] = await Promise.all([
    readFile(resultsPath),
    readFile(manifestPath)
  ]);
  const document = JSON.parse(resultsBytes.toString("utf8"));
  const manifest = JSON.parse(manifestBytes.toString("utf8"));
  const manifestEntry = manifest.files?.find((entry) => entry.path === "results.json");

  if (document.schemaVersion !== "v1" || document.runId !== expectedRunId) {
    fail(`${sourceLabel} results runId 或 schemaVersion 不匹配`);
  }
  if (document.platform !== platform || manifest.platform !== platform) {
    fail(`${sourceLabel} platform 不匹配`);
  }
  if (manifest.runId !== expectedRunId || manifest.outcome !== "passed") {
    fail(`${sourceLabel} manifest 未通过`);
  }
  if (!manifestEntry || manifestEntry.sizeBytes !== resultsBytes.length) {
    fail(`${sourceLabel} manifest results 大小不匹配`);
  }
  const resultsSHA256 = sha256(resultsBytes);
  if (manifestEntry.sha256 !== resultsSHA256) {
    fail(`${sourceLabel} manifest results SHA256 不匹配`);
  }
  if (document.metrics?.case_count !== document.results?.length || document.metrics.failed_count !== 0) {
    fail(`${sourceLabel} metrics 与 results 不一致或包含 failed`);
  }

  const cases = document.results.map(normalizeCase);
  validateCases(cases, sourceLabel);
  return {
    cases,
    source: {
      label: sourceLabel,
      path: `${relativeRoot}/results.json`,
      resultsSha256: resultsSHA256,
      manifestSha256: sha256(manifestBytes)
    }
  };
}

async function loadFullEvaluation(artifactRoot, runId, platform, artifactId) {
  const artifactName = `agent-safety-proof-pack-full-${platform}-${runId}`;
  const root = path.join(artifactRoot, artifactName);
  const parts = [];
  for (const suite of ["dangerous", "benign", "governance"]) {
    const expectedRunId = `full-${runId}-${platform}-${suite}`;
    parts.push(
      await loadRun(
        root,
        `ci-proof-packs/full/${platform}/${suite}`,
        expectedRunId,
        platform,
        `${platform}/${suite}`
      )
    );
  }
  const cases = parts.flatMap((part) => part.cases);
  assertComposition(cases, canonicalSuites, `${platform} full`);
  return {
    artifact: { id: artifactId, name: artifactName },
    evaluation: {
      id: `full-${platform}`,
      kind: "full",
      platform,
      artifactId,
      sources: parts.map((part) => part.source),
      metrics: aggregateMetrics(cases),
      cases
    }
  };
}

async function loadQuickEvaluation(artifactRoot, runId, artifactId) {
  const artifactName = `agent-safety-proof-pack-quick-${runId}`;
  const root = path.join(artifactRoot, artifactName);
  const part = await loadRun(
    root,
    "ci-proof-packs/quick",
    `pr-quick-${runId}`,
    "linux",
    "linux/quick"
  );
  assertComposition(part.cases, expectedQuickSuites, "quick");
  return {
    artifact: { id: artifactId, name: artifactName },
    evaluation: {
      id: "quick-linux",
      kind: "quick",
      platform: "linux",
      artifactId,
      sources: [part.source],
      metrics: aggregateMetrics(part.cases),
      cases: part.cases
    }
  };
}

export function validateProof(proof) {
  if (proof.schemaVersion !== "v1" || !/^\d{4}-\d{2}-\d{2}$/.test(proof.publishedAt ?? "")) {
    fail("公开评估快照版本或日期无效");
  }
  if (!Number.isInteger(proof.run?.id) || !/^[a-f0-9]{40}$/.test(proof.run?.headSha ?? "")) {
    fail("公开评估快照 run 或 commit 无效");
  }
  if (proof.run.url !== `https://github.com/aki0225/AgentToolGate/actions/runs/${proof.run.id}`) {
    fail("公开评估快照 run URL 无效");
  }
  const expectedArtifacts = new Map([
    [`agent-safety-proof-pack-quick-${proof.run.id}`, "quick-linux"],
    [`agent-safety-proof-pack-full-windows-${proof.run.id}`, "full-windows"],
    [`agent-safety-proof-pack-full-linux-${proof.run.id}`, "full-linux"]
  ]);
  if (proof.artifacts?.length !== expectedArtifacts.size) {
    fail("公开评估快照必须包含三个 Artifact");
  }
  const artifactById = new Map();
  for (const artifact of proof.artifacts) {
    if (
      !Number.isInteger(artifact.id) ||
      artifact.id <= 0 ||
      artifactById.has(artifact.id) ||
      !expectedArtifacts.has(artifact.name)
    ) {
      fail("Artifact provenance 无效");
    }
    artifactById.set(artifact.id, artifact);
  }
  const evaluations = Object.fromEntries(proof.evaluations.map((item) => [item.id, item]));
  if (proof.evaluations.length !== 3 || !evaluations["quick-linux"] || !evaluations["full-windows"] || !evaluations["full-linux"]) {
    fail("公开评估快照必须包含 quick、Windows full 和 Linux full");
  }
  for (const evaluation of proof.evaluations) {
    const artifact = artifactById.get(evaluation.artifactId);
    if (!artifact || expectedArtifacts.get(artifact.name) !== evaluation.id) {
      fail(`${evaluation.id} 与 Artifact provenance 不一致`);
    }
    validateCases(evaluation.cases, evaluation.id);
    assertComposition(
      evaluation.cases,
      evaluation.kind === "quick" ? expectedQuickSuites : canonicalSuites,
      evaluation.id
    );
    if (
      !evaluation.sources?.length ||
      !evaluation.sources.every(
        (source) =>
          typeof source.path === "string" &&
          source.path.startsWith("ci-proof-packs/") &&
          !source.path.includes("..") &&
          !path.isAbsolute(source.path) &&
          /^[a-f0-9]{64}$/.test(source.resultsSha256) &&
          /^[a-f0-9]{64}$/.test(source.manifestSha256)
      )
    ) {
      fail(`${evaluation.id} 缺少可核对的 source SHA256`);
    }
    if (JSON.stringify(evaluation.metrics) !== JSON.stringify(aggregateMetrics(evaluation.cases))) {
      fail(`${evaluation.id} metrics 与逐 case 状态不一致`);
    }
  }
  return proof;
}

export function summarizeEvaluation(evaluation) {
  const metrics = aggregateMetrics(evaluation.cases);
  return {
    id: evaluation.id,
    platform: evaluation.platform,
    total: metrics.caseCount,
    passed: metrics.passedCount,
    failed: metrics.failedCount,
    skipped: metrics.skippedCount
  };
}

export function buildPublicSummary(proof) {
  return {
    schemaVersion: proof.schemaVersion,
    publishedAt: proof.publishedAt,
    run: proof.run,
    evaluations: proof.evaluations.map((evaluation) => ({
      id: evaluation.id,
      kind: evaluation.kind,
      platform: evaluation.platform,
      ...evaluation.metrics
    }))
  };
}

function canonicalJSON(value) {
  return `${JSON.stringify(value, null, 2)}\n`;
}

export function renderReadmeBlock(proof) {
  const summaries = Object.fromEntries(
    proof.evaluations.map((evaluation) => [evaluation.id, summarizeEvaluation(evaluation)])
  );
  const commitUrl = `https://github.com/aki0225/AgentToolGate/commit/${proof.run.headSha}`;
  const sourceUrl = "evaluation/published/agent-safety-proof.json";
  const line = (label, summary) =>
    `- **${label}**：${summary.passed} passed / ${summary.failed} failed / ${summary.skipped} skipped。`;
  return `${readmeStart}
## 实测评估

基于 [GitHub Actions run ${proof.run.id}](${proof.run.url}) 对
[\`${proof.run.headSha.slice(0, 7)}\`](${commitUrl}) 的 synthetic / disposable 评估：

${line("Quick（Linux）", summaries["quick-linux"])}
${line("Windows full", summaries["full-windows"])}
${line("Linux full", summaries["full-linux"])}

数字由 [公开评估快照](${sourceUrl}) 的逐 case 状态计算；同一文件记录 Artifact 名称、
ID 与源文件 SHA256。它不是 OS sandbox 证明，也不替代真实 Codex / Claude Code 客户端验收。
${readmeEnd}`;
}

function updateReadme(readme, block) {
  const start = readme.indexOf(readmeStart);
  const end = readme.indexOf(readmeEnd);
  if (start >= 0 || end >= 0) {
    if (start < 0 || end < start) {
      fail("README 评估区块标记不完整");
    }
    return `${readme.slice(0, start)}${block}${readme.slice(end + readmeEnd.length)}`;
  }
  const insertion = "## 防护范围";
  const index = readme.indexOf(insertion);
  if (index < 0) {
    fail("README 缺少防护范围插入点");
  }
  return `${readme.slice(0, index)}${block}\n\n${readme.slice(index)}`;
}

function parseOptions(args) {
  const options = {};
  for (let index = 0; index < args.length; index += 2) {
    const key = args[index];
    const value = args[index + 1];
    if (!key?.startsWith("--") || value === undefined) {
      fail(`无效参数：${key ?? "<empty>"}`);
    }
    options[key.slice(2)] = value;
  }
  return options;
}

function requiredOption(options, name) {
  const value = options[name];
  if (!value) {
    fail(`缺少 --${name}`);
  }
  return value;
}

async function importProof(args) {
  const options = parseOptions(args);
  const artifactRoot = path.resolve(requiredOption(options, "artifact-root"));
  const runId = requiredOption(options, "run-id");
  const headSha = requiredOption(options, "head-sha");
  const publishedAt = requiredOption(options, "date");
  if (!/^\d+$/.test(runId) || !/^[a-f0-9]{40}$/.test(headSha)) {
    fail("run-id 或 head-sha 格式无效");
  }
  const numericRunId = Number(runId);
  const quick = await loadQuickEvaluation(artifactRoot, runId, Number(requiredOption(options, "quick-artifact-id")));
  const windows = await loadFullEvaluation(artifactRoot, runId, "windows", Number(requiredOption(options, "windows-artifact-id")));
  const linux = await loadFullEvaluation(artifactRoot, runId, "linux", Number(requiredOption(options, "linux-artifact-id")));
  const proof = validateProof({
    schemaVersion: "v1",
    publishedAt,
    run: {
      id: numericRunId,
      url: `https://github.com/aki0225/AgentToolGate/actions/runs/${runId}`,
      headSha
    },
    artifacts: [quick.artifact, windows.artifact, linux.artifact],
    evaluations: [quick.evaluation, windows.evaluation, linux.evaluation]
  });
  const readme = await readFile(readmePath, "utf8");
  await mkdir(path.dirname(proofPath), { recursive: true });
  await mkdir(path.dirname(summaryPath), { recursive: true });
  await writeFile(proofPath, canonicalJSON(proof), "utf8");
  await writeFile(summaryPath, canonicalJSON(buildPublicSummary(proof)), "utf8");
  await writeFile(readmePath, updateReadme(readme, renderReadmeBlock(proof)), "utf8");
}

async function checkProof() {
  const proof = validateProof(await readJSON(proofPath));
  const [readme, summary] = await Promise.all([
    readFile(readmePath, "utf8"),
    readFile(summaryPath, "utf8")
  ]);
  if (summary !== canonicalJSON(buildPublicSummary(proof))) {
    fail("Pages 评估摘要与公开快照不一致；运行 proof:sync 更新");
  }
  const expected = updateReadme(readme, renderReadmeBlock(proof));
  if (expected !== readme) {
    fail("README 实测评估区块与公开快照不一致；运行 proof:sync 更新");
  }
}

async function syncReadme() {
  const proof = validateProof(await readJSON(proofPath));
  const readme = await readFile(readmePath, "utf8");
  await mkdir(path.dirname(summaryPath), { recursive: true });
  await writeFile(summaryPath, canonicalJSON(buildPublicSummary(proof)), "utf8");
  await writeFile(readmePath, updateReadme(readme, renderReadmeBlock(proof)), "utf8");
}

async function main() {
  const [command, ...args] = process.argv.slice(2);
  if (command === "import") {
    await importProof(args);
  } else if (command === "check") {
    await checkProof();
  } else if (command === "sync") {
    await syncReadme();
  } else {
    fail("用法：evaluation-proof.mjs import|check|sync");
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((error) => {
    console.error(error.message);
    process.exitCode = 1;
  });
}
