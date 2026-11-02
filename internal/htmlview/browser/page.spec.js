// How the page lookout view writes behaves in a browser, from the reader's side.
import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

/** @type {Record<string, string>} */
const pages = JSON.parse(process.env.LOOKOUT_PAGES ?? "{}");

/** @param {string} name */
function pageURL(name) {
  const url = pages[name];
  if (!url) throw new Error(`no page rendered for fixture ${name}`);
  return url;
}

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

test.describe("the overview", () => {
  test("lists every workstream in stated order, by title with its id beside it, or by its id", async ({ page }) => {
    await page.goto(pageURL("busy"));
    const lanes = page.getByRole("region", { name: "every workstream, in stated order" }).getByRole("link");
    await expect(lanes).toHaveText([
      "!search-revamp",
      "Billing export to the ledger billing-export",
      "!release-train",
      "infra-upgrade",
      "docs-site",
      "!onboarding",
    ]);
  });

  test("names each workstream's cue in words, not by color alone", async ({ page }) => {
    await page.goto(pageURL("busy"));
    const overview = page.getByRole("region", { name: "every workstream, in stated order" });
    const row = (/** @type {string} */ label) =>
      overview.locator(".ovrow").filter({ has: page.getByRole("link", { name: label }) });
    await expect(row("docs-site")).toContainText("stale");
    await expect(row("onboarding")).toContainText("unread");
    await expect(row("release-train")).toContainText("behind its rank");
    await expect(row("infra-upgrade")).not.toContainText("behind its rank");
  });

  test("marks a workstream drawing on a failing input, and says why on hover", async ({ page }) => {
    await page.goto(pageURL("busy"));
    const link = page.getByRole("link", { name: "search-revamp" });
    await expect(link.getByTitle("draws on a failing input")).toHaveText("!");
  });

  test("opens a workstream's detail from its lane", async ({ page }) => {
    await page.goto(pageURL("busy"));
    await page.getByRole("link", { name: "Billing export to the ledger billing-export" }).click();
    await expect(page).toHaveURL(/#ws-2$/);
    await expect(page.locator("#ws-2")).toBeInViewport();
  });
});

test.describe("inputs", () => {
  test("name a failing input with its reasons, and leave healthy ones quiet", async ({ page }) => {
    await page.goto(pageURL("busy"));
    await expect(page.getByText("tracker is degraded: stale, missing_fields")).toBeVisible();
    await expect(page.getByText("chat is error: missing")).toBeVisible();
    await expect(page.getByText("code is ok")).toHaveCount(0);
    await expect(page.getByText("calendar")).toHaveCount(0);
  });
});

test.describe("a workstream's detail", () => {
  test("starts closed, and Tab from the top of the page reaches it and Enter opens it", async ({ page }) => {
    await page.goto(pageURL("busy"));
    const detail = page.locator("#ws-1 details");
    await expect(detail).not.toHaveAttribute("open");
    const summary = detail.locator("summary");
    // Past the overview's lanes, the first workstream's summary is the next
    // stop; the bound only keeps a regression from looping forever.
    for (let stops = 0; stops < 20 && !(await summary.evaluate((s) => s === document.activeElement)); stops++) {
      await page.keyboard.press("Tab");
    }
    await expect(summary).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(detail).toHaveAttribute("open");
  });
});

test("an empty setting says it has no workstreams", async ({ page }) => {
  await page.goto(pageURL("empty"));
  await expect(page.getByText("No workstreams.")).toBeVisible();
});

for (const name of ["busy", "empty"]) {
  test.describe(`the ${name} page`, () => {
    test("requests nothing beyond its own file", async ({ page }) => {
      /** @type {string[]} */
      const requests = [];
      page.on("request", (r) => {
        if (r.url() !== pageURL(name)) requests.push(r.url());
      });
      await page.goto(pageURL(name));
      await page.waitForLoadState("networkidle");
      expect(requests).toEqual([]);
    });

    test("trips its content security policy nowhere", async ({ page }) => {
      await page.goto(pageURL(name));
      await page.waitForLoadState("networkidle");
      const seen = await page.evaluate(
        () => /** @type {{cspViolations: string[]}} */ (/** @type {unknown} */ (window)).cspViolations,
      );
      expect(seen).toEqual([]);
    });

    for (const colorScheme of /** @type {const} */ (["light", "dark"])) {
      test(`passes an accessibility audit in its ${colorScheme} colors`, async ({ page }) => {
        await page.emulateMedia({ colorScheme });
        await page.goto(pageURL(name));
        // axe skips what is hidden, so every workstream's detail is opened
        // and its items audited too.
        for (const summary of await page.locator("summary").all()) await summary.click();
        await expect(page.locator("details:not([open])")).toHaveCount(0);
        const results = await new AxeBuilder({ page }).analyze();
        const found = results.violations.flatMap((v) =>
          v.nodes.map((n) => ({ rule: v.id, target: n.target.join(" "), fg: n.any[0]?.data?.fgColor })),
        );
        expect(found).toEqual([]);
      });
    }
  });
}
