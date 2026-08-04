import { describe, expect, it } from "vitest";

import {
  createDemoState,
  getAvailableEvents,
  transitionDemo,
  type DemoEvent,
  type DemoMachineState,
  type ScenarioId
} from "./stateMachine";

function run(state: DemoMachineState, event: DemoEvent): DemoMachineState {
  const result = transitionDemo(state, event);
  expect(result.accepted, result.reason).toBe(true);
  return result.state;
}

function runSequence(scenarioId: ScenarioId, events: DemoEvent[]): DemoMachineState {
  return events.reduce(run, createDemoState(scenarioId));
}

describe("GitHub 审批状态机", () => {
  it("完成自批拒绝、独立 reviewer 审批、冻结参数执行和脱敏审计", () => {
    const state = runSequence("github-approval", [
      { type: "START" },
      { type: "COMPLETE_EVALUATION" },
      { type: "ATTEMPT_SELF_REVIEW" },
      { type: "SELECT_REVIEWER" },
      { type: "APPROVE", reason: "已核对仓库与冻结参数" },
      { type: "EXECUTE" },
      { type: "WRITE_AUDIT" }
    ]);

    expect(state.status).toBe("audited");
    expect(state.selfReviewBlocked).toBe(true);
    expect(state.actor).toBe("reviewer");
    expect(state.outcome).toBe("approved");
    expect(state.upstreamCalls).toBe(1);
    expect(state.frozenArguments).toBe("cleared");
    expect(state.auditEntries).toBe(1);
    expect(state.auditRedacted).toBe(true);
    expect(state.reviewReason).toBe("已核对仓库与冻结参数");
  });

  it("requester 自批不会进入 approved，也不会触达上游", () => {
    const state = runSequence("github-approval", [
      { type: "START" },
      { type: "COMPLETE_EVALUATION" },
      { type: "ATTEMPT_SELF_REVIEW" }
    ]);

    expect(state.status).toBe("self_review_denied");
    expect(state.outcome).toBe("pending");
    expect(state.upstreamCalls).toBe(0);
    expect(state.frozenArguments).toBe("stored");
  });

  it("reviewer 拒绝后清空冻结参数，上游计数仍为零", () => {
    const state = runSequence("github-approval", [
      { type: "START" },
      { type: "COMPLETE_EVALUATION" },
      { type: "ATTEMPT_SELF_REVIEW" },
      { type: "SELECT_REVIEWER" },
      { type: "REJECT", reason: "仓库范围不符合本次变更" },
      { type: "WRITE_AUDIT" }
    ]);

    expect(state.status).toBe("audited");
    expect(state.outcome).toBe("rejected");
    expect(state.upstreamCalls).toBe(0);
    expect(state.executionCount).toBe(0);
    expect(state.frozenArguments).toBe("cleared");
    expect(state.auditRedacted).toBe(true);
  });
});

describe("本地动作票据状态机", () => {
  it("首次 deny_with_ticket 创建票据，批准后只能消费一次", () => {
    const state = runSequence("local-guard", [
      { type: "START" },
      { type: "COMPLETE_EVALUATION" },
      { type: "SELECT_REVIEWER" },
      { type: "APPROVE", reason: "已核对 synthetic 目标与一次性边界" },
      { type: "EXECUTE" },
      { type: "WRITE_AUDIT" },
      { type: "REPLAY" }
    ]);

    expect(state.status).toBe("replay_denied");
    expect(state.ticketState).toBe("consumed");
    expect(state.ticketUses).toBe(1);
    expect(state.executionCount).toBe(1);
    expect(state.replayDeniedCount).toBe(1);
  });

  it("本地请求被 reviewer 拒绝后不再提供重复消费事件", () => {
    const state = runSequence("local-guard", [
      { type: "START" },
      { type: "COMPLETE_EVALUATION" },
      { type: "SELECT_REVIEWER" },
      { type: "REJECT", reason: "风险与业务收益不匹配" },
      { type: "WRITE_AUDIT" }
    ]);

    expect(state.outcome).toBe("rejected");
    expect(state.ticketUses).toBe(0);
    expect(getAvailableEvents(state)).toEqual([]);
  });
});

describe("MCP Secret 状态机", () => {
  it("只在批准后的模拟执行阶段注入 Secret，并在 Audit 中标记脱敏", () => {
    const beforeExecution = runSequence("mcp-secret", [
      { type: "SYNC_REGISTRY" },
      { type: "START" },
      { type: "COMPLETE_EVALUATION" },
      { type: "SELECT_REVIEWER" },
      { type: "APPROVE", reason: "已核对写类工具和 Secret 边界" }
    ]);

    expect(beforeExecution.secretInjected).toBe(false);
    expect(beforeExecution.upstreamCalls).toBe(0);

    const executed = run(beforeExecution, { type: "EXECUTE" });
    expect(executed.secretInjected).toBe(true);
    expect(executed.upstreamCalls).toBe(1);

    const audited = run(executed, { type: "WRITE_AUDIT" });
    expect(audited.auditRedacted).toBe(true);
    expect(audited.auditEntries).toBe(1);
  });
});

describe("通用迁移约束", () => {
  it("拒绝非法迁移并保持原状态引用", () => {
    const initial = createDemoState("github-approval");
    const result = transitionDemo(initial, { type: "EXECUTE" });

    expect(result.accepted).toBe(false);
    expect(result.state).toBe(initial);
    expect(result.reason).toContain("不接受事件");
  });

  it("审批理由为空时拒绝迁移", () => {
    const reviewerReady = runSequence("mcp-secret", [
      { type: "SYNC_REGISTRY" },
      { type: "START" },
      { type: "COMPLETE_EVALUATION" },
      { type: "SELECT_REVIEWER" }
    ]);
    const result = transitionDemo(reviewerReady, { type: "APPROVE", reason: "   " });

    expect(result.accepted).toBe(false);
    expect(result.state.status).toBe("reviewer_ready");
    expect(result.reason).toBe("审批或拒绝必须填写理由");
  });

  it("重置后回到完全一致的初始状态", () => {
    const changed = runSequence("github-approval", [
      { type: "START" },
      { type: "COMPLETE_EVALUATION" },
      { type: "ATTEMPT_SELF_REVIEW" }
    ]);
    const reset = transitionDemo(changed, { type: "RESET" });

    expect(reset.accepted).toBe(true);
    expect(reset.state).toEqual(createDemoState("github-approval"));
  });
});
