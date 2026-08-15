import { useEffect, useRef, useState, type KeyboardEvent } from "react";

import {
  realCodexProof,
  realCodexScenarios,
  type RealCodexRecordingEvent,
  type RealCodexScenarioId
} from "../evaluation/realCodexProof";
import { Icon } from "./Icon";

const playbackRate = 1;
const minimumPlaybackDelayMs = 160;
const maximumPlaybackDelayMs = 10_000;

export function playbackDelayMilliseconds(previousTime: number, nextTime: number) {
  return Math.min(
    maximumPlaybackDelayMs,
    Math.max(minimumPlaybackDelayMs, ((nextTime - previousTime) * 1000) / playbackRate)
  );
}

export function horizontalTabIndex(
  key: string,
  selectedIndex: number,
  itemCount: number
) {
  if (itemCount <= 0) {
    return null;
  }
  switch (key) {
    case "ArrowRight":
      return (selectedIndex + 1) % itemCount;
    case "ArrowLeft":
      return (selectedIndex - 1 + itemCount) % itemCount;
    case "Home":
      return 0;
    case "End":
      return itemCount - 1;
    default:
      return null;
  }
}

export function actionEvidenceHeading(observed: boolean) {
  return observed
    ? "真实 Hook 请求"
    : "验收合同复原";
}

function formatClock(milliseconds: number) {
  const seconds = Math.floor(milliseconds / 1000);
  const remainder = milliseconds % 1000;
  return `${String(Math.floor(seconds / 60)).padStart(2, "0")}:${String(seconds % 60).padStart(
    2,
    "0"
  )}.${String(remainder).padStart(3, "0")}`;
}

function eventTone(event: RealCodexRecordingEvent) {
  const text = event.text.toLowerCase();
  if (event.text.startsWith("$ ") || event.text.startsWith("工具调用：")) {
    return "command";
  }
  if (event.text.startsWith("计划摘要：") || event.text.startsWith("响应摘要：")) {
    return "agent";
  }
  if (event.text.startsWith("MCP ")) {
    return "mcp";
  }
  if (text.includes("deny") || text.includes("拒绝") || text.includes("阻断")) {
    return "danger";
  }
  if (
    event.text.startsWith("命令完成") ||
    text.includes("status=completed") ||
    /\ballow(?:ed)?\b/.test(text)
  ) {
    return "success";
  }
  return "system";
}

function hasTimelineEvent(events: RealCodexRecordingEvent[], prefixes: string[]) {
  return events.some((event) => prefixes.some((prefix) => event.text.startsWith(prefix)));
}

