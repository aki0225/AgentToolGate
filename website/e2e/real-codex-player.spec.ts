import { expect, test, type Page } from "@playwright/test";

const proofSelector = "#real-codex-proof";
const eventSelector = ".real-codex-line";

async function openProof(page: Page) {
  await page.goto("./#real-codex-proof");
  await expect(page.locator(proofSelector)).toBeVisible();
}

async function openRawRecording(page: Page) {
  const rawProof = page.locator(".real-codex-raw-proof");
  await rawProof.getByText("查看同步录制与验收日志").click();
  await expect(rawProof).toHaveAttribute("open", "");
  return rawProof;
}

test.describe("真实 Codex 五场景播放器", () => {
  test("默认展示破坏性删除的命令、拒绝理由和未发生后果", async ({ page }) => {
    await openProof(page);

    const panel = page.locator("#real-codex-panel");
    await expect(page.getByRole("tab", { name: /破坏性删除/ })).toHaveAttribute(
      "aria-selected",
      "true"
    );
    await expect(panel.getByRole("list", { name: "破坏性删除动作链" })).toContainText(
      "$ Remove-Item -Recurse ."
    );
    await expect(panel).toContainText("执行前拒绝");
    await expect(panel).toContainText("会破坏整个工作区");
    await expect(panel).toContainText("动作未执行");
    await expect(panel).toContainText("仓库根目录、sentinel、HEAD、tree");
    await expect(panel).toContainText("历史验收合同复原");
    await expect(panel.locator(".real-codex-raw-proof")).not.toHaveAttribute("open", "");
  });

  test("敏感读取明确展示读取命令与私钥泄露风险", async ({ page }) => {
    await openProof(page);

    await page.getByRole("tab", { name: /敏感读取/ }).click();
    const panel = page.locator("#real-codex-panel");
    await expect(panel).toContainText("$ Get-Content .ssh/id_rsa");
    await expect(panel).toContainText("凭据与身份材料");
    await expect(panel).toContainText("私钥泄露");
    await expect(panel).toContainText("公开产物不含 synthetic 值");
  });

  test("水平键切换标签，垂直键保留默认行为", async ({ page }) => {
    await openProof(page);

    const tablist = page.getByRole("tablist", { name: "真实 Codex 验收场景" });
    const tabs = tablist.getByRole("tab");
    await expect(tabs).toHaveCount(5);

    const firstTab = tabs.nth(0);
    const secondTab = tabs.nth(1);
    const lastTab = tabs.nth(4);

    await firstTab.focus();
    await firstTab.press("ArrowRight");
    await expect(secondTab).toBeFocused();
    await expect(secondTab).toHaveAttribute("aria-selected", "true");

    await secondTab.press("ArrowLeft");
    await expect(firstTab).toBeFocused();
    await expect(firstTab).toHaveAttribute("aria-selected", "true");

    await firstTab.press("End");
    await expect(lastTab).toBeFocused();
    await expect(lastTab).toHaveAttribute("aria-selected", "true");

    await lastTab.press("Home");
    await expect(firstTab).toBeFocused();
    await expect(firstTab).toHaveAttribute("aria-selected", "true");

    for (const key of ["ArrowUp", "ArrowDown"]) {
      const prevented = await firstTab.evaluate((element, pressedKey) => {
        const event = new KeyboardEvent("keydown", {
          key: pressedKey,
          bubbles: true,
          cancelable: true
        });
        element.dispatchEvent(event);
        return event.defaultPrevented;
      }, key);
      expect(prevented).toBe(false);
      await expect(firstTab).toHaveAttribute("aria-selected", "true");
    }
  });

  test("播放、暂停和重置保持稳定状态", async ({ page }) => {
    await openProof(page);

    await page.getByRole("tab", { name: /低摩擦开发/ }).click();
    const panel = page.locator("#real-codex-panel");
    await openRawRecording(page);
    const events = panel.locator(eventSelector);
    await expect(events).toHaveCount(3);

    await panel.getByRole("button", { name: "播放录制" }).click();
    await expect(panel.getByRole("button", { name: "暂停" })).toBeVisible();
    await expect.poll(() => events.count()).toBeGreaterThan(3);

    await panel.getByRole("button", { name: "暂停" }).click();
    const pausedCount = await events.count();
    await page.waitForTimeout(1_100);
    await expect(events).toHaveCount(pausedCount);

    await panel.getByRole("button", { name: "重置" }).click();
    await expect(events).toHaveCount(3);
    await expect(panel.getByRole("button", { name: "播放录制" })).toBeVisible();
  });

  test("短场景完成后可以重新播放", async ({ page }) => {
    await openProof(page);

    await page.getByRole("tab", { name: /敏感读取/ }).click();
    const panel = page.locator("#real-codex-panel");
    await openRawRecording(page);
    const events = panel.locator(eventSelector);

    await expect(events).toHaveCount(3);
    await panel.getByRole("button", { name: "播放录制" }).click();
    await expect(panel.getByRole("button", { name: "重新播放" })).toBeVisible({
      timeout: 5_000
    });
    const completedCount = await events.count();
    expect(completedCount).toBeGreaterThan(3);

    await panel.getByRole("button", { name: "重新播放" }).click();
    await expect(panel.getByRole("button", { name: "暂停" })).toBeVisible();
    await expect.poll(() => events.count()).toBeLessThan(completedCount);
  });

  test("三种视口下页面没有横向溢出", async ({ page }) => {
    for (const viewport of [
      { width: 1440, height: 900 },
      { width: 760, height: 900 },
      { width: 375, height: 812 }
    ]) {
      await page.setViewportSize(viewport);
      await openProof(page);
      const dimensions = await page.evaluate(() => ({
        clientWidth: document.documentElement.clientWidth,
        scrollWidth: document.documentElement.scrollWidth
      }));
      expect(dimensions.scrollWidth).toBeLessThanOrEqual(dimensions.clientWidth);
    }
  });
});

test.describe("减少动态效果", () => {
  test("直接展开完整记录且隐藏播放控件", async ({ page }) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    await openProof(page);

    const panel = page.locator("#real-codex-panel");
    await openRawRecording(page);
    await expect(panel.getByRole("status")).toContainText("已展开完整记录");
    await expect.poll(() => panel.locator(eventSelector).count()).toBeGreaterThan(3);
    await expect(panel.locator(".real-codex-cursor")).toHaveCount(0);
    await expect(panel.getByRole("button", { name: "播放录制" })).toHaveCount(0);
    await expect(panel.getByRole("button", { name: "重置" })).toHaveCount(0);
  });
});
