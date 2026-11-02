// The items no workstream holds, as the page lists them for a reader
// reconciling their workstreams against what is moving.
import { expect, test } from "@playwright/test";
import { pageURL, showLists, watchPolicy } from "./support.js";

watchPolicy();

/** @param {import("@playwright/test").Page} page */
const unplaced = (page) => page.getByRole("region", { name: "Unplaced" });

// Newest touch first, each named by the token lookout adopt and dismiss take:
// one holding a character that does not print is quoted, as lookout unplaced
// quotes it.
const tokens = [
  "sessions:owner/tools:main",
  '"code:owner/app:fix/\\u200bcache"',
  "code:owner/app:spike/cache",
  "hub:owner/app#60",
];

test.describe("the unplaced items", () => {
  test("list every one, newest touch first, by the token adopt takes", async ({ page }) => {
    await page.goto(pageURL("busy"));
    const section = unplaced(page);
    await expect(section.locator("summary")).toContainText("4 unplaced");
    await section.locator("summary").click();
    await expect(section.locator("li .id")).toHaveText(tokens);
    await expect(section.locator("li").nth(0)).toContainText("30m ago");
    await expect(section.locator("li").nth(1)).toContainText("Cache the build");
    await expect(section.locator("li").nth(3)).toContainText("Bump the linter");
    await expect(section.locator("li").nth(3)).toContainText("8h ago");
  });

  test("start closed, and Enter on their summary opens them", async ({ page }) => {
    await page.goto(pageURL("busy"));
    const detail = unplaced(page).locator("details");
    await expect(detail).not.toHaveAttribute("open");
    await unplaced(page).locator("summary").focus();
    await page.keyboard.press("Enter");
    await expect(detail).toHaveAttribute("open");
  });

  test("show beside the map and beside the lists alike", async ({ page }) => {
    await page.goto(pageURL("busy"));
    await expect(unplaced(page)).toBeVisible();
    await showLists(page);
    await expect(unplaced(page)).toBeVisible();
  });

  test("open a web link in a new tab, and leave any other address unlinked", async ({ page }) => {
    await page.goto(pageURL("busy"));
    await unplaced(page).locator("summary").click();
    const links = unplaced(page).getByRole("link");
    await expect(links).toHaveCount(1);
    await expect(links).toHaveAttribute("href", "https://example.com/owner/app/pull/60");
    await expect(links).toHaveAttribute("target", "_blank");
    await expect(links).toHaveAttribute("rel", "noreferrer noopener");
  });

  test("are not shown where nothing is unplaced", async ({ page }) => {
    await page.goto(pageURL("map"));
    await expect(unplaced(page)).toHaveCount(0);
  });
});

test.describe("with script off", () => {
  test.use({ javaScriptEnabled: false });

  test("the unplaced items still show", async ({ page }) => {
    await page.goto(pageURL("busy"));
    await expect(unplaced(page)).toBeVisible();
  });
});
