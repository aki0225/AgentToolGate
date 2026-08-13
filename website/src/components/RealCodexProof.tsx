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
  const [selectedId, setSelectedId] = useState<RealCodexScenarioId>("low-friction");
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
    const nextIndex = horizontalTabIndex(
      event.key,
      selectedIndex,
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

  const decisionLabel = selectedScenario.decision === "allow" ? "允许" : "拒绝";
  const auditLabel =
    selectedScenario.auditStatus === "correlated" ? "Audit 已关联" : "Audit 不适用";

  return (
    <section className="real-codex-proof" id="real-codex-proof" aria-labelledby="real-codex-title">
      <header className="real-codex-header">
        <div>
          <p className="real-codex-kicker">真实客户端证据 · {realCodexProof.publishedAt}</p>
          <h3 id="real-codex-title">真实 Codex CLI · 五场景预录验收</h3>
          <p>
            以正常开发为默认入口，再切换检查敏感读取、破坏性删除、synthetic
            外传和项目保护写入。每段都核对同步事件与独立后置条件；仅在确有记录时展示
            Audit 关联。
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
            <p>{selectedScenario.description}</p>
          </div>
          <div className={`real-codex-decision real-codex-decision-${selectedScenario.decision}`}>
            <small>Guard 决策</small>
            <strong>{decisionLabel}</strong>
            <code>
              {selectedScenario.decision} / {selectedScenario.riskLevel}
            </code>
          </div>
        </div>

        <div className="real-codex-grid">
          <div className="real-codex-terminal">
            <div className="real-codex-terminal-bar">
              <div aria-hidden="true">
                <span />
                <span />
                <span />
              </div>
              <strong>codex.exec / {selectedScenario.label}</strong>
              <small>
                {formatClock(selectedScenario.recording.durationMs)} · 自适应加速回放
              </small>
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

            <div
              className={`real-codex-verdict real-codex-verdict-${selectedScenario.decision}`}
            >
              <span>{selectedScenario.decision === "deny" ? "PreToolUse" : "Guard"}</span>
              <strong>
                {selectedScenario.decision} · {selectedScenario.target}
              </strong>
              <code>{selectedScenario.matchedRule}</code>
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

          <aside className="real-codex-evidence" aria-label={`${selectedScenario.label}验收证据`}>
            <div className="real-codex-evidence-heading">
              <span>同步证据与后置检查</span>
              <strong>不是模型自报</strong>
            </div>

            <dl className="real-codex-checks">
              <div>
                <dt>动作目标</dt>
                <dd>
                  <strong>{selectedScenario.target}</strong>
                  <code>{selectedScenario.guardSignal}</code>
                  <small>
                    动作 {selectedScenario.actionType} ·{" "}
                    {selectedScenario.id === "low-friction" ? "代表性写入规则" : "后端规则"}{" "}
                    {selectedScenario.matchedRule}
                  </small>
                </dd>
              </div>
              <div>
                <dt>执行结果</dt>
                <dd>
                  <strong>
                    {selectedScenario.decision} / {selectedScenario.riskLevel}
                  </strong>
                  <p>{selectedScenario.outcome}</p>
                </dd>
              </div>
              <div>
                <dt>Audit</dt>
                <dd>
                  <strong>{auditLabel}</strong>
                  <p>{selectedScenario.auditSummary}</p>
                </dd>
              </div>
              <div>
                <dt>执行后</dt>
                <dd>
                  <strong>独立后置条件</strong>
                  <p>{selectedScenario.postconditionSummary}</p>
                </dd>
              </div>
            </dl>

            <div className="real-codex-shared-checks" aria-label="五场景共享可信条件">
              <span>Hook {realCodexProof.sharedChecks.hookSource} / trusted</span>
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
