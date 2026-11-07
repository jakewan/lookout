// The page's map: a mind map of the setting, its workstreams and their items. Go decides every order, place, count and cue and hands them over in
// the page's data block; this script only lays them out and shows or hides
// parts of them, so no rule lives here twice. It builds the page only with
// createElement and textContent.

/**
 * @typedef {object} MapNode
 * @property {string} kind setting, workstream, item, or unread
 * @property {string} handle what commands take, exactly
 * @property {string} ref the handle, cleaned for showing
 * @property {string} label
 * @property {string} sub
 * @property {string} name
 * @property {string[]} words
 * @property {string} flag
 * @property {string} tone fault, behind, unread, stale, or empty: how loud its branch is drawn
 * @property {string} state
 * @property {string} href
 * @property {string} joined_by
 * @property {boolean} hidden_chars
 * @property {string} anchor
 * @property {number} parent
 * @property {number[]} children
 * @property {number} weight
 * @property {string} rollup
 * @property {string} rollup_tone the loudest tone beneath the node, so a fold never hides a flag
 * @property {boolean} start_collapsed
 */

/**
 * @typedef {object} CrossLink
 * @property {number} from
 * @property {number} to
 * @property {string} kind
 * @property {string} label
 */

/**
 * What the page says about its read's age from `at` on.
 * @typedef {object} AgeStep
 * @property {string} at an RFC 3339 moment
 * @property {string} text
 * @property {boolean} old
 */

/**
 * @typedef {object} ViewModel
 * @property {number} hub
 * @property {MapNode[]} nodes
 * @property {CrossLink[]} links
 * @property {AgeStep[]} read_age
 */

/**
 * Where a node is drawn: its branch meets its label's line at (x, y), and the
 * line runs `run` pixels outward, to the right where side is 1 and to the left
 * where it is -1. The hub has side 0 and sits at the center.
 * @typedef {{x: number, y: number, side: number, depth: number, run: number}} Place
 */

/** @typedef {{x: number, y: number}} Point */

/** The height each shown leaf takes. */
const row = 26;
/** The horizontal curve between a node's line and its children's. */
const gap = 36;
/** How far from the center the first branches leave the hub. */
const hubReach = 56;

/**
 * How long a node's line is: the first branches are longest, as in a
 * hand-drawn map.
 * @param {number} depth
 */
function runAt(depth) {
  return depth === 1 ? 200 : 190;
}

/**
 * @param {ViewModel} model
 * @param {number} i
 * @returns {MapNode}
 */
function nodeOf(model, i) {
  const n = model.nodes[i];
  if (!n) throw new Error(`the map has no node ${i}`);
  return n;
}

/**
 * The nodes shown beneath hub, in Go's order, leaving out what a collapsed
 * node holds.
 * @param {ViewModel} model
 * @param {number} hub
 * @param {Set<number>} collapsed
 * @returns {number[]}
 */
export function shown(model, hub, collapsed) {
  /** @type {number[]} */
  const out = [];
  /** @param {number} i */
  const walk = (i) => {
    out.push(i);
    if (collapsed.has(i)) return;
    for (const c of nodeOf(model, i).children) walk(c);
  };
  walk(hub);
  return out;
}

/**
 * The rows a shown node takes: Go's leaf weight, or one row where the node is
 * collapsed or holds a collapsed node, whose leaves are then not drawn.
 * @param {ViewModel} model
 * @param {number} i
 * @param {Set<number>} collapsed
 * @returns {number}
 */
function rows(model, i, collapsed) {
  const node = nodeOf(model, i);
  if (collapsed.has(i) || node.children.length === 0) return 1;
  if (collapsed.size === 0) return Math.max(1, node.weight);
  return node.children.reduce((sum, c) => sum + rows(model, c, collapsed), 0);
}

/**
 * Where each shown node is drawn, as a hand-drawn mind map is: the hub at the
 * center, its branches leaving to both sides and running outward, each level
 * further out, siblings stacked by the leaves they hold. Go's order runs down
 * the right side and then back up the left, clockwise from the top.
 * @param {ViewModel} model
 * @param {number} hub
 * @param {Set<number>} collapsed
 * @returns {Map<number, Place>}
 */
