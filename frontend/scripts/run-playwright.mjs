import { mkdirSync } from "node:fs";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";

const args = process.argv.slice(2);

if (args.length === 0) {
  console.error("用法: node ./scripts/run-playwright.mjs <playwright 参数>");
  process.exit(1);
}

if (process.platform === "win32" && !process.env.PLAYWRIGHT_BROWSERS_PATH?.trim()) {
  const browserPathCandidates = [
    "E:\\tools\\playwright-browsers",
    "F:\\tools\\playwright-browsers",
    fileURLToPath(new URL("../.playwright-browsers", import.meta.url)),
  ];

  for (const browserPath of browserPathCandidates) {
    try {
      mkdirSync(browserPath, { recursive: true });
      process.env.PLAYWRIGHT_BROWSERS_PATH = browserPath;
      break;
    } catch {
      // 某些本地 Windows 环境会挂载不可写盘符；继续尝试下一个候选目录。
    }
  }

  if (!process.env.PLAYWRIGHT_BROWSERS_PATH?.trim()) {
    throw new Error("未找到可写的 Playwright 浏览器缓存目录，请设置 PLAYWRIGHT_BROWSERS_PATH。");
  }
}

const playwrightCli = fileURLToPath(new URL("../node_modules/playwright/cli.js", import.meta.url));
const child = spawn(process.execPath, [playwrightCli, ...args], {
  env: process.env,
  stdio: "inherit",
  shell: false,
});

child.on("error", (error) => {
  console.error(error);
  process.exit(1);
});

child.on("exit", (code, signal) => {
  if (signal) {
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code ?? 0);
});
