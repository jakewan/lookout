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
const quoted = '"code:owner/app:fix/\\u200bcache"';
const tokens = ["sessions:owner/tools:main", quoted, "code:owner/app:spike/cache", "hub:owner/app#60"];

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

  test("say a quoted token goes to the shell in single quotes, and not a plain one", async ({ page }) => {
    await page.goto(pageURL("busy"));
    await unplaced(page).locator("summary").click();
    const hinted = unplaced(page).getByTitle("pass it to the shell in single quotes");
    await expect(hinted).toHaveCount(1);
    await expect(hinted).toHaveText(quoted);
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

  test("show on a setting with no workstreams yet, where everything is unplaced", async ({ page }) => {
    await page.goto(pageURL("empty"));
    await expect(page.getByText("No workstreams.")).toBeVisible();
    await unplaced(page).locator("summary").click();
    await expect(unplaced(page).locator("li .id")).toHaveText(["code:owner/app:spike/first", "tracker:two spaces"]);
  });

  test("show a token's spaces as written, and say one holding a space goes to the shell in single quotes", async ({
    page,
  }) => {
    await page.goto(pageURL("empty"));
    await unplaced(page).locator("summary").click();
    // toHaveText collapses whitespace, so the rendered text is read as is.
    const spaced = unplaced(page).locator("li .id").nth(1);
    // biome-ignore lint/performance/useDomNodeTextContent: the rendered text is the point; textContent ignores the CSS that collapses or keeps spaces.
    expect(await spaced.innerText()).toBe("tracker:two  spaces");
    await expect(spaced).toHaveAttribute("title", "pass it to the shell in single quotes");
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
