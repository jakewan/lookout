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
    await expect(cue(page)).toHaveText("5m ago");
    await expect(cue(page)).not.toHaveClass(/\bold\b/);
  });

  test("moves on while the page stays open, and turns old at two hours", async ({ page }) => {
    await page.clock.install({ time: read + 2 * hour - 2 * minute });
    await page.goto(pageURL("busy"));
    await expect(cue(page)).toHaveText("1h ago");
    await page.clock.runFor(3 * minute);
    await expect(cue(page)).toHaveText("2h ago, old");
    await expect(cue(page)).toHaveClass(/\bold\b/);
  });

  test("sits on the as-of line, with the note on a line below", async ({ page }) => {
    await page.clock.install({ time: read + 5 * minute });
    await page.goto(pageURL("busy"));
    const box = async (/** @type {string} */ selector) => {
      const b = await page.locator(`header ${selector}`).boundingBox();
      if (!b) throw new Error(`${selector} is not shown`);
      return b;
    };
    const asof = await box(".asof");
    const age = await box(".readage");
    const note = await box(".note");
    expect(age.y).toBeLessThan(asof.y + asof.height);
    expect(age.y + age.height).toBeGreaterThan(asof.y);
    expect(note.y).toBeGreaterThanOrEqual(asof.y + asof.height);
  });

  test("shows one time, carrying the exact read for a hover and an agent", async ({ page }) => {
    await page.goto(pageURL("busy"));
    const asof = page.locator("header time.asof");
    await expect(asof).toHaveText("as of 2030-06-15 12:00 UTC");
    await expect(asof).toHaveAttribute("datetime", "2030-06-15T12:00:00Z");
    await expect(asof).toHaveAttribute("title", "read 2030-06-15T12:00:00Z, as in view.json beside this page");
    await expect(page.locator("header")).not.toContainText("2030-06-15T12:00:00Z");
  });

  test("is shown on a page with no workstreams too", async ({ page }) => {
    await page.clock.install({ time: read + 3 * hour });
    await page.goto(pageURL("empty"));
    await expect(cue(page)).toHaveText("3h ago, old");
  });

  test("stays hidden while the read is dated after the browser's clock", async ({ page }) => {
    await page.clock.install({ time: read - hour });
    await page.goto(pageURL("busy"));
    await expect(cue(page)).toBeHidden();
    await expect(page.locator("header time.asof")).toBeVisible();
  });

  test("leaves only the as-of time with script off", async ({ browser }) => {
    const context = await browser.newContext({ javaScriptEnabled: false });
    const page = await context.newPage();
    await page.goto(pageURL("busy"));
    await expect(page.locator(".readage")).toBeHidden();
    await expect(page.locator("header time.asof")).toHaveText("as of 2030-06-15 12:00 UTC");
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
