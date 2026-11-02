// The page's map: a radial drawing of the setting, its workstreams and their
// items. Go decides every order, place, count and cue and hands them over in
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
 * @property {string} state
 * @property {string} href
 * @property {string} joined_by
 * @property {boolean} hidden_chars
 * @property {string} anchor
 * @property {number} parent
 * @property {number[]} children
 * @property {number} weight
 * @property {string} rollup
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
 * @typedef {object} ViewModel
 * @property {number} hub
 * @property {MapNode[]} nodes
 * @property {CrossLink[]} links
 */

/** @typedef {{x: number, y: number}} Point */

/** The distance between one ring of the map and the next. */
const ring = 170;

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
 * Where each shown node sits, with hub at the center. Rank 1 of the first
 * ring is centered at twelve o'clock and the rest follow clockwise. Around
 * the setting every workstream gets an equal share, so a large one cannot
 * crowd a small one out; further out, a node's share follows the leaf weight
 * Go gave it.
 * @param {ViewModel} model
 * @param {number} hub
 * @param {Set<number>} collapsed
 * @returns {Map<number, Point>}
 */
export function layout(model, hub, collapsed) {
  /** @type {Map<number, Point>} */
  const at = new Map();
  at.set(hub, { x: 0, y: 0 });
  /**
   * @param {number} i
   * @param {number} depth
   * @param {number} from
   * @param {number} span
   */
  const spread = (i, depth, from, span) => {
    if (collapsed.has(i)) return;
    const node = nodeOf(model, i);
    const equal = node.kind === "setting";
    const share = (/** @type {number} */ c) => (equal ? 1 : Math.max(1, nodeOf(model, c).weight));
    const total = node.children.reduce((sum, c) => sum + share(c), 0);
    let start = from;
    for (const c of node.children) {
      const width = (span * share(c)) / total;
      const angle = start + width / 2;
      at.set(c, { x: Math.cos(angle) * ring * depth, y: Math.sin(angle) * ring * depth });
      spread(c, depth + 1, start, width);
      start += width;
    }
  };
  const root = nodeOf(model, hub);
  const first = root.children[0];
  const total = root.children.reduce(
    (sum, c) => sum + (root.kind === "setting" ? 1 : Math.max(1, nodeOf(model, c).weight)),
    0,
  );
  const firstShare = first === undefined ? 0 : root.kind === "setting" ? 1 : Math.max(1, nodeOf(model, first).weight);
  const firstWidth = total === 0 ? 0 : (2 * Math.PI * firstShare) / total;
  spread(hub, 1, -Math.PI / 2 - firstWidth / 2, 2 * Math.PI);
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
    const xs = [...at.values()].map((p) => p.x);
    const ys = [...at.values()].map((p) => p.y);
    const pad = 110;
    const left = Math.min(...xs) - pad;
    const top = Math.min(...ys) - pad / 2;
    const width = Math.max(...xs) - left + pad;
    const height = Math.max(...ys) - top + pad / 2;
    /** @param {number} i */
    const point = (i) => {
      const p = at.get(i);
      return p ? { x: p.x - left, y: p.y - top } : undefined;
    };

    const drawing = svg("svg");
    attrs(drawing, { "aria-hidden": "true", width: String(width), height: String(height) });
    for (const [i, p] of at) {
      const parent = nodeOf(this.model, i).parent;
      const q = point(parent);
      const r = point(i);
      if (i === this.hub || !q || !r || !p) continue;
      const line = svg("line");
      attrs(line, { class: "edge", x1: String(q.x), y1: String(q.y), x2: String(r.x), y2: String(r.y) });
      drawing.append(line);
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
      tree.append(this.item(i, level, pos, size, point));
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
   * @param {(i: number) => Point | undefined} point
   * @returns {HTMLElement}
   */
  item(i, level, pos, size, point) {
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
      tabindex: i === this.selected ? "0" : "-1",
    });
    li.append(said);
    if (i !== this.hub) li.setAttribute("data-parent", nodeOf(this.model, node.parent).handle);
    if (node.children.length > 0) li.setAttribute("aria-expanded", String(open));
    const box = el(
      "span",
      undefined,
      `node ${node.kind}${node.flag ? " flagged" : ""}${i === this.selected ? " selected" : ""}`,
    );
    box.setAttribute("aria-hidden", "true");
    if (node.flag) box.append(el("span", node.flag, "flag"));
    box.append(el("span", node.label, "label"));
    if (node.kind === "workstream" && node.sub) box.append(" ", el("span", node.sub, "sub"));
    if (!open && node.rollup) box.append(" ", el("span", node.rollup, "rollup"));
    // The item itself sits at its node's point, so focusing it scrolls the
    // node into view.
    const p = point(i);
    if (p) {
      li.style.left = `${p.x}px`;
      li.style.top = `${p.y}px`;
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
    const links = this.model.links.filter((l) => l.from === i || l.to === i);
    if (links.length > 0) {
      const list = el("ul");
      for (const l of links) list.append(el("li", linkWords(this.model, l, i)));
      parts.push(list);
    }
    if (node.href) {
      const a = el("a", "open in its source");
      a.href = node.href;
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

  /** Follows a lane's link to its workstream's node. */
  follow() {
    const anchor = window.location.hash.slice(1);
    const i = this.model.nodes.findIndex((n) => n.anchor !== "" && n.anchor === anchor);
    if (i >= 0 && !this.map.hidden) this.select(i, true);
  }
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
  window.addEventListener("hashchange", () => view.follow());
  toggle.hidden = false;
  choose(true);
}

if (typeof document !== "undefined") main();
