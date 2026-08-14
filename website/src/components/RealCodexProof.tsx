import { useEffect, useRef, useState, type KeyboardEvent } from "react";

import {
  realCodexProof,
  realCodexScenarios,
  type RealCodexRecordingEvent,
  type RealCodexScenarioId
} from "../evaluation/realCodexProof";
import { Icon } from "./Icon";

const playbackRate = 4;
const minimumPlaybackDelayMs = 90;
const maximumPlaybackDelayMs = 900;
const initialEventLimit = 3;

function initialEventCount(eventCount: number) {
  return Math.min(initialEventLimit, eventCount);
}

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
    ? "Hook 观测到的工具调用"
    : "按验收合同复原的动作摘要";
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
  if (event.text.startsWith("$ ")) {
    return "command";
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
  const [visibleCount, setVisibleCount] = useState(() => initialEventCount(events.length));
  const [playing, setPlaying] = useState(false);
  const screenRef = useRef<HTMLDivElement>(null);
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const reducedMotion = useReducedMotion();
  const visibleEvents = events.slice(0, visibleCount);
  const complete = visibleCount >= events.length;
  const progress = Math.round((visibleCount / events.length) * 100);
  const currentTime = visibleEvents.at(-1)?.timeSeconds ?? 0;

  useEffect(() => {
    setPlaying(false);
    setVisibleCount(reducedMotion ? events.length : initialEventCount(events.length));
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
    setVisibleCount(
      reducedMotion
        ? nextScenario.recordingData.events.length
        : initialEventCount(nextScenario.recordingData.events.length)
    );
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
    setVisibleCount(reducedMotion ? events.length : initialEventCount(events.length));
  }

  const decisionAction =
    selectedScenario.decision === "allow" ? "允许执行" : "执行前拒绝";
  const executionLabel =
    selectedScenario.actionEvidence.execution === "completed"
      ? "动作已完成"
      : "动作未执行";
  const auditLabel =
    selectedScenario.auditStatus === "correlated" ? "Audit 已关联" : "Audit 不适用";
  const evidenceSourceLabel = selectedScenario.actionEvidence.observed
    ? "唯一 Hook 请求匹配"
    : "历史验收合同复原";
  const evidenceSourceDetail = selectedScenario.actionEvidence.observed
    ? "验收器从唯一 Hook 请求及其对应 Audit 生成动作摘要，不是 Codex 原始终端事件。"
    : "当前历史 v2 未保存动作摘要；这里按已通过验收的场景合同与运行平台复原，不是原始 Hook 或 Codex 事件。";

  return (
    <section className="real-codex-proof" id="real-codex-proof" aria-labelledby="real-codex-title">
      <header className="real-codex-header">
        <div>
          <p className="real-codex-kicker">真实客户端证据 · {realCodexProof.publishedAt}</p>
          <h3 id="real-codex-title">看见危险动作被执行前拦下</h3>
          <p>
            先看工具调用，再看拒绝理由和独立后置检查。每条动作摘要都标明证据来源。
          </p>
        </div>
        <span className="real-codex-status">
          <Icon name="check" />
          {realCodexScenarios.length} 段已核验证据
        </span>
      </header>

      <div className="real-codex-runtime" aria-label="真实 Codex 验收运行环境">
        <span>{realCodexProof.runtime.releaseTag}</span>
        <span>Codex {realCodexProof.runtime.clientVersion}</span>
        <span>{realCodexProof.runtime.platform}</span>
        <span>{realCodexProof.runtime.model}</span>
        <span>commit {realCodexProof.source.commitSha.slice(0, 7)}</span>
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

        <div className="real-codex-story-layout">
          <ol className="real-codex-story" aria-label={`${selectedScenario.label}动作链`}>
            <li className="real-codex-story-step real-codex-story-intent">
              <span className="real-codex-story-index">01</span>
              <div>
                <small>验收场景指令</small>
                <strong>{selectedScenario.actionEvidence.intent}</strong>
              </div>
            </li>

            <li className="real-codex-story-step real-codex-story-action">
              <span className="real-codex-story-index">02</span>
              <div>
                <small>
                  {actionEvidenceHeading(selectedScenario.actionEvidence.observed)}
                </small>
                <code>{selectedScenario.actionEvidence.display}</code>
                <p>
                  工具 {selectedScenario.actionEvidence.tool} · 目标{" "}
                  {selectedScenario.target}
                </p>
              </div>
            </li>

            <li
              className={`real-codex-story-step real-codex-story-decision real-codex-story-decision-${selectedScenario.decision}`}
            >
              <span className="real-codex-story-index">03</span>
              <div>
                <small>AgentToolGate · PreToolUse</small>
                <strong>{decisionAction}</strong>
                <p>
                  场景风险说明：
                  {selectedScenario.actionEvidence.riskExplanation}
                </p>
                <div className="real-codex-story-signals" aria-label="Guard 判定字段">
                  <code>{selectedScenario.riskLevel}</code>
                  <code>{selectedScenario.guardSignal}</code>
                  <code>{selectedScenario.matchedRule}</code>
                </div>
              </div>
            </li>

            <li className="real-codex-story-step real-codex-story-result">
              <span className="real-codex-story-index">04</span>
              <div>
                <small>独立后置检查</small>
                <strong>{executionLabel}</strong>
                <p>{selectedScenario.postconditionSummary}</p>
              </div>
            </li>
          </ol>

          <aside
            className="real-codex-proof-notes"
            aria-label={`${selectedScenario.label}证据说明`}
          >
            <div>
              <span>证据来源</span>
              <strong>{evidenceSourceLabel}</strong>
              <p>{evidenceSourceDetail}</p>
            </div>
            <div>
              <span>Audit</span>
              <strong>{auditLabel}</strong>
              <p>{selectedScenario.auditSummary}</p>
            </div>
            <div className="real-codex-shared-checks" aria-label="五场景共享可信条件">
              <span>Hook trusted</span>
              <span>无 trust bypass</span>
              <span>清理通过</span>
            </div>
            <div className="real-codex-links">
              <a href={realCodexProof.source.evidenceUrl} rel="noreferrer" target="_blank">
                查看公开脱敏证据
                <Icon name="external" />
              </a>
              <a href={realCodexProof.source.commitUrl} rel="noreferrer" target="_blank">
                查看录制基线提交
                <Icon name="external" />
              </a>
            </div>
          </aside>
        </div>

        <details className="real-codex-raw-proof">
          <summary>
            <span>查看同步录制与验收日志</span>
            <small>
              {selectedScenario.recording.eventCount} events ·{" "}
              {formatClock(selectedScenario.recording.durationMs)}
            </small>
          </summary>

          <div className="real-codex-terminal">
            <div className="real-codex-terminal-bar">
              <div aria-hidden="true">
                <span />
                <span />
                <span />
              </div>
              <strong>codex.exec / {selectedScenario.label}</strong>
              <small>自适应加速回放</small>
            </div>

            <div
              aria-live="polite"
              aria-relevant="additions text"
              aria-label={`${selectedScenario.label}同步事件录制`}
              className="real-codex-screen"
              ref={screenRef}
              role="log"
            >
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
                  {playing ? "暂停" : complete ? "重新播放" : "播放录制"}
                </button>
                <button
                  className="real-codex-control-secondary"
                  type="button"
                  onClick={resetPlayback}
                >
                  重置
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
        </details>
      </div>

      <footer className="real-codex-boundaries">
        <span>预录，不是浏览器实时连接</span>
        <span>仅使用 synthetic 数据</span>
        <span>不包含真实凭据或 provider 身份</span>
        <span>不是 OS sandbox、EDR 或完整 DLP</span>
        <span>Codex ask 当前保守拒绝，不冒充交互审批</span>
      </footer>
    </section>
  );
}
