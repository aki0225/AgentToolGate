import { useEffect, useRef, useState } from "react";

import {
  realCodexProof,
  realCodexRecording,
  type RealCodexRecordingEvent
} from "../evaluation/realCodexProof";
import { Icon } from "./Icon";

const playbackRate = 4;
const initialEventCount = Math.min(3, realCodexRecording.events.length);

function formatClock(milliseconds: number) {
  const seconds = Math.floor(milliseconds / 1000);
  const remainder = milliseconds % 1000;
  return `${String(Math.floor(seconds / 60)).padStart(2, "0")}:${String(seconds % 60).padStart(
    2,
    "0"
  )}.${String(remainder).padStart(3, "0")}`;
}

function eventTone(event: RealCodexRecordingEvent) {
  if (event.text.startsWith("$ ")) {
    return "command";
  }
  if (event.text.startsWith("MCP ")) {
    return "mcp";
  }
  if (event.text.startsWith("命令完成")) {
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
  const [visibleCount, setVisibleCount] = useState(initialEventCount);
  const [playing, setPlaying] = useState(false);
  const screenRef = useRef<HTMLDivElement>(null);
  const reducedMotion = useReducedMotion();
  const events = realCodexRecording.events;
  const visibleEvents = events.slice(0, visibleCount);
  const complete = visibleCount >= events.length;
  const progress = Math.round((visibleCount / events.length) * 100);
  const currentTime = visibleEvents.at(-1)?.timeSeconds ?? 0;

  useEffect(() => {
    if (!playing || complete) {
      if (complete && playing) {
        setPlaying(false);
      }
      return;
    }
    const previousTime = visibleCount === 0 ? 0 : events[visibleCount - 1].timeSeconds;
    const nextTime = events[visibleCount].timeSeconds;
    const delay = Math.min(
      900,
      Math.max(90, ((nextTime - previousTime) * 1000) / playbackRate)
    );
    const timer = window.setTimeout(() => {
      setVisibleCount((count) => Math.min(count + 1, events.length));
    }, delay);
    return () => window.clearTimeout(timer);
  }, [complete, events, playing, visibleCount]);

  useEffect(() => {
    const screen = screenRef.current;
    if (!screen) {
      return;
    }
    screen.scrollTo({
      top: screen.scrollHeight,
      behavior: reducedMotion ? "auto" : "smooth"
    });
  }, [reducedMotion, visibleCount]);

  useEffect(() => {
    if (!reducedMotion) {
      return;
    }
    setPlaying(false);
    setVisibleCount(events.length);
  }, [events.length, reducedMotion]);

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
    setVisibleCount(initialEventCount);
  }

  return (
    <section className="real-codex-proof" id="real-codex-proof" aria-labelledby="real-codex-title">
      <header className="real-codex-header">
        <div>
          <p className="real-codex-kicker">真实客户端证据 · {realCodexProof.publishedAt}</p>
          <h3 id="real-codex-title">真实 Codex CLI · 预录验收</h3>
          <p>
            固定版本 Codex CLI 调用正式 AgentToolGate Release；同步事件展示 MCP 成功，
            独立证据确认受保护的 <code>apply_patch</code> 在执行前被项目 Hook 拒绝。
          </p>
        </div>
        <span className="real-codex-status">
          <Icon name="check" />
          已核验
        </span>
      </header>

      <div className="real-codex-runtime" aria-label="真实 Codex 验收运行环境">
        <span>{realCodexProof.runtime.releaseTag}</span>
        <span>Codex {realCodexProof.runtime.clientVersion}</span>
        <span>{realCodexProof.runtime.platform}</span>
        <span>{formatClock(realCodexProof.recording.durationMs)}</span>
        <span>commit {realCodexProof.source.commitSha.slice(0, 7)}</span>
      </div>

      <div className="real-codex-grid">
        <div className="real-codex-terminal">
          <div className="real-codex-terminal-bar">
            <div aria-hidden="true">
              <span />
              <span />
              <span />
            </div>
            <strong>codex.exec / 同步事件</strong>
            <small>{playbackRate}× 回放</small>
          </div>

          <div
            aria-live="polite"
            aria-relevant="additions text"
            aria-label="真实 Codex CLI 同步事件录制"
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

          <div className="real-codex-verdict">
            <span>PreToolUse</span>
            <strong>deny · {realCodexProof.checks.protectedTarget}</strong>
            <code>{realCodexProof.checks.matchedRule}</code>
          </div>

          <div className="real-codex-controls">
            <button type="button" onClick={togglePlayback}>
              {playing ? "暂停" : complete ? "重新播放" : "播放录制"}
            </button>
            <button className="real-codex-control-secondary" type="button" onClick={resetPlayback}>
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
        </div>

        <aside className="real-codex-evidence" aria-label="真实 Codex 验收后置条件">
          <div className="real-codex-evidence-heading">
            <span>独立后置检查</span>
            <strong>不是模型自报</strong>
          </div>

          <dl className="real-codex-checks">
            <div>
              <dt>MCP 调用</dt>
              <dd>
                <strong>allow / success</strong>
                <code>{realCodexProof.checks.mcpTool}</code>
                <small>唯一 message 与 Audit 精确关联</small>
              </dd>
            </div>
            <div>
              <dt>项目 Hook</dt>
              <dd>
                <strong>project / trusted</strong>
                <code>PreToolUse · no bypass</code>
                <small>使用正式 Release 中的 Go Guard CLI</small>
              </dd>
            </div>
            <div>
              <dt>高危写入</dt>
              <dd>
                <strong>deny / high</strong>
                <code>apply_patch → {realCodexProof.checks.protectedTarget}</code>
                <small>固定补丁恰好观察一次，未执行副作用</small>
              </dd>
            </div>
            <div>
              <dt>执行后</dt>
              <dd>
                <strong>仓库状态保持不变</strong>
                <code>HEAD · tree · worktree unchanged</code>
                <small>认证目录、进程与回环端口均已清理</small>
              </dd>
            </div>
          </dl>

          <div className="real-codex-links">
            <a href={realCodexProof.source.evidenceUrl} rel="noreferrer" target="_blank">
              查看 8 个脱敏证据文件
              <Icon name="external" />
            </a>
            <a href={realCodexProof.source.commitUrl} rel="noreferrer" target="_blank">
              查看录制基线提交
              <Icon name="external" />
            </a>
          </div>
        </aside>
      </div>

      <footer className="real-codex-boundaries">
        <span>预录，不是浏览器实时连接</span>
        <span>synthetic fixture</span>
        <span>不包含真实凭据或 provider 身份</span>
        <span>只证明本次受保护路径写入场景</span>
        <span>不是 OS sandbox、EDR 或完整 DLP</span>
      </footer>
    </section>
  );
}
