import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { App } from "./App";

describe("展示站首屏入口", () => {
  it("只保留真实 Codex 验收作为首页演示入口", () => {
    const html = renderToStaticMarkup(<App />);

    expect(html).toContain('href="#real-codex-proof"');
    expect(html).toContain("查看真实 Codex 验收");
    expect(html).toContain("Stable v0.4.0");
    expect(html).toContain("发布验收");
    expect(html).not.toContain('href="#demo"');
    expect(html).not.toContain(">交互演示</a>");
  });
});
