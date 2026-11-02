// Global setup: render each fixture readout through the real binary, so the
// specs drive the page `lookout view` writes rather than the template alone.
import { execFileSync } from "node:child_process";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

/** The readouts the specs open, by name: one beside the Go golden, one with nothing in it. */
const fixtures = {
  busy: "internal/htmlview/testdata/busy.json",
  empty: "internal/htmlview/browser/fixtures/empty.json",
};

export default function render() {
  const scratch = mkdtempSync(join(tmpdir(), "lookout-page-"));
  const binary = join(scratch, "lookout");
  execFileSync("go", ["build", "-o", binary, "./cmd/lookout"], { stdio: "inherit" });
  /** @type {Record<string, string>} */
  const pages = {};
  for (const [name, readout] of Object.entries(fixtures)) {
    // Each fixture gets its own cache directory, since both name the same
    // setting and `view` writes one page per setting.
    // `--readout` reads no configuration; dropping LOOKOUT_CONFIG keeps the
    // caller's own setting out of reach should that ever change.
    /** @type {NodeJS.ProcessEnv} */
    const env = { ...process.env, XDG_CACHE_HOME: join(scratch, name) };
    delete env.LOOKOUT_CONFIG;
    const path = execFileSync(binary, ["view", "--readout", readout], { env, encoding: "utf8" }).trim();
    pages[name] = pathToFileURL(path).href;
  }
  // Workers inherit the environment global setup leaves behind.
  process.env.LOOKOUT_PAGES = JSON.stringify(pages);
}
