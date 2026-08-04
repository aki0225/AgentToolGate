import type { ScenarioDefinition } from "../demo/scenarios";
import {
  eventPresentation,
  statusLabels,
  type DemoEventKind,
  type DemoMachineState,
  type FrozenArgumentsState,
  type TicketState
} from "../demo/stateMachine";
import { Icon } from "./Icon";

interface DemoConsoleProps {
  scenario: ScenarioDefinition;
  state: DemoMachineState;
  availableEvents: DemoEventKind[];
  reviewReason: string;
  feedback: string;
  onReviewReasonChange: (value: string) => void;
  onEvent: (event: DemoEventKind) => void;
  onReset: () => void;
}

const frozenArgumentsLabels: Record<FrozenArgumentsState, string> = {
  none: "未暂存",
  stored: "内部暂存",
  cleared: "已清空"
};

const ticketLabels: Record<TicketState, string> = {
  none: "未创建",
  pending: "待审",
  approved: "已批准，可消费一次",
  consumed: "已消费"
};

export function DemoConsole({
  scenario,
  state,
  availableEvents,
  reviewReason,
  feedback,
  onReviewReasonChange,
  onEvent,
  onReset
}: DemoConsoleProps) {
  const view = scenario.states[state.status];

  if (!view) {
    throw new Error(`场景 ${scenario.id} 缺少状态 ${state.status} 的展示定义`);
  }

  const metrics =
    state.scenarioId === "local-guard"
      ? [
          { label: "本地执行", value: state.executionCount.toString().padStart(2, "0") },
          { label: "票据消费", value: state.ticketUses.toString().padStart(2, "0") },
          { label: "重复拒绝", value: state.replayDeniedCount.toString().padStart(2, "0") }
        ]
      : [
          { label: "上游请求", value: state.upstreamCalls.toString().padStart(2, "0") },
          { label: "执行次数", value: state.executionCount.toString().padStart(2, "0") },
          { label: "审计记录", value: state.auditEntries.toString().padStart(2, "0") }
        ];

  return (
    <div
      className="demo-console"
      id={`scenario-panel-${scenario.id}`}
      role="tabpanel"
      aria-labelledby={`scenario-tab-${scenario.id}`}
      tabIndex={0}
    >
      <div className="demo-console-banner">
        <span>
          <Icon name="lock" />
          Synthetic demo · 不连接真实后端或上游服务
        </span>
        <button className="text-button" type="button" onClick={onReset}>
          重新演示
        </button>
      </div>

      <div className="demo-console-grid">
        <section className="demo-request-pane" aria-labelledby={`${scenario.id}-request-title`}>
          <div className="console-pane-heading">
            <h3 id={`${scenario.id}-request-title`}>请求</h3>
          </div>
          <dl className="fixture-list">
            {scenario.fixture.map((item) => (
              <div key={item.label}>
                <dt>{item.label}</dt>
                <dd>{item.code ? <code>{item.value}</code> : item.value}</dd>
              </div>
            ))}
          </dl>
          <div className="signal-list" aria-label="风险信号">
            {scenario.signals.map((signal) => (
              <code key={signal}>{signal}</code>
            ))}
          </div>
        </section>

        <section className="demo-state-pane" aria-labelledby={`${scenario.id}-state-title`}>
          <div className="console-pane-heading console-pane-heading-inline">
            <h3 id={`${scenario.id}-state-title`}>状态</h3>
            <span className={`state-badge state-badge-${view.tone}`}>
              {statusLabels[state.status]}
            </span>
          </div>

          <div className="state-announcement" aria-live="polite" aria-atomic="true">
            <span>{view.decision}</span>
            <h4>{view.headline}</h4>
            <p>{view.description}</p>
          </div>

          <ul className="state-evidence">
            {view.evidence.map((item) => (
              <li key={item}>
                <Icon name="check" />
                <span>{item}</span>
              </li>
            ))}
          </ul>

          {state.status === "reviewer_ready" ? (
            <label className="review-reason">
              <span>审阅理由</span>
              <textarea
                maxLength={160}
                rows={3}
                value={reviewReason}
                onChange={(event) => onReviewReasonChange(event.target.value)}
              />
              <small>{reviewReason.length}/160，理由只保存在当前页面内存中。</small>
            </label>
          ) : null}

          <div className="demo-actions" aria-label="当前可用状态迁移">
            {availableEvents.map((event) => {
              const presentation = eventPresentation[event];
              const label = scenario.actionLabels?.[event] ?? presentation.label;
              return (
                <button
                  className={`demo-action demo-action-${presentation.tone}`}
                  key={event}
                  type="button"
                  onClick={() => onEvent(event)}
                >
                  {label}
                  <Icon name={presentation.tone === "danger" ? "warning" : "arrow"} />
                </button>
              );
            })}
          </div>
          {feedback ? (
            <p className="demo-feedback" role="alert">
              {feedback}
            </p>
          ) : null}

          <ol className="state-history" aria-label="已完成状态">
            {state.history.map((status, index) => (
              <li
                className={index === state.history.length - 1 ? "state-history-current" : ""}
                key={`${status}-${index}`}
              >
                <span>{String(index + 1).padStart(2, "0")}</span>
                {statusLabels[status]}
              </li>
            ))}
          </ol>
        </section>

        <aside className="demo-evidence-pane" aria-labelledby={`${scenario.id}-evidence-title`}>
          <div className="console-pane-heading">
            <h3 id={`${scenario.id}-evidence-title`}>证据</h3>
          </div>

          <div className="metric-strip">
            {metrics.map((metric) => (
              <div key={metric.label}>
                <strong>{metric.value}</strong>
                <span>{metric.label}</span>
              </div>
            ))}
          </div>

          <dl className="evidence-list">
            <div>
              <dt>当前 actor</dt>
              <dd>
                <code>{state.actor}</code>
              </dd>
            </div>
            <div>
              <dt>最终结果</dt>
              <dd>
                <code>{state.outcome}</code>
              </dd>
            </div>
            {state.scenarioId === "local-guard" ? (
              <div>
                <dt>一次性 ticket</dt>
                <dd>{ticketLabels[state.ticketState]}</dd>
              </div>
            ) : (
              <div>
                <dt>冻结执行参数</dt>
                <dd>{frozenArgumentsLabels[state.frozenArguments]}</dd>
              </div>
            )}
            {state.scenarioId === "mcp-secret" ? (
              <>
                <div>
                  <dt>模型可见 Secret</dt>
                  <dd>否</dd>
                </div>
                <div>
                  <dt>运行时 Secret</dt>
                  <dd>{state.secretInjected ? "已注入 synthetic runtime" : "尚未注入"}</dd>
                </div>
              </>
            ) : null}
            <div>
              <dt>Audit 输出</dt>
              <dd>
                <code>{state.auditRedacted ? "[REDACTED]" : "待写入"}</code>
              </dd>
            </div>
            {state.reviewReason ? (
              <div>
                <dt>审阅理由</dt>
                <dd>{state.reviewReason}</dd>
              </div>
            ) : null}
          </dl>
        </aside>
      </div>
    </div>
  );
}
