import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { App } from "./App";

describe("展示站首屏入口", () => {
  it("只保留真实 Codex 验收作为首页演示入口", () => {
    const html = renderToStaticMarkup(<App />);

    expect(html).toContain('href="#real-codex-proof"');
    expect(html).toContain("查看真实 Codex 验收");
    expect(html).toContain("Stable v0.4.2");
    expect(html).toContain("/docs/v0.4.2-release-acceptance.md");
    expect(html).toContain("/releases/tag/v0.4.2");
    expect(html).toContain("v0.4.1 发布验收");
    expect(html).toContain("v0.3.2 / 0126bc2");
    expect(html).toContain("CI #31954428232");
    expect(html).toContain("v0.4.1 Release 评估证据");
    expect(html).toContain(
      "/evaluation/published/agent-safety/releases/v0.4.1/proof.json",
    );
    expect(html).toContain("评估方法与历史快照");
    expect(html).toContain("Windows <strong>30 passed</strong>");
    expect(html).toContain("26 passed · 4 skipped");
    expect(html).toContain("良性中断 W/L");
    expect(html).toContain("AUTH_MODE=local");
    expect(html).toContain("DEV_MODE");
    expect(html.indexOf("init codex")).toBeLessThan(html.indexOf("up --open"));
    expect(html.indexOf("up --open")).toBeLessThan(html.indexOf("doctor --dir ."));
    expect(html).not.toContain('href="#demo"');
    expect(html).not.toContain(">交互演示</a>");
  });
});
