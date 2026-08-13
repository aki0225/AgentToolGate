import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import {
  horizontalTabIndex,
  playbackDelayMilliseconds,
  RealCodexProof
} from "./RealCodexProof";

describe("真实 Codex 多场景预录面板", () => {
  it("呈现五个可访问场景标签并默认选择低摩擦场景", () => {
    const html = renderToStaticMarkup(<RealCodexProof />);

    expect(html).toContain("真实 Codex CLI · 五场景预录验收");
    expect(html).toContain('role="tablist"');
    expect(html.match(/role="tab"/g)).toHaveLength(5);
    expect(html).toContain('id="real-codex-tab-low-friction"');
    expect(html).toContain('id="real-codex-tab-sensitive-read"');
    expect(html).toContain('id="real-codex-tab-destructive-delete"');
    expect(html).toContain('id="real-codex-tab-network-egress"');
    expect(html).toContain('id="real-codex-tab-protected-write"');
    expect(html).toContain('aria-selected="true"');
    expect(html.match(/aria-controls="real-codex-panel"/g)).toHaveLength(5);
    expect(html).toContain('id="real-codex-panel"');
    expect(html).toContain("allow / low");
  });

  it("明确展示证据边界且不冒充实时或交互审批", () => {
    const html = renderToStaticMarkup(<RealCodexProof />);

    expect(html).toContain("预录，不是浏览器实时连接");
    expect(html).toContain("仅使用 synthetic 数据");
    expect(html).toContain("不包含真实凭据或 provider 身份");
    expect(html).toContain("不是 OS sandbox、EDR 或完整 DLP");
    expect(html).toContain("Codex ask 当前保守拒绝，不冒充交互审批");
    expect(html).toContain("不是模型自报");
    expect(html).toContain("仅在确有记录时展示");
    expect(html).toContain("查看公开脱敏证据");
  });

  it("播放器默认不自动播放并只显示低摩擦录制的前三条事件", () => {
    const html = renderToStaticMarkup(<RealCodexProof />);

    expect(html).toContain(">播放录制</button>");
    expect(html).toContain(">重置</button>");
    expect(html).not.toContain(">暂停</button>");
    expect(html.match(/class="real-codex-line /g)).toHaveLength(3);
    expect(html).toContain("自适应加速回放");
    expect(html).not.toContain("4×");
    expect(html).toContain("代表性写入规则");
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

  it("低摩擦首屏按证据状态呈现 Audit，不声称普通读取必有后端 Audit", () => {
    const html = renderToStaticMarkup(<RealCodexProof />);

    expect(html).toMatch(/Audit (已关联|不适用)/);
    expect(html).toContain("workspace_write");
    expect(html).toContain("代表性写入规则 agent-guard-safe-workspace-write-allow");
    expect(html).not.toContain("每段录制都与 Guard Audit");
    expect(html).not.toContain("实时演示");
    expect(html).not.toContain("交互审批已完成");
  });
});
