// How a page left open says how old its read is. The fixtures are read at
// 2030-06-15T12:00:00Z, so each spec sets the browser's clock against that.
import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { pageURL, watchPolicy } from "./support.js";

watchPolicy();

const read = new Date("2030-06-15T12:00:00Z").getTime();
const minute = 60 * 1000;
const hour = 60 * minute;

/** @param {import("@playwright/test").Page} page */
const cue = (page) => page.getByRole("status");

test.describe("the read's age", () => {
  test("says how long ago the page was read", async ({ page }) => {
    await page.clock.install({ time: read + 5 * minute + 20 * 1000 });
    await page.goto(pageURL("busy"));
    await expect(cue(page)).toHaveText("read 5m ago");
    await expect(cue(page)).not.toHaveClass(/\bold\b/);
  });

  test("moves on while the page stays open, and turns old at two hours", async ({ page }) => {
    await page.clock.install({ time: read + 2 * hour - 2 * minute });
    await page.goto(pageURL("busy"));
    await expect(cue(page)).toHaveText("read 1h ago");
    await page.clock.runFor(3 * minute);
    await expect(cue(page)).toHaveText("read 2h ago; run lookout view again, then reload, to see the latest");
    await expect(cue(page)).toHaveClass(/\bold\b/);
  });

  test("is shown on a page with no workstreams too", async ({ page }) => {
    await page.clock.install({ time: read + 3 * hour });
    await page.goto(pageURL("empty"));
    await expect(cue(page)).toHaveText(/^read 3h ago; /);
  });

  test("stays hidden while the read is dated after the browser's clock", async ({ page }) => {
    await page.clock.install({ time: read - hour });
    await page.goto(pageURL("busy"));
    await expect(cue(page)).toBeHidden();
    await expect(page.locator(".read")).toHaveText("2030-06-15T12:00:00Z");
  });

  test("leaves only the exact read with script off", async ({ browser }) => {
    const context = await browser.newContext({ javaScriptEnabled: false });
    const page = await context.newPage();
    await page.goto(pageURL("busy"));
    await expect(page.locator(".readage")).toBeHidden();
    await expect(page.locator(".read")).toHaveText("2030-06-15T12:00:00Z");
    await context.close();
  });

  for (const colorScheme of /** @type {const} */ (["light", "dark"])) {
    test(`passes an accessibility audit once old, in its ${colorScheme} colors`, async ({ page }) => {
      await page.emulateMedia({ colorScheme });
      await page.clock.install({ time: read + 3 * hour });
      await page.goto(pageURL("busy"));
      await expect(cue(page)).toBeVisible();
      const results = await new AxeBuilder({ page }).include(".readage").analyze();
      expect(results.violations.map((v) => v.id)).toEqual([]);
    });
  }
});