function useReducedMotion() {
  const [reduced, setReduced] = useState(false);

  useEffect(() => {
    const media = window.matchMedia("(prefers-reduced-motion: reduce)");
    const update = () => setReduced(media.matches);
    update();
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);

  return reduced;
}

export function RealCodexProof() {
  const [selectedId, setSelectedId] =
    useState<RealCodexScenarioId>("destructive-delete");
  const selectedIndex = realCodexScenarios.findIndex((scenario) => scenario.id === selectedId);
  const selectedScenario = realCodexScenarios[selectedIndex];
  const events = selectedScenario.recordingData.events;
  const [visibleCount, setVisibleCount] = useState(0);
  const [playing, setPlaying] = useState(false);
  const screenRef = useRef<HTMLDivElement>(null);
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const reducedMotion = useReducedMotion();
  const visibleEvents = events.slice(0, visibleCount);
  const complete = visibleCount >= events.length;
  const progress = events.length === 0 ? 0 : Math.round((visibleCount / events.length) * 100);
  const currentTime = visibleEvents.at(-1)?.timeSeconds ?? 0;
  const decisionRevealed =
    reducedMotion ||
    hasTimelineEvent(visibleEvents, ["AgentToolGate：", "AgentToolGate 决策："]);
  const reasonRevealed =
    reducedMotion ||
    hasTimelineEvent(visibleEvents, ["原因：", "场景风险说明（验收合同）："]);
  const resultRevealed =
    reducedMotion ||
    hasTimelineEvent(visibleEvents, ["验证：", "独立后置条件："]);

  useEffect(() => {
    setPlaying(false);
    setVisibleCount(reducedMotion ? events.length : 0);
  }, [events, reducedMotion]);

  useEffect(() => {
    if (!playing || complete) {
      if (complete && playing) {
        setPlaying(false);
      }
      return;
    }
    const previousTime = visibleCount === 0 ? 0 : events[visibleCount - 1].timeSeconds;
    const nextTime = events[visibleCount].timeSeconds;
    const delay = playbackDelayMilliseconds(previousTime, nextTime);
    const timer = window.setTimeout(() => {
      setVisibleCount((count) => Math.min(count + 1, events.length));
    }, delay);
    return () => window.clearTimeout(timer);
  }, [complete, events, playing, visibleCount]);

  useEffect(() => {
    if (typeof screenRef.current?.scrollTo !== "function") {
      return;
    }
    const screen = screenRef.current;
    screen.scrollTo({
      top: screen.scrollHeight,
      behavior: reducedMotion ? "auto" : "smooth"
    });
  }, [reducedMotion, selectedId, visibleCount]);

  function selectScenario(id: RealCodexScenarioId) {
    const nextScenario = realCodexScenarios.find((scenario) => scenario.id === id);
    if (!nextScenario) {
      return;
    }
    setPlaying(false);
    setSelectedId(id);
    setVisibleCount(reducedMotion ? nextScenario.recordingData.events.length : 0);
  }

  function handleTabKeyDown(event: KeyboardEvent<HTMLButtonElement>) {
    const focusedIndex = tabRefs.current.indexOf(event.currentTarget);
    const nextIndex = horizontalTabIndex(
      event.key,
      focusedIndex >= 0 ? focusedIndex : selectedIndex,
      realCodexScenarios.length
    );
    if (nextIndex === null) {
      return;
    }
    event.preventDefault();
    // 所有标签始终挂载，先同步移动焦点可避免 React 提交与动画帧的时序竞争。
    tabRefs.current[nextIndex]?.focus();
    selectScenario(realCodexScenarios[nextIndex].id);
  }

  function togglePlayback() {
    if (reducedMotion) {
      setVisibleCount(events.length);
      setPlaying(false);
      return;
    }
    if (complete) {
      setVisibleCount(0);
      setPlaying(true);
      return;
    }
    setPlaying((value) => !value);
  }

  function resetPlayback() {
    setPlaying(false);
    setVisibleCount(reducedMotion ? events.length : 0);
  }

  const decisionAction =
    selectedScenario.decision === "allow" ? "允许执行" : "执行前拦截";
  const executionLabel =
    selectedScenario.actionEvidence.execution === "completed"
      ? "动作已完成"
      : "动作未执行";
  const auditLabel =
    selectedScenario.auditStatus === "correlated" ? "Audit 已关联" : "Audit 不适用";
  const evidenceSourceDetail = selectedScenario.actionEvidence.observed
    ? "动作与决策由唯一 Hook 请求和关联 Audit 核对；计划与响应是公开摘要，不是模型原文。"
    : "当前历史 v2 未保存动作摘要；这里按已通过验收的场景合同与运行平台复原，不是原始 Hook 或 Codex 事件。";

  return (
    <section className="real-codex-proof" id="real-codex-proof" aria-labelledby="real-codex-title">
      <header className="real-codex-header">
        <div>
          <p className="real-codex-kicker">真实 Codex 验证回放 · {realCodexProof.publishedAt}</p>
          <h3 id="real-codex-title">从行动意图，到执行前拦截</h3>
          <p>真实调用经 Hook、Audit 与后置检查对齐后，从零秒开始回放。</p>
        </div>
        <span className="real-codex-status">
          <Icon name="check" />
          {realCodexScenarios.length} 个真实场景
        </span>
      </header>

      <div className="real-codex-runtime" aria-label="真实 Codex 验收运行环境">
        <span>{realCodexProof.runtime.releaseTag}</span>
        <span>Codex {realCodexProof.runtime.clientVersion}</span>
        <span>{realCodexProof.runtime.platform}</span>
      </div>

      <div
        className="real-codex-tabs"
        role="tablist"
        aria-label="真实 Codex 验收场景"
        aria-orientation="horizontal"
      >
        {realCodexScenarios.map((scenario, index) => {
          const active = scenario.id === selectedId;
          return (
            <button
              aria-controls="real-codex-panel"
              aria-selected={active}
              className={`real-codex-tab real-codex-tab-${scenario.decision}${
                active ? " real-codex-tab-active" : ""
              }`}
              id={`real-codex-tab-${scenario.id}`}
              key={scenario.id}
              onClick={() => selectScenario(scenario.id)}
              onKeyDown={handleTabKeyDown}
              ref={(element) => {
                tabRefs.current[index] = element;
              }}
              role="tab"
              tabIndex={active ? 0 : -1}
              type="button"
            >
              <span>{scenario.label}</span>
              <small>
                <i aria-hidden="true" />
                {scenario.decision}
              </small>
            </button>
          );
        })}
      </div>

      <div
        aria-labelledby={`real-codex-tab-${selectedScenario.id}`}
        className="real-codex-panel"
        id="real-codex-panel"
        key={selectedScenario.id}
        role="tabpanel"
        tabIndex={0}
      >
        <div className="real-codex-scenario-heading">
          <div>
            <span>场景 {String(selectedIndex + 1).padStart(2, "0")}</span>
            <h4>{selectedScenario.title}</h4>
          </div>
        </div>

        <div className="real-codex-player-layout">
          <div className="real-codex-terminal">
            <div className="real-codex-terminal-bar">
              <div aria-hidden="true">
                <span />
                <span />
                <span />
              </div>
              <strong>验证回放 · {selectedScenario.label}</strong>
              <small>原始时间轴 · 从 00:00 开始</small>
            </div>

            <div
              aria-live="polite"
              aria-relevant="additions text"
              aria-label={`${selectedScenario.label}验证叙事回放`}
              className="real-codex-screen"
              ref={screenRef}
              role="log"
            >
              {visibleEvents.length > 0 ? (
                <ol>
                  {visibleEvents.map((event, index) => (
                    <li
                      className={`real-codex-line real-codex-line-${eventTone(event)}`}
                      key={`${event.timeSeconds}-${index}`}
                    >
                      <time>{event.timeSeconds.toFixed(3)}</time>
                      <code>{event.text}</code>
                    </li>
                  ))}
                </ol>
              ) : (
                <div className="real-codex-ready">
                  <time>00:00.000</time>
                  <code>等待播放</code>
                </div>
              )}
              {!complete ? (
                <span className="real-codex-cursor" aria-hidden="true">
                  _
                </span>
              ) : null}
            </div>

            {reducedMotion ? (
              <div className="real-codex-controls real-codex-controls-static" role="status">
                <span>已展开完整记录</span>
                <time>{formatClock(selectedScenario.recording.durationMs)}</time>
              </div>
            ) : (
              <div className="real-codex-controls">
                <button type="button" onClick={togglePlayback}>
                  {playing ? "暂停" : complete ? "从头重播" : "播放"}
                </button>
                <button
                  className="real-codex-control-secondary"
                  type="button"
                  onClick={resetPlayback}
                >
                  回到开头
                </button>
                <div
                  aria-label={`播放进度 ${progress}%`}
                  aria-valuemax={100}
                  aria-valuemin={0}
                  aria-valuenow={progress}
                  className="real-codex-progress"
                  role="progressbar"
                >
                  <span style={{ width: `${progress}%` }} />
                </div>
                <time>{formatClock(Math.round(currentTime * 1000))}</time>
              </div>
            )}
          </div>

          <aside className="real-codex-verdict" aria-label={`${selectedScenario.label}结果`}>
            <div
              className={`real-codex-verdict-decision ${
                decisionRevealed
                  ? `real-codex-verdict-${selectedScenario.decision}`
                  : "real-codex-verdict-waiting"
              }`}
            >
              <span>AgentToolGate</span>
              <strong>{decisionRevealed ? decisionAction : "等待调用"}</strong>
              {reasonRevealed ? (
                <p>{selectedScenario.actionEvidence.riskExplanation}</p>
              ) : null}
              <code>
                {decisionRevealed
                  ? `${selectedScenario.riskLevel} · ${selectedScenario.guardSignal}`
                  : "待判定"}
              </code>
            </div>
            <div
              className={`real-codex-verdict-result${
                resultRevealed ? "" : " real-codex-verdict-waiting"
              }`}
            >
              <span>最终结果</span>
              <strong>{resultRevealed ? executionLabel : "等待验证"}</strong>
              {resultRevealed ? <p>{selectedScenario.postconditionSummary}</p> : null}
            </div>
          </aside>
        </div>

        <details className="real-codex-methodology">
          <summary>
            <span>证据来源与安全边界</span>
            <small>
              {selectedScenario.recording.eventCount} 条事件 ·{" "}
              {formatClock(selectedScenario.recording.durationMs)}
            </small>
          </summary>
          <div className="real-codex-methodology-grid">
            <div>
              <span>证据</span>
              <strong>
                {actionEvidenceHeading(selectedScenario.actionEvidence.observed)} ·{" "}
                {auditLabel}
              </strong>
              <p>
                {evidenceSourceDetail} {selectedScenario.auditSummary}
              </p>
              <code>
                {selectedScenario.matchedRule} · {realCodexProof.runtime.model} · commit{" "}
                {realCodexProof.source.commitSha.slice(0, 7)}
              </code>
            </div>
            <div>
              <span>边界</span>
              <p>
                这是 synthetic 数据的预录证据，不包含真实凭据，也不声称替代 OS
                sandbox、EDR 或完整 DLP。Codex ask 当前仍按保守拒绝处理。
              </p>
            </div>
            <div className="real-codex-methodology-links">
              <a href={realCodexProof.source.evidenceUrl} rel="noreferrer" target="_blank">
                公开脱敏证据
                <Icon name="external" />
              </a>
              <a href={realCodexProof.source.commitUrl} rel="noreferrer" target="_blank">
                录制基线提交
                <Icon name="external" />
              </a>
            </div>
          </div>
        </details>
      </div>
    </section>
  );
}
