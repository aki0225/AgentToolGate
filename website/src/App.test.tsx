import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { App } from "./App";

describe("展示站首屏入口", () => {
  it("优先进入真实 Codex 验收，同时保留 synthetic 交互演示入口", () => {
    const html = renderToStaticMarkup(<App />);

    expect(html).toContain('href="#real-codex-proof"');
    expect(html).toContain("查看真实 Codex 验收");
    expect(html).toContain('href="#demo"');
    expect(html).toContain(">交互演示</a>");
    expect(html).not.toContain(">查看交互演示");
  });
});
