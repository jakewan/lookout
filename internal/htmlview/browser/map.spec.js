// How the page's map of each workstream's items behaves, from the reader's
// side, over a readout holding every shape the map has to place.
import { expect, test } from "@playwright/test";
import { pageURL, showLists, watchPolicy } from "./support.js";

watchPolicy();

/** @param {import("@playwright/test").Page} page */
const tree = (page) => page.getByRole("tree", { name: "map of every workstream's items" });

/**
 * A node by its handle: a workstream's id, or an item's <input>:<id>.
 * @param {import("@playwright/test").Page} page
 * @param {string} handle
 */
const node = (page, handle) => page.locator(`[role="treeitem"][data-handle="${handle}"]`);

/**
 * The nodes directly beneath a node, given by its handle, in order.
 * @param {import("@playwright/test").Page} page
 * @param {string} handle
 */
const children = (page, handle) => page.locator(`[role="treeitem"][data-parent="${handle}"]`);

/** @param {import("@playwright/test").Page} page */
const hubOf = (page) => tree(page).locator('[role="treeitem"][aria-level="1"]');

/**
 * The handles of the nodes a locator finds, in order.
 * @param {import("@playwright/test").Locator} nodes
 */
const handles = (nodes) => nodes.evaluateAll((els) => els.map((e) => e.getAttribute("data-handle")));

/**
 * A cross-link, by the handles at its ends.
 * @param {import("@playwright/test").Page} page
 * @param {string} from
 * @param {string} to
 */
const crossLink = (page, from, to) => page.locator(`svg .xlink[data-from="${from}"][data-to="${to}"]`);

test.describe("the map", () => {
  test("shows by default, and its switch reaches the lists", async ({ page }) => {
    await page.goto(pageURL("map"));
    await expect(tree(page)).toBeVisible();
    await expect(page.locator("#ws-1")).toBeHidden();
    await showLists(page);
    await expect(tree(page)).toBeHidden();
    await expect(page.locator("#ws-1")).toBeVisible();
    await page.getByRole("button", { name: "Map" }).click();
    await expect(tree(page)).toBeVisible();
  });

  test("centers on the setting, with every workstream around it in stated order", async ({ page }) => {
    await page.goto(pageURL("map"));
    await expect(hubOf(page)).toHaveCount(1);
    await expect.poll(() => handles(children(page, ""))).toEqual(["platform", "billing", "ops", "docs", "intake"]);
  });

  test("names each workstream's cues in words and marks each flagged one by shape", async ({ page }) => {
    await page.goto(pageURL("map"));
    await expect(node(page, "platform")).toHaveAccessibleName(/^Platform rework platform.*draws on a failing input/);
    await expect(node(page, "ops")).toHaveAccessibleName(/stale.*behind its rank/);
    await expect(node(page, "intake")).toHaveAccessibleName(/nothing read/);
    for (const flagged of ["platform", "ops", "intake"]) {
      await expect(node(page, flagged).locator(":scope > .node .flag")).not.toHaveText("");
    }
    await expect(node(page, "docs").locator(":scope > .node .flag")).toHaveCount(0);
  });
});

/**
 * Where a node's label sits on the page.
 * @param {import("@playwright/test").Page} page
 * @param {string} handle
 */
const box = async (page, handle) => {
  const b = await node(page, handle).locator(":scope > .node").boundingBox();
  if (!b) throw new Error(`${handle} is not drawn`);
  return { x: b.x + b.width / 2, y: b.y + b.height / 2 };
};

