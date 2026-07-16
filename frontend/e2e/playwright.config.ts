import { defineConfig, devices } from "@playwright/test";

const baseURL = process.env.E2E_BASE_URL?.trim() || "http://127.0.0.1:5173";
const browserChannel = process.env.E2E_BROWSER_CHANNEL?.trim() || undefined;
const videoMode = process.env.E2E_VIDEO?.trim() || (process.env.CI ? "retain-on-failure" : "off");

export default defineConfig({
  testDir: ".",
  timeout: 120_000,
  expect: {
    timeout: 10_000,
  },
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  workers: 1,
  outputDir: "../test-results",
  reporter: process.env.CI ? [["list"], ["html", { open: "never", outputFolder: "../playwright-report" }]] : "list",
  use: {
    baseURL,
    trace: "on-first-retry",
    screenshot: "only-on-failure",
    video: videoMode === "off" ? "off" : "retain-on-failure",
    viewport: { width: 1440, height: 1200 },
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"], channel: browserChannel },
    },
  ],
});