export function layout(model, hub, collapsed) {
  /** @type {Map<number, Place>} */
  const at = new Map();
  at.set(hub, { x: 0, y: 0, side: 0, depth: 0, run: 0 });
  /**
   * Places nodes on one side in a column centered on y, top to bottom.
   * @param {number[]} nodes
   * @param {number} side
   * @param {number} x
   * @param {number} y
   * @param {number} depth
   */
  const column = (nodes, side, x, y, depth) => {
    const total = nodes.reduce((sum, c) => sum + rows(model, c, collapsed), 0);
    let top = y - (total * row) / 2;
    for (const c of nodes) {
      const height = rows(model, c, collapsed) * row;
      const run = runAt(depth);
      const mid = top + height / 2;
      at.set(c, { x, y: mid, side, depth, run });
      if (!collapsed.has(c)) column(nodeOf(model, c).children, side, x + side * (run + gap), mid, depth + 1);
      top += height;
    }
  };
  if (!collapsed.has(hub)) {
    const children = nodeOf(model, hub).children;
    const half = Math.ceil(children.length / 2);
    column(children.slice(0, half), 1, hubReach + gap, 0, 1);
    // The left side reads bottom to top, so the order keeps running clockwise.
    column(children.slice(half).reverse(), -1, -(hubReach + gap), 0, 1);
  }
  return at;
}

/**
 * The words the panel uses for one of a node's cross-links.
 * @param {ViewModel} model
 * @param {CrossLink} link
 * @param {number} i
 */
export function linkWords(model, link, i) {
  const other = nodeOf(model, link.from === i ? link.to : link.from);
  return `${link.label} with ${other.ref}`;
}

/**
 * @template {keyof HTMLElementTagNameMap} K
 * @param {K} tag
 * @param {string} [text]
 * @param {string} [className]
 * @returns {HTMLElementTagNameMap[K]}
 */
function el(tag, text, className) {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  if (className) e.className = className;
  return e;
}

/** @param {string} tag */
function svg(tag) {
  return document.createElementNS("http://www.w3.org/2000/svg", tag);
}

/**
 * @param {Element} e
 * @param {Record<string, string>} attrs
 */
function attrs(e, attrs) {
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
}

/** The map's state: which node is the hub, which is selected, which are collapsed. */
class MapView {
  /**
   * @param {ViewModel} model
   * @param {HTMLElement} map
   */
  constructor(model, map) {
    this.model = model;
    this.map = map;
    this.hub = model.hub;
    this.selected = model.hub;
    /** @type {Set<number>} */
    this.collapsed = new Set(model.nodes.flatMap((n, i) => (n.start_collapsed ? [i] : [])));
    /** @type {Map<number, HTMLElement>} */
    this.items = new Map();
  }

  /** @param {string} selector */
  part(selector) {
    const e = this.map.querySelector(selector);
    if (!(e instanceof HTMLElement)) throw new Error(`the map has no ${selector}`);
    return e;
  }

