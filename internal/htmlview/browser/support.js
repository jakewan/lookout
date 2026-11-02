// What every page spec shares: where the rendered pages are, and a content
// security policy check that runs after every spec, so a violation the page
// trips while being used fails as surely as one it trips while loading.
import { expect, test } from "@playwright/test";

/** @type {Record<string, string>} */
const pages = JSON.parse(process.env.LOOKOUT_PAGES ?? "{}");

/** @param {string} name */
export function pageURL(name) {
  const url = pages[name];
  if (!url) throw new Error(`no page rendered for fixture ${name}`);
  return url;
}

/**
 * The violations the page has reported. A report is dispatched as a task of
 * its own, after the action that caused it, so the read waits a frame and a
 * task first; without that, a violation from a spec's last click could land
 * after it was read.
 * @param {import("@playwright/test").Page} page
 */
export async function cspViolations(page) {
  return page.evaluate(async () => {
    await new Promise((settle) => requestAnimationFrame(() => setTimeout(settle, 0)));
    return /** @type {{cspViolations?: string[]}} */ (/** @type {unknown} */ (window)).cspViolations ?? [];
  });
}

export function watchPolicy() {
  test.beforeEach(async ({ page }) => {
    // Init scripts run outside the page's content security policy, so this
    // listener sees every violation the policy reports.
    await page.addInitScript(() => {
      /** @type {string[]} */
      const seen = [];
      Object.assign(window, { cspViolations: seen });
      document.addEventListener("securitypolicyviolation", (e) => {
        seen.push(`${e.violatedDirective} ${e.blockedURI}`);
      });
    });
  });
  test.afterEach(async ({ page, javaScriptEnabled }) => {
    // A spec that never opened a page, or opened it with script off, has
    // nothing to report and no listener to report it.
    if (javaScriptEnabled && page.url().startsWith("file:")) expect(await cspViolations(page)).toEqual([]);
  });
}

/**
 * Switches a page to its lists, where its script offers the switch; with
 * script off the lists are all there is.
 * @param {import("@playwright/test").Page} page
 */
export async function showLists(page) {
  const list = page.getByRole("group", { name: "Show the detail as" }).getByRole("button", { name: "List" });
  if (await list.isVisible()) await list.click();
}