test.describe("the map's drawing", () => {
  test("spreads sideways from the center: down the right in stated order, then back up the left", async ({ page }) => {
    await page.goto(pageURL("map"));
    const hub = await box(page, "");
    const [platform, billing, ops, docs, intake] = await Promise.all(
      ["platform", "billing", "ops", "docs", "intake"].map((h) => box(page, h)),
    );
    if (!platform || !billing || !ops || !docs || !intake) throw new Error("a workstream is not drawn");
    for (const right of [platform, billing, ops]) expect(right.x).toBeGreaterThan(hub.x);
    for (const left of [docs, intake]) expect(left.x).toBeLessThan(hub.x);
    expect(platform.y).toBeLessThan(billing.y);
    expect(billing.y).toBeLessThan(ops.y);
    expect(docs.y).toBeGreaterThan(intake.y);
    // Each level steps outward, as a hand-drawn map's branches do.
    const [p1, p2, p3] = await Promise.all(["tracker:P-1", "tracker:P-2", "tracker:P-3"].map((h) => box(page, h)));
    if (!p1 || !p2 || !p3) throw new Error("an item is not drawn");
    expect(p1.x).toBeGreaterThan(platform.x);
    expect(p2.x).toBeGreaterThan(p1.x);
    expect(p3.x).toBeGreaterThan(p2.x);
    const guides = await box(page, "code:owner/docs:guides");
    expect(guides.x).toBeLessThan(docs.x);
  });

  test("gives each branch the loudness of its workstream's cue", async ({ page }) => {
    await page.goto(pageURL("map"));
    for (const [handle, tone] of [
      ["platform", "fault"],
      ["tracker:P-3", "fault"],
      ["ops", "behind"],
      ["intake", "unread"],
      ["docs", ""],
    ]) {
      await expect(node(page, handle ?? "")).toHaveAttribute("data-tone", tone ?? "");
    }
  });
});

test.describe("placing items", () => {
  test("hangs a join under its member, and a branch under the pull request it came through", async ({ page }) => {
    await page.goto(pageURL("map"));
    await expect.poll(() => handles(children(page, "tracker:P-2"))).toEqual(["tracker:P-3", "hub:owner/app#10"]);
    await expect.poll(() => handles(children(page, "hub:owner/app#10"))).toEqual(["code:owner/app:feature/split"]);
  });

  test("hangs a member under its parent in the same workstream, and leaves one whose parent is absent at the top", async ({
    page,
  }) => {
    await page.goto(pageURL("map"));
    await expect
      .poll(() => handles(children(page, "platform")))
      .toEqual(["tracker:P-1", "tracker:P-9", "tracker:C-1", "tracker:J-1"]);
    await expect.poll(() => handles(children(page, "tracker:P-1"))).toEqual(["tracker:P-2"]);
  });

  test("breaks a cycle at its earliest node and draws the edge it gave up as a cross-link", async ({ page }) => {
    await page.goto(pageURL("map"));
    await expect.poll(() => handles(children(page, "tracker:C-1"))).toEqual(["tracker:C-2"]);
    await expect(crossLink(page, "tracker:C-1", "tracker:C-2")).toHaveAttribute("data-kind", "parent");
    // A member whose parent joined through it: the join hangs under the
    // member, and the parent edge is drawn instead.
    await expect.poll(() => handles(children(page, "tracker:J-1"))).toEqual(["tracker:J-0"]);
    await expect(crossLink(page, "tracker:J-1", "tracker:J-0")).toHaveAttribute("data-kind", "parent");
  });

  test("draws a shared item once, where it is a member, linked to what it joined through in each other holder", async ({
    page,
  }) => {
    await page.goto(pageURL("map"));
    await expect(tree(page).locator('[data-handle="hub:owner/app#20"]')).toHaveCount(1);
    await expect(node(page, "hub:owner/app#20")).toHaveAttribute("data-parent", "billing");
    await expect(page.locator('svg .xlink[data-from="hub:owner/app#20"]')).toHaveCount(2);
    await expect(crossLink(page, "hub:owner/app#20", "tracker:P-1")).toHaveAttribute("data-kind", "joined");
    await expect(crossLink(page, "hub:owner/app#20", "tracker:O-1")).toHaveAttribute("data-kind", "joined");
  });

  test("hangs what joined through a shared item, where that item is not drawn, under the item's own place", async ({
    page,
  }) => {
    await page.goto(pageURL("map"));
    await expect.poll(() => handles(children(page, "tracker:O-1"))).toEqual(["code:owner/app:feature/rollout"]);
  });

  test("links a child to a parent another workstream holds, and keeps a joined child's parent", async ({ page }) => {
    await page.goto(pageURL("map"));
    await expect(crossLink(page, "tracker:B-1", "tracker:P-1")).toHaveAttribute("data-kind", "parent");
    await expect.poll(() => handles(children(page, "hub:owner/app#20"))).toEqual(["tracker:B-3"]);
    await expect(crossLink(page, "tracker:B-3", "tracker:B-1")).toHaveAttribute("data-kind", "parent");
  });

  test("shows a member nothing read, and a link of a kind it does not know as given", async ({ page }) => {
    await page.goto(pageURL("map"));
    await expect(node(page, "hub:owner/app#21")).toHaveAccessibleName(/not read/);
    await expect(node(page, "tracker:I-1")).toHaveAccessibleName(/not read/);
    const blocks = crossLink(page, "tracker:O-1", "platform");
    await expect(blocks).toHaveAttribute("data-kind", "blocks");
  });
});