  /**
   * Builds the map's content off the page, so a failure partway leaves the
   * page as it was.
   */
  build() {
    const at = layout(this.model, this.hub, this.collapsed);
    const xs = [...at.values()].flatMap((p) => [p.x, p.x + p.side * p.run]);
    const ys = [...at.values()].map((p) => p.y);
    const pad = 60;
    const left = Math.min(...xs, -hubReach) - pad;
    const top = Math.min(...ys) - pad;
    const width = Math.max(...xs, hubReach) - left + pad;
    const height = Math.max(...ys) - top + pad / 2;
    /** @param {number} i */
    const place = (i) => {
      const p = at.get(i);
      return p ? { ...p, x: p.x - left, y: p.y - top } : undefined;
    };
    /**
     * Where a cross-link meets a node: the middle of its line.
     * @param {number} i
     * @returns {Point | undefined}
     */
    const point = (i) => {
      const p = place(i);
      return p ? { x: p.x + (p.side * p.run) / 2, y: p.y } : undefined;
    };

    const drawing = svg("svg");
    attrs(drawing, { "aria-hidden": "true", width: String(width), height: String(height) });
    for (const i of at.keys()) {
      if (i === this.hub) continue;
      const node = nodeOf(this.model, i);
      const p = place(i);
      const q = place(node.parent);
      if (!p || !q) continue;
      // A branch curves out of the end of its parent's line, or out of the
      // hub, into the line its own label is written on.
      const from = q.side === 0 ? { x: q.x + p.side * hubReach, y: q.y } : { x: q.x + q.side * q.run, y: q.y };
      const bend = (p.side * gap) / 2;
      const path = svg("path");
      attrs(path, {
        class: `branch depth-${Math.min(p.depth, 3)}${node.tone ? ` tone-${node.tone}` : ""}`,
        d: `M ${from.x} ${from.y} C ${from.x + bend} ${from.y} ${p.x - bend} ${p.y} ${p.x} ${p.y} L ${p.x + p.side * p.run} ${p.y}`,
      });
      drawing.append(path);
    }
    const center = point(this.hub) ?? { x: 0, y: 0 };
    for (const link of this.model.links) {
      const a = point(link.from);
      const b = point(link.to);
      if (!a || !b) continue;
      const path = svg("path");
      // Each cross-link bows toward the hub, so it reads as a relation
      // drawn over the tree rather than as one of its branches.
      const cx = (a.x + b.x) / 2 + (center.x - (a.x + b.x) / 2) * 0.5;
      const cy = (a.y + b.y) / 2 + (center.y - (a.y + b.y) / 2) * 0.5;
      attrs(path, {
        class: this.touches(link) ? "xlink strong" : "xlink",
        d: `M ${a.x} ${a.y} Q ${cx} ${cy} ${b.x} ${b.y}`,
        "data-from": nodeOf(this.model, link.from).handle,
        "data-to": nodeOf(this.model, link.to).handle,
        "data-kind": link.kind,
      });
      const label = svg("text");
      attrs(label, { class: "xlabel", x: String((a.x + 2 * cx + b.x) / 4), y: String((a.y + 2 * cy + b.y) / 4) });
      label.textContent = link.label;
      drawing.append(path, label);
    }

    this.items = new Map();
    const tree = el("ul");
    attrs(tree, { role: "tree", "aria-label": "map of every workstream's items" });
    // The tree is flat, each node saying its level and place among its
    // siblings, so no node's name or visible text takes in the nodes
    // beneath it.
    /**
     * @param {number} i
     * @param {number} level
     * @param {number} pos
     * @param {number} size
     */
    const add = (i, level, pos, size) => {
      tree.append(this.item(i, level, pos, size, place));
      if (this.collapsed.has(i)) return;
      const kids = nodeOf(this.model, i).children;
      kids.forEach((c, k) => {
        add(c, level + 1, k + 1, kids.length);
      });
    };
    add(this.hub, 1, 1, 1);
    tree.addEventListener("keydown", (e) => this.key(e));

    const canvas = el("div", undefined, "plane");
    canvas.style.width = `${width}px`;
    canvas.style.height = `${height}px`;
    canvas.append(drawing, tree);
    return canvas;
  }

  /**
   * @param {CrossLink} link
   */
  touches(link) {
    return link.from === this.selected || link.to === this.selected;
  }

  /**
   * @param {number} i
   * @param {number} level
   * @param {number} pos
   * @param {number} size
   * @param {(i: number) => Place | undefined} place
   * @returns {HTMLElement}
   */
  item(i, level, pos, size, place) {
    const node = nodeOf(this.model, i);
    const li = el("li");
    const open = !this.collapsed.has(i);
    const name = open || !node.rollup ? node.name : `${node.name}, ${node.rollup} hidden`;
    // The name is read from a hidden span rather than an aria-label, and
    // holds the visible label, so what a speech user sees is what they say.
    const said = el("span", name, "vh");
    said.id = `map-node-${i}`;
    attrs(li, {
      role: "treeitem",
      "aria-level": String(level),
      "aria-posinset": String(pos),
      "aria-setsize": String(size),
      "aria-labelledby": said.id,
      "aria-selected": String(i === this.selected),
      "data-handle": node.handle,
      "data-tone": node.tone,
      tabindex: i === this.selected ? "0" : "-1",
    });
    li.append(said);
    if (i !== this.hub) li.setAttribute("data-parent", nodeOf(this.model, node.parent).handle);
    if (node.children.length > 0) li.setAttribute("aria-expanded", String(open));
    const p = place(i);
    const side = !p || p.side === 0 ? "hub" : p.side > 0 ? "right" : "left";
    const box = el(
      "span",
      undefined,
      `node ${node.kind} ${side}${node.tone ? ` tone-${node.tone}` : ""}${i === this.selected ? " selected" : ""}`,
    );
    box.setAttribute("aria-hidden", "true");
    if (node.flag) box.append(el("span", node.flag, "flag"));
    box.append(el("span", node.label, "label"));
    if (node.sub) box.append(" ", el("span", node.sub, "sub"));
    if (!open && node.rollup) {
      box.append(" ", el("span", node.rollup, `rollup${node.rollup_tone ? ` tone-${node.rollup_tone}` : ""}`));
    }
    // The item itself sits where its branch meets its line, so focusing it
    // scrolls the node into view; the label is written along the line.
    if (p) {
      li.style.left = `${p.x}px`;
      li.style.top = `${p.y}px`;
      if (p.run > 0) box.style.maxWidth = `${p.run - 8}px`;
    }
    box.addEventListener("click", () => this.select(i, true));
    li.append(box);
    this.items.set(i, li);
    return li;
  }

