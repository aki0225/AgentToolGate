import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import {
  actionEvidenceHeading,
  horizontalTabIndex,
  playbackDelayMilliseconds,
  RealCodexProof
} from "./RealCodexProof";

describe("真实 Codex 多场景预录面板", () => {
  it("呈现五个可访问场景标签并默认展示破坏性删除", () => {
    const html = renderToStaticMarkup(<RealCodexProof />);

    expect(html).toContain("看见危险动作被执行前拦下");
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
    expect(html).toContain("critical");
    expect(html).toContain("root_delete");
    expect(html).toContain("$ rm -rf .");
    expect(html).toContain("执行前拒绝");
    expect(html).toContain("场景风险说明");
    expect(html).toContain("动作未执行");
    expect(html).toContain("仓库根目录、sentinel、HEAD、tree 和干净工作区全部保持不变");
    expect(html).toContain("验收场景指令");
    expect(html).toContain("Hook 观测到的工具调用");
    expect(html).not.toContain("Agent 意图");
    expect(html).not.toContain("实际工具调用摘要");
  });

  it("明确展示动作摘要来源且不冒充原始事件或交互审批", () => {
    const html = renderToStaticMarkup(<RealCodexProof />);

    expect(html).toContain("唯一 Hook 请求匹配");
    expect(html).toContain("不是 Codex 原始终端事件");
    expect(html).not.toContain("历史验收合同复原");
    expect(html).toContain("v0.3.2");
    expect(html).toContain("Codex 0.147.0");
    expect(html).toContain("linux-amd64");
    expect(html).toContain("commit bf0bb9d");
    expect(html).toContain("预录，不是浏览器实时连接");
    expect(html).toContain("仅使用 synthetic 数据");
    expect(html).toContain("不包含真实凭据或 provider 身份");
    expect(html).toContain("不是 OS sandbox、EDR 或完整 DLP");
    expect(html).toContain("Codex ask 当前保守拒绝，不冒充交互审批");
    expect(html).toContain("查看公开脱敏证据");
  });

  it("原始录制默认折叠，播放器不自动播放并只准备前三条事件", () => {
    const html = renderToStaticMarkup(<RealCodexProof />);

    expect(html).toContain("<details");
    expect(html).not.toContain("<details open");
    expect(html).toContain("查看同步录制与验收日志");
    expect(html).toContain(">播放录制</button>");
    expect(html).toContain(">重置</button>");
    expect(html).not.toContain(">暂停</button>");
    expect(html.match(/class="real-codex-line /g)).toHaveLength(3);
    expect(html).toContain("自适应加速回放");
    expect(html).not.toContain("4×");
    expect(html).toContain("root_delete");
  });

  it("按水平标签语义处理键盘，并保留上下方向键的页面滚动", () => {
    expect(horizontalTabIndex("ArrowRight", 4, 5)).toBe(0);
    expect(horizontalTabIndex("ArrowLeft", 0, 5)).toBe(4);
    expect(horizontalTabIndex("Home", 3, 5)).toBe(0);
    expect(horizontalTabIndex("End", 1, 5)).toBe(4);
    expect(horizontalTabIndex("ArrowUp", 2, 5)).toBeNull();
    expect(horizontalTabIndex("ArrowDown", 2, 5)).toBeNull();
  });

  it("使用有界自适应延迟播放录制", () => {
    expect(playbackDelayMilliseconds(0, 0.02)).toBe(90);
    expect(playbackDelayMilliseconds(1, 2)).toBe(250);
    expect(playbackDelayMilliseconds(0, 10)).toBe(900);
  });

  it("在标题层区分 Hook 观测证据和合同复原摘要", () => {
    expect(actionEvidenceHeading(true)).toBe("Hook 观测到的工具调用");
    expect(actionEvidenceHeading(false)).toBe("按验收合同复原的动作摘要");
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
