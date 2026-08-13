import { mkdirSync } from "node:fs";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";

const args = process.argv.slice(2);

if (args.length === 0) {
  console.error("用法: node ./scripts/run-playwright.mjs <playwright 参数>");
  process.exit(1);
}

if (process.platform === "win32" && !process.env.PLAYWRIGHT_BROWSERS_PATH?.trim()) {
  const browserPath = fileURLToPath(new URL("../.playwright-browsers", import.meta.url));
  mkdirSync(browserPath, { recursive: true });
  process.env.PLAYWRIGHT_BROWSERS_PATH = browserPath;
}

const playwrightCli = fileURLToPath(new URL("../node_modules/playwright/cli.js", import.meta.url));
const child = spawn(process.execPath, [playwrightCli, ...args], {
  env: process.env,
  stdio: "inherit",
  shell: false
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
