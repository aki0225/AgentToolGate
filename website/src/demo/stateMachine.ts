export const scenarioIds = ["local-guard", "github-approval", "mcp-secret"] as const;

export type ScenarioId = (typeof scenarioIds)[number];

export type DemoStatus =
  | "idle"
  | "registry_synced"
  | "evaluating"
  | "approval_required"
  | "self_review_denied"
  | "reviewer_ready"
  | "approved"
  | "rejected"
  | "executed"
  | "audited"
  | "replay_denied";

export type DemoEventKind =
  | "START"
  | "SYNC_REGISTRY"
  | "COMPLETE_EVALUATION"
  | "ATTEMPT_SELF_REVIEW"
  | "SELECT_REVIEWER"
  | "APPROVE"
  | "REJECT"
  | "EXECUTE"
  | "WRITE_AUDIT"
  | "REPLAY"
  | "RESET";

export type DemoOutcome = "pending" | "approved" | "rejected";
export type FrozenArgumentsState = "none" | "stored" | "cleared";
export type TicketState = "none" | "pending" | "approved" | "consumed";

export interface DemoEvent {
  type: DemoEventKind;
  reason?: string;
}

export interface DemoMachineState {
  scenarioId: ScenarioId;
  status: DemoStatus;
  history: DemoStatus[];
  actor: "requester" | "reviewer";
  outcome: DemoOutcome;
  upstreamCalls: number;
  executionCount: number;
  ticketUses: number;
  replayDeniedCount: number;
  auditEntries: number;
  frozenArguments: FrozenArgumentsState;
  ticketState: TicketState;
  secretInjected: boolean;
  auditRedacted: boolean;
  selfReviewBlocked: boolean;
  reviewReason: string;
}

export interface TransitionResult {
  accepted: boolean;
  state: DemoMachineState;
  reason?: string;
}

type TransitionMap = Partial<Record<DemoStatus, Partial<Record<DemoEventKind, DemoStatus>>>>;

const transitionMaps: Record<ScenarioId, TransitionMap> = {
  "local-guard": {
    idle: { START: "evaluating" },
    evaluating: { COMPLETE_EVALUATION: "approval_required" },
    approval_required: { SELECT_REVIEWER: "reviewer_ready" },
    reviewer_ready: { APPROVE: "approved", REJECT: "rejected" },
    approved: { EXECUTE: "executed" },
    rejected: { WRITE_AUDIT: "audited" },
    executed: { WRITE_AUDIT: "audited" },
    audited: { REPLAY: "replay_denied" }
  },
  "github-approval": {
    idle: { START: "evaluating" },
    evaluating: { COMPLETE_EVALUATION: "approval_required" },
    approval_required: { ATTEMPT_SELF_REVIEW: "self_review_denied" },
    self_review_denied: { SELECT_REVIEWER: "reviewer_ready" },
    reviewer_ready: { APPROVE: "approved", REJECT: "rejected" },
    approved: { EXECUTE: "executed" },
    rejected: { WRITE_AUDIT: "audited" },
    executed: { WRITE_AUDIT: "audited" }
  },
  "mcp-secret": {
    idle: { SYNC_REGISTRY: "registry_synced" },
    registry_synced: { START: "evaluating" },
    evaluating: { COMPLETE_EVALUATION: "approval_required" },
    approval_required: { SELECT_REVIEWER: "reviewer_ready" },
    reviewer_ready: { APPROVE: "approved", REJECT: "rejected" },
    approved: { EXECUTE: "executed" },
    rejected: { WRITE_AUDIT: "audited" },
    executed: { WRITE_AUDIT: "audited" }
  }
};

export const eventPresentation: Record<
  DemoEventKind,
  { label: string; tone: "primary" | "secondary" | "danger" }
> = {
  START: { label: "提交静态请求", tone: "primary" },
  SYNC_REGISTRY: { label: "同步工具目录", tone: "primary" },
  COMPLETE_EVALUATION: { label: "完成规则评估", tone: "primary" },
  ATTEMPT_SELF_REVIEW: { label: "尝试 requester 自批", tone: "danger" },
  SELECT_REVIEWER: { label: "切换为 reviewer", tone: "secondary" },
  APPROVE: { label: "批准本次请求", tone: "primary" },
  REJECT: { label: "拒绝本次请求", tone: "danger" },
  EXECUTE: { label: "执行已批准动作", tone: "primary" },
  WRITE_AUDIT: { label: "写入脱敏审计", tone: "secondary" },
  REPLAY: { label: "再次消费同一票据", tone: "danger" },
  RESET: { label: "重新演示", tone: "secondary" }
};

