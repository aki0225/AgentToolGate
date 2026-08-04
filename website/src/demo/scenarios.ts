import type { DemoStatus, ScenarioId } from "./stateMachine";

export type ScenarioTone = "neutral" | "info" | "warning" | "success" | "danger";

export interface ScenarioStateView {
  tone: ScenarioTone;
  headline: string;
  description: string;
  decision: string;
  evidence: string[];
}

export interface ScenarioDefinition {
  id: ScenarioId;
  index: string;
  shortName: string;
  title: string;
  summary: string;
  defaultReviewReason: string;
  fixture: Array<{
    label: string;
    value: string;
    code?: boolean;
  }>;
  signals: string[];
  requestPreview: string;
  actionLabels?: Partial<Record<string, string>>;
  states: Partial<Record<DemoStatus, ScenarioStateView>>;
}

export const scenarios: ScenarioDefinition[] = [
  {
    id: "local-guard",
    index: "01",
    shortName: "本地高危动作",
    title: "Local Action Firewall：一次性票据",
    summary: "识别持久化落点和隐藏执行信号，在 Codex 无完整 interactive ask 时保守阻断。",
    defaultReviewReason: "已核对 synthetic 目标、风险信号与一次性消费边界",
    fixture: [
      { label: "adapter", value: "Codex", code: true },
      { label: "action", value: "write", code: true },
      { label: "target", value: "C:\\Synthetic\\Startup\\agent-note.ps1", code: true },
      { label: "actor", value: "requester", code: true }
    ],
    signals: ["persistence", "powershell_hidden", "execution_policy_bypass"],
    requestPreview: `{
  "tool": "synthetic.write_file",
  "target": "C:\\\\Synthetic\\\\Startup\\\\agent-note.ps1",
  "content": "[示例内容已省略]"
}`,
    actionLabels: {
      START: "提交本地动作",
      COMPLETE_EVALUATION: "运行 Guard 评估",
      SELECT_REVIEWER: "切换为 reviewer",
      APPROVE: "批准现有一次性票据",
      EXECUTE: "消费票据并模拟执行",
      WRITE_AUDIT: "写入 Guard 审计",
      REPLAY: "再次消费同一票据"
    },
    states: {
      idle: {
        tone: "neutral",
        headline: "合成动作尚未进入治理",
        description: "页面只保存内存状态，不读取本地文件，也不执行示例内容。",
        decision: "idle",
        evidence: ["本地执行次数为 0", "ticket 尚未创建"]
      },
      evaluating: {
        tone: "info",
        headline: "Guard 正在归一化目标与信号",
        description: "识别 action type、敏感落点和内容风险标签，示例不包含可执行恶意脚本。",
        decision: "evaluating",
        evidence: ["target_category=sensitive", "risk_level=high"]
      },
      approval_required: {
        tone: "warning",
        headline: "首次 deny_with_ticket 已创建待审票据",
        description: "高风险动作被保守阻断；票据已存在，Reviewer 之后批准的是这张票据，而不是重新生成一张。",
        decision: "deny_with_ticket",
        evidence: ["ticket.status=pending", "高风险 fingerprint 不进入 remembered allow"]
      },
      reviewer_ready: {
        tone: "warning",
        headline: "Reviewer 正在审阅现有票据",
        description: "审阅人核对 synthetic 目标、风险信号和单次消费边界后才能作出决定。",
        decision: "ticket_review",
        evidence: ["actor=reviewer", "本地执行次数仍为 0"]
      },
      approved: {
        tone: "success",
        headline: "现有票据已批准，可消费一次",
        description: "批准不会直接制造长期放行；高风险票据仍绑定原 fingerprint，并只允许单次消费。",
        decision: "ticket_approved",
        evidence: ["ticket.status=approved", "ticket.uses=0"]
      },
      rejected: {
        tone: "danger",
        headline: "Reviewer 已拒绝本次本地动作",
        description: "动作保持阻断，票据不会进入可消费状态。",
        decision: "rejected",
        evidence: ["本地执行次数为 0", "ticket.uses=0"]
      },
      executed: {
        tone: "success",
        headline: "一次性票据已消费",
        description: "静态状态机只把本地执行计数变为 1；页面没有执行命令或写入文件。",
        decision: "ticket_consumed",
        evidence: ["ticket.uses=1", "本地执行次数=1"]
      },
      audited: {
        tone: "info",
        headline: "风险解释与结果已脱敏留痕",
        description: "Audit 与 OTel 不保存原始敏感内容，只保留受限字段、决策和关联标识。",
        decision: "audited",
        evidence: ["target 使用 synthetic 值", "敏感内容标记为 [REDACTED]"]
      },
      replay_denied: {
        tone: "danger",
        headline: "同一票据的第二次消费被拒绝",
        description: "ticket 已经 consumed；重复使用不会增加执行次数。",
        decision: "ticket_already_consumed",
        evidence: ["ticket.uses 仍为 1", "本地执行次数仍为 1"]
      }
    }
  },
  {
    id: "github-approval",
    index: "02",
    shortName: "GitHub 写审批",
    title: "require_approval：冻结参数后再执行",
    summary: "审批前上游计数保持为 0；requester 不能自批，Reviewer 只执行创建审批时冻结的参数。",
    defaultReviewReason: "已核对 acme/demo 与冻结参数，仅批准本次 synthetic 请求",
    fixture: [
      { label: "tool", value: "github.create_issue", code: true },
      { label: "repository", value: "acme/demo", code: true },
      { label: "actor", value: "requester", code: true },
      { label: "operation", value: "write", code: true }
    ],
    signals: ["repository_allowlist", "write_operation", "independent_reviewer"],
    requestPreview: `{
  "tool": "github.create_issue",
  "arguments": {
    "repository": "acme/demo",
    "title": "更新发布检查清单",
    "body": "synthetic issue body"
  }
}`,
    actionLabels: {
      START: "提交 Tool Registry 调用",
      COMPLETE_EVALUATION: "执行 Policy 决策",
      ATTEMPT_SELF_REVIEW: "尝试 requester 自批",
      SELECT_REVIEWER: "切换为 reviewer",
      APPROVE: "批准冻结请求",
      EXECUTE: "执行冻结参数",
      WRITE_AUDIT: "写入脱敏 Audit"
    },
    states: {
      idle: {
        tone: "neutral",
        headline: "GitHub 写请求尚未提交",
        description: "synthetic fixture 只存在于浏览器内存中，不连接 GitHub API。",
        decision: "idle",
        evidence: ["policy 尚未评估", "上游请求计数=0"]
      },
      evaluating: {
        tone: "info",
        headline: "Tool Registry 调用进入 Policy",
        description: "仓库范围和写操作属性先进入确定性治理，再决定是否需要人工审批。",
        decision: "evaluating",
        evidence: ["tool=github.create_issue", "operation=write"]
      },
      approval_required: {
        tone: "warning",
        headline: "Policy 要求审批，调用保持 pending",
        description: "内部暂存创建审批时冻结的执行参数；公开 Audit 只展示脱敏摘要，审批前不会触达上游。",
        decision: "policy=require_approval · call.status=approval_required",
        evidence: ["frozen arguments=stored", "上游请求计数=0"]
      },
      self_review_denied: {
        tone: "danger",
        headline: "requester 自批返回 forbidden",
        description: "审批仍保持 pending，冻结参数未被执行，上游请求计数继续为 0。",
        decision: "403 forbidden",
        evidence: ["approval.status=pending", "上游请求计数=0"]
      },
      reviewer_ready: {
        tone: "warning",
        headline: "独立 Reviewer 已接管审阅",
        description: "填写理由并核对 repository、title 与 body 的冻结快照后，才能批准或拒绝。",
        decision: "reviewer_ready",
        evidence: ["actor=reviewer", "frozen arguments=stored"]
      },
      approved: {
        tone: "success",
        headline: "审批通过，等待执行冻结参数",
        description: "下一步模拟执行审批创建时保存的内部参数，不从审批请求重新读取参数。",
        decision: "approval.status=approved",
        evidence: ["上游请求计数仍为 0", "执行输入来自 frozen arguments"]
      },
      rejected: {
        tone: "danger",
        headline: "Reviewer 已拒绝请求",
        description: "冻结执行参数被清空，不触达 GitHub 上游。",
        decision: "approval.status=rejected",
        evidence: ["上游请求计数=0", "frozen arguments=cleared"]
      },
      executed: {
        tone: "success",
        headline: "冻结参数已执行一次",
        description: "只有批准后的模拟执行阶段才把上游请求计数从 0 变为 1。",
        decision: "call.status=success",
        evidence: ["上游请求计数=1", "frozen arguments=cleared"]
      },
      audited: {
        tone: "info",
        headline: "Requester、Reviewer 与理由已留痕",
        description: "Audit 展示决策、审批身份、理由和脱敏参数摘要，不保存原始敏感内容。",
        decision: "audited",
        evidence: ["review reason 已记录", "input/output 使用脱敏字段"]
      }
    }
  },
  {
    id: "mcp-secret",
    index: "03",
    shortName: "MCP 与 Secret",
    title: "MCP Outbound：模型参数与 Secret 分离",
    summary: "外部工具同步进 Tool Registry；ATG 管理的 Connector Secret 不进入模型参数，由后端运行时注入。",
    defaultReviewReason: "已核对写类 MCP 工具、冻结参数与运行时 Secret 注入边界",
    fixture: [
      { label: "client tool", value: "mcp_weather.create_note", code: true },
      { label: "connector", value: "mcp_weather", code: true },
      { label: "secretRef", value: "WEATHER_DEMO_TOKEN_ENV", code: true },
      { label: "transport", value: "HTTP + SSE", code: true }
    ],
    signals: ["remote_tool_sync", "write_or_unknown", "env_backed_secret"],
    requestPreview: `{
  "tool": "mcp_weather.create_note",
  "arguments": {
    "city": "杭州",
    "note": "准备 synthetic 现场检查"
  }
}

// 模型参数中没有 token、Authorization 或 secret value`,
    actionLabels: {
      SYNC_REGISTRY: "同步外部 MCP 工具",
      START: "提交 MCP 工具调用",
      COMPLETE_EVALUATION: "执行写类工具治理",
      SELECT_REVIEWER: "切换为 reviewer",
      APPROVE: "批准冻结请求",
      EXECUTE: "运行时注入并模拟执行",
      WRITE_AUDIT: "写入 [REDACTED] 审计"
    },
    states: {
      idle: {
        tone: "neutral",
        headline: "外部 MCP 工具尚未同步",
        description: "该演示不建立网络连接，只模拟 Connector metadata 进入 Tool Registry。",
        decision: "idle",
        evidence: ["模型参数不含 Secret", "上游请求计数=0"]
      },
      registry_synced: {
        tone: "info",
        headline: "远端工具已同步进 Tool Registry",
        description: "写类、未知或破坏性工具采用保守治理；这里不宣称完整 Streamable HTTP、OAuth 或 stdio Outbound。",
        decision: "tool_registered",
        evidence: ["tool=mcp_weather.create_note", "operation=write"]
      },
      evaluating: {
        tone: "info",
        headline: "MCP 工具调用进入统一治理链路",
        description: "所有经 ATG 接入的 Tool Registry 调用先经过 Policy 与硬护栏。",
        decision: "evaluating",
        evidence: ["Connector workspace 已核对", "Secret value 尚未解析"]
      },
      approval_required: {
        tone: "warning",
        headline: "写类 MCP 工具需要审批",
        description: "调用状态进入 approval_required，内部暂存冻结参数；审批前不触达外部 MCP Server。",
        decision: "policy=require_approval · call.status=approval_required",
        evidence: ["上游请求计数=0", "模型仍只看到 schema 与普通参数"]
      },
      reviewer_ready: {
        tone: "warning",
        headline: "Reviewer 正在核对冻结参数",
        description: "审阅内容不包含 Connector Secret value，只包含工具、普通参数和安全 metadata。",
        decision: "reviewer_ready",
        evidence: ["frozen arguments=stored", "secret value=not exposed"]
      },
      approved: {
        tone: "success",
        headline: "审批通过，Secret 仍未注入",
        description: "只有进入最终 Connector Runtime 时，后端才解析 env-backed valueRef。",
        decision: "approval.status=approved",
        evidence: ["上游请求计数=0", "runtime secret=not injected"]
      },
      rejected: {
        tone: "danger",
        headline: "Reviewer 已拒绝 MCP 写请求",
        description: "冻结参数被清空，Secret 不解析，外部 MCP Server 不会收到请求。",
        decision: "approval.status=rejected",
        evidence: ["上游请求计数=0", "runtime secret=not injected"]
      },
      executed: {
        tone: "success",
        headline: "后端运行时完成 Secret 注入",
        description: "模型参数从未包含 Secret；批准后的模拟执行阶段才注入 Connector Secret，并把上游计数变为 1。",
        decision: "call.status=success",
        evidence: ["runtime secret=injected", "上游请求计数=1"]
      },
      audited: {
        tone: "info",
        headline: "Audit 与 OTel 只保留脱敏内容",
        description: "公开证据使用 [REDACTED]，不保存原始 Secret、Authorization、body 或 MCP session。",
        decision: "output=[REDACTED]",
        evidence: ["trace id 可关联", "原始敏感内容不进入 Audit / OTel"]
      }
    }
  }
];

export const scenarioById = Object.fromEntries(
  scenarios.map((scenario) => [scenario.id, scenario])
) as Record<ScenarioId, ScenarioDefinition>;