  /** Draws the map again, keeping the selection and, where it had it, focus. */
  render() {
    const focused = this.map.contains(document.activeElement);
    this.part(".canvas").replaceChildren(this.build());
    this.crumbs();
    this.panel();
    if (focused) this.items.get(this.selected)?.focus();
  }

  /**
   * @param {number} i
   * @param {boolean} focus
   */
  select(i, focus) {
    this.selected = i;
    let up = nodeOf(this.model, i).parent;
    // A node hidden inside a collapsed one, or outside the hub, cannot be
    // selected without showing it: open the way to it.
    while (up >= 0) {
      this.collapsed.delete(up);
      up = nodeOf(this.model, up).parent;
    }
    if (!this.within(i)) this.hub = this.model.hub;
    this.render();
    if (focus) this.items.get(i)?.focus();
  }

  /** @param {number} i */
  within(i) {
    for (let k = i; k >= 0; k = nodeOf(this.model, k).parent) if (k === this.hub) return true;
    return false;
  }

  crumbs() {
    const nav = this.part(".crumbs");
    /** @type {number[]} */
    const up = [];
    for (let k = nodeOf(this.model, this.hub).parent; k >= 0; k = nodeOf(this.model, k).parent) up.unshift(k);
    nav.replaceChildren(
      ...up.map((k) => {
        const b = el("button", nodeOf(this.model, k).label);
        b.type = "button";
        b.addEventListener("click", () => {
          this.hub = k;
          this.select(k, true);
        });
        return b;
      }),
    );
    nav.hidden = up.length === 0;
  }

  panel() {
    const panel = this.part(".panel");
    const i = this.selected;
    const node = nodeOf(this.model, i);
    /** @type {HTMLElement[]} */
    const parts = [el("h2", node.label)];
    if (node.sub) parts.push(el("p", node.sub));
    if (node.handle) {
      const handle = el("p");
      handle.append(el("code", node.ref));
      const copy = el("button", "Copy");
      copy.type = "button";
      copy.addEventListener("click", () => {
        void navigator.clipboard.writeText(node.handle);
      });
      handle.append(" ", copy);
      parts.push(handle);
      if (node.hidden_chars) {
        parts.push(el("p", "This handle contains hidden characters, so it is shown escaped; Copy takes it exactly."));
      }
    }
    if (node.words.length > 0) parts.push(el("p", node.words.join(", ")));
    if (node.rollup) parts.push(el("p", `Holds ${node.rollup}`));
    const links = this.model.links.filter((l) => l.from === i || l.to === i);
    if (links.length > 0) {
      const list = el("ul");
      for (const l of links) list.append(el("li", linkWords(this.model, l, i)));
      parts.push(list);
    }
    if (node.href) {
      const a = el("a", "open in its source");
      a.href = node.href;
      // A new tab, so following an item leaves the map as it was.
      a.target = "_blank";
      a.rel = "noreferrer noopener";
      parts.push(a);
    }
    const actions = el("p");
    const hub = el("button", "Make hub");
    hub.type = "button";
    hub.addEventListener("click", () => {
      this.hub = i;
      this.select(i, true);
    });
    actions.append(hub);
    if (node.children.length > 0) {
      const fold = el("button", this.collapsed.has(i) ? "Expand" : "Collapse");
      fold.type = "button";
      fold.addEventListener("click", () => {
        this.toggle(i);
      });
      actions.append(" ", fold);
    }
    parts.push(actions);
    panel.replaceChildren(...parts);
    panel.hidden = false;
  }

  /** @param {number} i */
  toggle(i) {
    if (this.collapsed.has(i)) this.collapsed.delete(i);
    else this.collapsed.add(i);
    this.render();
  }