export const statusLabels: Record<DemoStatus, string> = {
  idle: "等待请求",
  registry_synced: "工具已同步",
  evaluating: "正在评估",
  approval_required: "等待审批",
  self_review_denied: "自批已拒绝",
  reviewer_ready: "审阅人就绪",
  approved: "审批已通过",
  rejected: "审批已拒绝",
  executed: "已执行",
  audited: "已审计",
  replay_denied: "重复消费已拒绝"
};

export function createDemoState(scenarioId: ScenarioId): DemoMachineState {
  return {
    scenarioId,
    status: "idle",
    history: ["idle"],
    actor: "requester",
    outcome: "pending",
    upstreamCalls: 0,
    executionCount: 0,
    ticketUses: 0,
    replayDeniedCount: 0,
    auditEntries: 0,
    frozenArguments: "none",
    ticketState: "none",
    secretInjected: false,
    auditRedacted: false,
    selfReviewBlocked: false,
    reviewReason: ""
  };
}

export function getAvailableEvents(state: DemoMachineState): DemoEventKind[] {
  const events = Object.keys(transitionMaps[state.scenarioId][state.status] ?? {}) as DemoEventKind[];

  // 被拒绝的本地请求没有可消费票据，不能展示重复消费分支。
  if (state.status === "audited" && state.outcome !== "approved") {
    return [];
  }

  return events;
}

export function transitionDemo(state: DemoMachineState, event: DemoEvent): TransitionResult {
  if (event.type === "RESET") {
    return {
      accepted: true,
      state: createDemoState(state.scenarioId)
    };
  }

  const nextStatus = transitionMaps[state.scenarioId][state.status]?.[event.type];
  if (!nextStatus) {
    return {
      accepted: false,
      state,
      reason: `状态 ${state.status} 不接受事件 ${event.type}`
    };
  }

  if ((event.type === "APPROVE" || event.type === "REJECT") && !event.reason?.trim()) {
    return {
      accepted: false,
      state,
      reason: "审批或拒绝必须填写理由"
    };
  }

  const nextState: DemoMachineState = {
    ...state,
    status: nextStatus,
    history: [...state.history, nextStatus]
  };

  applyEventEffects(nextState, event);

  return {
    accepted: true,
    state: nextState
  };
}

function applyEventEffects(state: DemoMachineState, event: DemoEvent): void {
  switch (event.type) {
    case "COMPLETE_EVALUATION":
      if (state.scenarioId === "local-guard") {
        // 首次 deny_with_ticket 已创建待审票据，批准动作不会再生成新票据。
        state.ticketState = "pending";
      } else {
        // Tool Registry 审批会在内部暂存冻结执行参数，公开审计只展示脱敏摘要。
        state.frozenArguments = "stored";
      }
      break;
    case "ATTEMPT_SELF_REVIEW":
      state.selfReviewBlocked = true;
      break;
    case "SELECT_REVIEWER":
      state.actor = "reviewer";
      break;
    case "APPROVE":
      state.outcome = "approved";
      state.reviewReason = event.reason?.trim() ?? "";
      if (state.scenarioId === "local-guard") {
        state.ticketState = "approved";
      }
      break;
    case "REJECT":
      state.outcome = "rejected";
      state.reviewReason = event.reason?.trim() ?? "";
      state.frozenArguments = "cleared";
      break;
    case "EXECUTE":
      state.executionCount += 1;
      if (state.scenarioId === "local-guard") {
        state.ticketState = "consumed";
        state.ticketUses += 1;
      } else {
        state.upstreamCalls += 1;
        state.frozenArguments = "cleared";
      }
      if (state.scenarioId === "mcp-secret") {
        state.secretInjected = true;
      }
      break;
    case "WRITE_AUDIT":
      state.auditEntries += 1;
      state.auditRedacted = true;
      break;
    case "REPLAY":
      state.replayDeniedCount += 1;
      break;
    case "START":
    case "SYNC_REGISTRY":
    case "RESET":
      break;
  }
}