test.describe("using the map", () => {
  test("opens a selected item's link in a new tab, leaving the map where it was", async ({ page }) => {
    await page.goto(pageURL("map"));
    await node(page, "tracker:P-1").locator(":scope > .node").click();
    const link = page.getByRole("region", { name: "selected node" }).getByRole("link");
    await expect(link).toHaveAttribute("href", "https://tracker.example.com/P-1");
    await expect(link).toHaveAttribute("target", "_blank");
    await expect(link).toHaveAttribute("rel", "noreferrer noopener");
  });

  test("shows a selected node's handle and copies it exactly", async ({ page }) => {
    await page.addInitScript(() => {
      /** @type {string[]} */
      const copied = [];
      Object.assign(window, { copied });
      Object.defineProperty(navigator, "clipboard", {
        value: { writeText: async (/** @type {string} */ s) => void copied.push(s) },
      });
    });
    await page.goto(pageURL("map"));
    await node(page, "tracker:B-\u200b2").locator(":scope > .node").click();
    const panel = page.getByRole("region", { name: "selected node" });
    await expect(panel).toContainText("contains hidden characters");
    await panel.getByRole("button", { name: "Copy" }).click();
    const copied = await page.evaluate(
      () => /** @type {{copied: string[]}} */ (/** @type {unknown} */ (window)).copied,
    );
    expect(copied).toEqual(["tracker:B-\u200b2"]);
  });

  test("lists a selected node's cross-links and draws them at full strength", async ({ page }) => {
    await page.goto(pageURL("map"));
    await expect(crossLink(page, "hub:owner/app#20", "tracker:O-1")).not.toHaveClass(/strong/);
    await node(page, "tracker:O-1").locator(":scope > .node").click();
    await expect(crossLink(page, "hub:owner/app#20", "tracker:O-1")).toHaveClass(/strong/);
    await expect(page.getByRole("region", { name: "selected node" })).toContainText("joined with hub:owner/app#20");
  });

  test("selects a workstream each time its lane is followed, not only the first", async ({ page }) => {
    await page.goto(pageURL("map"));
    const lane = page
      .getByRole("region", { name: "every workstream, in stated order" })
      .getByRole("link", { name: "billing" });
    await lane.click();
    await expect(node(page, "billing")).toHaveAttribute("aria-selected", "true");
    await node(page, "docs").locator(":scope > .node").click();
    await expect(node(page, "billing")).toHaveAttribute("aria-selected", "false");
    await lane.click();
    await expect(node(page, "billing")).toHaveAttribute("aria-selected", "true");
    await expect(node(page, "billing")).toBeFocused();
  });

  test("keeps a collapsed node's rollup in view however long its label", async ({ page }) => {
    await page.goto(pageURL("busy"));
    await node(page, "billing-export").locator(":scope > .node").click();
    await page.getByRole("button", { name: "Collapse" }).click();
    const box = await node(page, "billing-export").locator(":scope > .node").boundingBox();
    const rollup = await node(page, "billing-export").locator(":scope > .node .rollup").boundingBox();
    if (!box || !rollup) throw new Error("the collapsed node or its rollup is not drawn");
    expect(rollup.width).toBeGreaterThan(0);
    expect(rollup.x + rollup.width).toBeLessThanOrEqual(box.x + box.width + 0.5);
    await expect(page.getByRole("region", { name: "selected node" })).toContainText("3 items, 1 not read");
  });

  test("makes a node the hub and finds the way back", async ({ page }) => {
    await page.goto(pageURL("map"));
    await node(page, "tracker:P-2").locator(":scope > .node").click();
    await page.getByRole("button", { name: "Make hub" }).click();
    await expect(hubOf(page)).toHaveAttribute("data-handle", "tracker:P-2");
    const crumbs = page.getByRole("navigation", { name: "map hub" });
    await expect(crumbs.getByRole("button")).toHaveText(["example", "Platform rework", "Platform rework"]);
    await crumbs.getByRole("button", { name: "example" }).click();
    await expect(hubOf(page)).toHaveAttribute("data-handle", "");
  });

  test("collapses a node, saying how much it holds", async ({ page }) => {
    await page.goto(pageURL("map"));
    await node(page, "tracker:P-2").locator(":scope > .node").click();
    await page.getByRole("button", { name: "Collapse" }).click();
    await expect(node(page, "tracker:P-3")).toBeHidden();
    await expect(node(page, "tracker:P-2")).toHaveAttribute("aria-expanded", "false");
    await expect(node(page, "tracker:P-2").locator(":scope > .node")).toContainText("3 items");
  });

  test("folded all the way in, still says what is flagged, as loud as the loudest", async ({ page }) => {
    await page.goto(pageURL("map"));
    await page.getByRole("button", { name: "Collapse" }).click();
    const rollup = node(page, "").locator(":scope > .node .rollup");
    await expect(rollup).toHaveText("5 workstreams: 1 on a failing input, 2 behind their rank, 1 with nothing read");
    await expect(rollup).toHaveClass(/tone-fault/);
  });

  test("focuses a workstream when its lane is followed", async ({ page }) => {
    await page.goto(pageURL("map"));
    await page
      .getByRole("region", { name: "every workstream, in stated order" })
      .getByRole("link", { name: "billing" })
      .click();
    await expect(node(page, "billing")).toHaveAttribute("aria-selected", "true");
    await expect(node(page, "billing")).toBeFocused();
  });

  test("takes one Tab stop, and arrow keys move through it", async ({ page }) => {
    await page.goto(pageURL("map"));
    await expect(tree(page).locator('[role="treeitem"][tabindex="0"]')).toHaveCount(1);
    await page.getByRole("button", { name: "List" }).focus();
    await page.keyboard.press("Tab");
    await expect(node(page, "")).toBeFocused();
    await page.keyboard.press("ArrowDown");
    await expect(node(page, "platform")).toBeFocused();
    // As in any tree view: left closes an open node, then steps out of it.
    await page.keyboard.press("ArrowLeft");
    await expect(node(page, "platform")).toHaveAttribute("aria-expanded", "false");
    await page.keyboard.press("ArrowLeft");
    await expect(node(page, "")).toBeFocused();
  });
});

