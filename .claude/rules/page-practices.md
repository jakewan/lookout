---
paths:
  - "internal/htmlview/**/*"
  - "package.json"
  - "playwright.config.js"
  - "biome.json"
  - "tsconfig.json"
---

# Page Practices

Conventions for the page `lookout view` writes, and for the browser specs and tools around it.

## What the page may compute

The health, staleness and ranking rules live in Go: `internal/health` and the input adapters compute each input's health, `internal/viewrules` decides how every view marks age, staleness and health, and `internal/htmlview` derives what only the page shows. None of them lives in the page's script. Go renders the page's text, so it reads fully with no script, and it computes every verdict, order, count and rollup the page shows. Script the page gains may derive only geometry and visibility: where a node sits, what is collapsed, which node is the hub, what is copied to the clipboard. It never orders, ranks, counts, rolls up or breaks a cycle; if a view needs one of those, Go computes it and hands it over. `docs/vision.md` records why: rules written again in JavaScript would be a second copy, kept in step with Go's by hand.

## Files

- `page.html.tmpl` is the markup, `page.css` the stylesheet, `page.js` the script. Go embeds all three and inlines the stylesheet into one `<style>` element and the script into one `<script type="module">`, so the page stays one file that loads nothing.
- `page.css` is inlined as trusted CSS and `page.js` as trusted script. The stylesheet must never contain `</style`, nor the script `</script` or `<!--`; Go specs hold them to that.
- What the script draws reaches it as a view model (`viewmodel.go`) in a `<script type="application/json">` block. html/template writes it as JSON, which escapes every `<`, so readout text cannot end the block; pass it the Go value, never `template.JS`. It is the page's own, not a published contract. `testdata/map.viewmodel.json` is its golden, and `browser/viewmodel.check.js` assigns that golden to the script's JSDoc types, so `tsc` fails when Go drops or renames a key the script reads.
- The page's content security policy is the first element in its `<head>`, and a Go spec pins its exact text, the script's hash included. Go computes the hash from `page.js` and writes it into the template's own text before parsing, so the hash is never a value html/template escapes. Changing the policy is a deliberate change to what the page may do, made in the template and that spec together.
- The policy cannot hold the script back from navigating or opening a WebRTC connection, so two guards keep the script from them, and from writing markup or code from a string: `page-sinks.grit`, a Biome plugin scoped to `page.js`, refuses the call shapes it names, and a Go spec refuses the words those calls need, which a helper such as `attrs` cannot hide. The script builds the page with `createElement`, `createElementNS` and `textContent`, never by parsing markup.
- The script builds the whole map before changing anything on the page, and `[hidden]` cannot be overridden, so a script that fails partway leaves the lists as they were.
- Biome lints `page.css` but does not format it, so its rules keep the shape they were reviewed in. Reformat it as its own change, under the browser specs.

## Browser specs

The specs under `internal/htmlview/browser/` drive the page `lookout view --readout` writes from fixture readouts, in Chromium and Firefox, from the reader's side: what is shown and in what order, what a keyboard reaches, that the page requests nothing and trips its policy nowhere, and an accessibility audit in light and dark colors. They are plain JavaScript checked by `tsc` through JSDoc types, with no build step. Run them with `just test-page` after `just page-setup`; `just page-check` runs the type check, Biome and the lockfile registry check.

The audit opens every workstream's detail first, since axe skips hidden content. It asserts on axe's violations, not its incomplete results, which are checks axe could not decide; Firefox reports some contrast checks that way.

Fix an accessibility shortfall the audit finds. Where one must stay for a while, record it in the audit spec by what it is, never by switching its rule off, so any other shortfall still fails.

The network spec sees a request only where the engine reports one: Chromium reports a request the policy blocks, Firefox does not, so in Firefox the policy spec is what catches a page reaching out.

Every spec that runs the page's script checks the policy after it runs, through `support.js`, so a violation the page trips while being used fails as surely as one it trips while loading. A report arrives as a task of its own after the action that caused it, so the check waits a frame and a task before reading.

The map is a flat ARIA tree: every node is a `treeitem` in one list, saying its level, its place among its siblings and, in `data-parent`, its parent's handle. Nested groups would make each node's visible text take in the nodes beneath it, which axe reports as a name that does not match its label. A node's name comes from a hidden span through `aria-labelledby` and holds its visible label, so what a speech user sees is what they say; a Go spec holds every node to that.

## What the stack can do, as checked

Each fact below was checked against the pinned versions; recheck it when the version moves.

- **axe under the page's policy.** `@axe-core/playwright` runs on the page without bypassing its content security policy, in both engines, Trusted Types and the hash-only `script-src` included.
- **Trusted Types from a `<meta>` policy.** `require-trusted-types-for 'script'` in the page's `<meta>` policy is enforced in Chromium and Firefox: assigning a string to `innerHTML` throws.
- **A scrolling element is a tab stop in Firefox.** Firefox lets Tab land on an element that scrolls even when it holds focusable content, so the map's scrolling container carries `tabindex="-1"` to keep the map one tab stop.
- **GritQL regex groups bind.** In a Biome GritQL pattern, a capturing group in an `r"…"` regex is taken as a variable, and the plugin errors instead of reporting; write groups as `(?:…)`.
- **Clipboard on a `file://` page.** Both engines treat the opened page as a secure context and expose `navigator.clipboard.writeText`; a write still needs a user gesture.
- **Biome sink rules.** Biome's GritQL plugins match member assignments such as `el.innerHTML = …`, so a sink ban can be a lint rule rather than a text scan, and a plugin entry's `includes` scopes it to the files named.
- **CSS Baseline.** Cascade layers, nesting, logical properties, container queries, `prefers-reduced-motion` and `forced-colors` are Baseline widely available. `light-dark()` is only newly available, so the stylesheet keeps its `prefers-color-scheme` block for dark colors until it is widely available (web-features 3.44.0).
- **Browser downloads.** Playwright checks a browser download only against its announced size, not a checksum; `SECURITY.md` says so.
