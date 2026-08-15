import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { realCodexProof } from "../evaluation/realCodexProof";
import {
  actionEvidenceHeading,
  horizontalTabIndex,
  playbackDelayMilliseconds,
  RealCodexProof
} from "./RealCodexProof";

describe("真实 Codex 多场景预录面板", () => {
  it("呈现五个可访问场景标签并默认展示破坏性删除", () => {
    const html = renderToStaticMarkup(<RealCodexProof />);

    expect(html).toContain("从行动意图，到执行前拦截");
    expect(html).toContain('role="tablist"');
    expect(html.match(/role="tab"/g)).toHaveLength(5);
    expect(html).toContain('id="real-codex-tab-low-friction"');
    expect(html).toContain('id="real-codex-tab-sensitive-read"');
    expect(html).toContain('id="real-codex-tab-destructive-delete"');
    expect(html).toContain('id="real-codex-tab-network-egress"');
    expect(html).toContain('id="real-codex-tab-protected-write"');
    expect(html).toContain('aria-selected="true"');
    expect(html.match(/aria-selected="true"/g)).toHaveLength(1);
    expect(html.match(/aria-controls="real-codex-panel"/g)).toHaveLength(5);
    expect(html).toContain('id="real-codex-panel"');
    expect(html).toContain("等待调用");
    expect(html).toContain("等待验证");
    expect(html).not.toContain("<strong>执行前拦截</strong>");
    expect(html).not.toContain("<strong>动作未执行</strong>");
    expect(html).not.toContain("验收场景指令");
    expect(html).not.toContain("场景风险说明");
    expect(html).not.toContain("Evidence ladder");
  });

  it("明确展示动作摘要来源且不冒充原始事件或交互审批", () => {
    const html = renderToStaticMarkup(<RealCodexProof />);

    expect(html).toContain("真实 Hook 请求");
    expect(html).toContain("不是模型原文");
    expect(html).not.toContain("验收合同复原");
    expect(html).toContain("v0.3.2");
    expect(html).toContain("Codex 0.147.0");
    expect(html).toContain("linux-amd64");
    expect(html).toContain(`commit ${realCodexProof.source.commitSha.slice(0, 7)}`);
    expect(html).toContain("synthetic 数据的预录证据");
    expect(html).toContain("不包含真实凭据");
    expect(html).toContain("OS sandbox、EDR 或完整 DLP");
    expect(html).toContain("Codex ask 当前仍按保守拒绝处理");
    expect(html).toContain("公开脱敏证据");
  });

  it("播放器默认停在零秒并从第一条事件开始", () => {
    const html = renderToStaticMarkup(<RealCodexProof />);

    expect(html).toContain("等待播放");
    expect(html).toContain(">播放</button>");
    expect(html).toContain(">回到开头</button>");
    expect(html).not.toContain(">暂停</button>");
    expect(html.match(/class="real-codex-line /g)).toBeNull();
    expect(html).toContain("原始时间轴 · 从 00:00 开始");
    expect(html).not.toContain("自适应加速回放");
    expect(html).toContain("待判定");
    expect(html).toContain("验证叙事回放");
  });

  it("按水平标签语义处理键盘，并保留上下方向键的页面滚动", () => {
    expect(horizontalTabIndex("ArrowRight", 4, 5)).toBe(0);
    expect(horizontalTabIndex("ArrowLeft", 0, 5)).toBe(4);
    expect(horizontalTabIndex("Home", 3, 5)).toBe(0);
    expect(horizontalTabIndex("End", 1, 5)).toBe(4);
    expect(horizontalTabIndex("ArrowUp", 2, 5)).toBeNull();
    expect(horizontalTabIndex("ArrowDown", 2, 5)).toBeNull();
  });

  it("按原始时间轴的有界延迟播放录制", () => {
    expect(playbackDelayMilliseconds(0, 0.02)).toBe(160);
    expect(playbackDelayMilliseconds(1, 2)).toBe(1_000);
    expect(playbackDelayMilliseconds(0, 10)).toBe(10_000);
  });

  it("在标题层区分 Hook 观测证据和合同复原摘要", () => {
    expect(actionEvidenceHeading(true)).toBe("真实 Hook 请求");
    expect(actionEvidenceHeading(false)).toBe("验收合同复原");
  });

  it("危险动作首屏呈现关联 Audit，不声称实时演示或交互审批", () => {
    const html = renderToStaticMarkup(<RealCodexProof />);

    expect(html).toContain("Audit 已关联");
    expect(html).toContain("critical deny Audit");
    expect(html).toContain("guard-core-deny-floor");
    expect(html).not.toContain("每段录制都与 Guard Audit");
    expect(html).not.toContain("实时演示");
    expect(html).not.toContain("交互审批已完成");
  });
});