test.describe("a reader who must be able to trust it", () => {
  test("names the exact read it was drawn from", async ({ page }) => {
    await page.goto(pageURL("map"));
    // Exactly in the as-of time's tooltip, and to the millisecond in its
    // datetime, which HTML's grammar caps at three fractional digits.
    const asof = page.locator("header time.asof");
    await expect(asof).toHaveAttribute(
      "title",
      "read 2030-06-15T12:00:00.123456789Z, as in view.json beside this page",
    );
    await expect(asof).toHaveAttribute("datetime", "2030-06-15T12:00:00.123Z");
  });

  test("keeps today's lists where its script fails", async ({ page }) => {
    await page.addInitScript(() => {
      JSON.parse = () => {
        throw new Error("broken on purpose");
      };
    });
    await page.goto(pageURL("map"));
    await expect(page.locator("#ws-1")).toBeVisible();
    await expect(page.getByRole("group", { name: "Show the detail as" })).toBeHidden();
  });
});

test.describe("with script off", () => {
  test.use({ javaScriptEnabled: false });

  test("reads as lists, with no switch and no map", async ({ page }) => {
    await page.goto(pageURL("map"));
    await expect(page.locator("#ws-1")).toBeVisible();
    await expect(page.getByRole("group", { name: "Show the detail as" })).toBeHidden();
    await expect(tree(page)).toBeHidden();
  });
});
