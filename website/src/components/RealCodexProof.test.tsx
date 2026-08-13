import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { RealCodexProof } from "./RealCodexProof";

describe("真实 Codex 预录面板", () => {
  it("明确区分预录、synthetic 与安全边界", () => {
    const html = renderToStaticMarkup(<RealCodexProof />);

    expect(html).toContain("真实 Codex CLI · 预录验收");
    expect(html).toContain("预录，不是浏览器实时连接");
    expect(html).toContain("synthetic fixture");
    expect(html).toContain("不是 OS sandbox、EDR 或完整 DLP");
    expect(html).toContain("mock.real_codex_echo");
    expect(html).toContain("project_protected_path");
    expect(html).toContain("独立后置检查");
    expect(html).toContain("仓库状态保持不变");
    expect(html).toContain("查看 8 个脱敏证据文件");
  });

  it("播放器按钮具有明确文本并默认不自动播放", () => {
    const html = renderToStaticMarkup(<RealCodexProof />);

    expect(html).toContain(">播放录制</button>");
    expect(html).toContain(">重置</button>");
    expect(html).not.toContain(">暂停</button>");
  });
});