  /**
   * The tree view keys: up and down move through what is shown, right opens
   * a node or steps into it, left closes it or steps out, Enter selects.
   * @param {KeyboardEvent} e
   */
  key(e) {
    const order = shown(this.model, this.hub, this.collapsed);
    const here = order.findIndex((i) => this.items.get(i) === document.activeElement);
    if (here < 0) return;
    const i = order[here] ?? this.hub;
    const node = nodeOf(this.model, i);
    /** @param {number | undefined} to */
    const move = (to) => {
      if (to === undefined) return;
      this.selected = to;
      this.render();
      this.items.get(to)?.focus();
    };
    switch (e.key) {
      case "ArrowDown":
        move(order[here + 1]);
        break;
      case "ArrowUp":
        move(order[here - 1]);
        break;
      case "ArrowRight":
        if (node.children.length === 0) return;
        if (this.collapsed.has(i)) this.toggle(i);
        else move(node.children[0]);
        break;
      case "ArrowLeft":
        if (node.children.length > 0 && !this.collapsed.has(i)) this.toggle(i);
        else if (i !== this.hub) move(node.parent);
        break;
      case "Enter":
      case " ":
        this.select(i, true);
        break;
      default:
        return;
    }
    e.preventDefault();
  }

  /**
   * Selects the workstream a lane's anchor names, where the map is shown.
   * @param {string} anchor
   */
  follow(anchor) {
    const i = this.model.nodes.findIndex((n) => n.anchor !== "" && n.anchor === anchor);
    if (i >= 0 && !this.map.hidden) this.select(i, true);
  }
}

/**
 * The step Go wrote for this moment: the latest whose moment has passed, or
 * null while none has, as for a read dated after the browser's clock. Picking
 * among Go's steps by the clock is what is shown, not a rule: the wording, the
 * grain and when a read turns old are all Go's.
 * @param {AgeStep[]} steps
 * @param {number} now
 * @returns {AgeStep | null}
 */
export function ageAt(steps, now) {
  /** @type {AgeStep | null} */
  let shown = null;
  for (const step of steps) {
    if (Date.parse(step.at) > now) break;
    shown = step;
  }
  return shown;
}

/**
 * Keeps the header's read-age slot on the step for the moment, for as long
 * as the page stays open. It runs apart from the map, since a page with no
 * workstreams has no map but still ages.
 */
export function age() {
  const slot = document.querySelector(".readage");
  const data = document.getElementById("lookout-map");
  if (!(slot instanceof HTMLElement) || !data) return;
  /** @type {ViewModel} */
  const model = JSON.parse(data.textContent ?? "");
  const steps = model.read_age ?? [];
  const show = () => {
    const step = ageAt(steps, Date.now());
    slot.hidden = step === null;
    // The slot is a status a screen reader announces on change, so it is
    // written only when what it says changes.
    if (step && slot.textContent !== step.text) {
      slot.textContent = step.text;
      slot.classList.toggle("old", step.old);
    }
  };
  show();
  setInterval(show, 30 * 1000);
}

export function main() {
  const map = document.getElementById("map");
  const lists = document.getElementById("lists");
  const data = document.getElementById("lookout-map");
  const toggle = document.querySelector(".viewswitch");
  if (!map || !lists || !data || !(toggle instanceof HTMLElement)) return;
  /** @type {ViewModel} */
  const model = JSON.parse(data.textContent ?? "");
  const view = new MapView(model, map);
  // Everything is built before anything on the page changes, so a failure
  // partway leaves the lists as they were.
  const canvas = view.build();
  const [showMap, showList] = toggle.querySelectorAll("button");
  if (!showMap || !showList) return;
  /** @param {boolean} on */
  const choose = (on) => {
    map.hidden = !on;
    lists.hidden = on;
    showMap.setAttribute("aria-pressed", String(on));
    showList.setAttribute("aria-pressed", String(!on));
  };
  view.part(".canvas").replaceChildren(canvas);
  view.crumbs();
  view.panel();
  showMap.addEventListener("click", () => choose(true));
  showList.addEventListener("click", () => choose(false));
  // A lane is followed by its click, since clicking the same lane twice does
  // not change the address and fires no hashchange; hashchange still covers
  // moving back and forward through history. Both read the anchor from what
  // they are given, so the script never touches the page's own address
  // object, which a Go word scan keeps out of it.
  for (const lane of document.querySelectorAll(".overview a.ovname")) {
    if (!(lane instanceof HTMLAnchorElement)) continue;
    lane.addEventListener("click", (e) => {
      // In the map a lane selects its workstream's node; jumping on to the
      // hidden list section would only take focus away from it. In the
      // lists, the link goes where it always has.
      if (map.hidden) return;
      e.preventDefault();
      view.follow(lane.hash.slice(1));
    });
  }
  window.addEventListener("hashchange", (e) => view.follow(new URL(e.newURL).hash.slice(1)));
  toggle.hidden = false;
  choose(true);
}

if (typeof document !== "undefined") {
  age();
  main();
}
