# lookout

A single vantage point over everything you have in flight. lookout gathers recent activity from git, GitHub, AI coding sessions, and files supplied by other tools, groups it into workstreams you define, and reports how recently each one moved and whether every input it relies on is healthy.

lookout is at an early stage, and has no releases yet. To build it from a clone, run `just install`, which installs it to `~/.local/bin` using the toolchain `mise.toml` pins. Reading git repositories needs git 2.36 or later, and reading GitHub needs the [GitHub CLI](https://cli.github.com/), logged in to github.com (`gh auth login`) or with `GH_TOKEN` set. Reading Claude Code sessions needs the configuration directories that hold them, listed as `config_dirs`, and git, which places each session in the repository at its working directory when lookout reads. [docs/vision.md](docs/vision.md) describes the dashboard it is the data layer for.

Run `lookout show` to read a setting's workstreams in a terminal. It lists them in their stated order, one row each, in plain text that fits 80 columns. Each row shows:
- how long since the workstream was last touched;
- `stale` once that time is longer than `event_window`, or `unread` when nothing was read for it on this read, neither a member nor an item a key joined;
- its members per input, with how many no input supplied, and, counted apart, the items a key joined to it (`github 1 +2 joined`).

A row starting with `!` draws on an input that is degraded or in error, so its figures may understate. Below the rows, a warning names each such input with its reasons, and each workstream that draws on one. The last line counts the unplaced and dismissed items and the completed workstreams, and it starts with `!` whenever any input is degraded or in error, since such an input can leave those counts short. Tabs and line breaks in a name are shown as spaces. A name holding any other control character, an invisible formatting character, or a space other than the plain one is shown quoted, with that character escaped. A name longer than 24 characters is cut and ends in `~`. The width counts every character as one column, so wide characters, such as many in Chinese, Japanese or Korean, can push a line past 80. The view is drawn from the same readout `lookout read --json` writes. It is a stopgap for reading the data now, not the dashboard the vision describes.

Run `lookout snapshot` once a day to keep a baseline of how often new work appears and how stale your workstreams are. Read beside the gesture log in the same directory, it shows how each new item was placed, and each record lists the items a key joined apart from those a command or a hand edit placed. A joined item that last moved before `discovery_window` counts as already there rather than as arriving. With a `github` input configured, each record also lists the pull requests you authored within your github.com `forge_owners` that merged since the last record that read them. To count them, skip records whose `merged_prs.failed` is set, since a failed read lists nothing, and group by each entry's `merged_at`: a merge GitHub's search finds late is listed on a later record. The first snapshot of a date is kept, so running it again later that day changes nothing.

## Configuration

Each setting, such as `personal` or `work`, has one configuration file, `$XDG_CONFIG_HOME/lookout/<setting>.yml` (`~/.config/lookout/<setting>.yml` by default). A setting name uses lowercase letters, digits, `-` and `_`. lookout reads exactly one file per invocation: the one `--setting <name>` names, or else the path in `LOOKOUT_CONFIG`, or else the only file in that directory, unless that file sets `explicit_only`. With several files there and neither given, it refuses rather than combining settings, whichever of them set `explicit_only`.

```yaml
event_window: 168h
discovery_window: 72h
forge_owners: [your-name, gitlab.com/your-name]
key_pattern: '([A-Z][A-Z0-9]+-[0-9]+)'
inputs:
  - name: code
    kind: git
    repos: [~/code/*]
    ignore_branches: [worktree-*, dependabot/*/*]
  - name: hub
    kind: github
  - name: sessions
    kind: claude_code
    config_dirs: [~/.claude]
  - name: tracker
    kind: supplied
    supplied: tracker.json
    max_age: 24h
workstreams:
  - name: search
    members:
      - hub:your-name/app#12
      - code:your-name/app:feature/search
  - name: billing
    members:
      - tracker:ABC-123
    completed: 2030-06-15
```

- **`explicit_only`**: `true` keeps this setting from being read as the only file in the directory, so every command, reads included, uses it only when `--setting` or `LOOKOUT_CONFIG` names it. Without that, a command typed for another setting would edit or record into this one. A command that names neither is refused, and the refusal writes nothing. `LOOKOUT_CONFIG` counts as naming the setting even when a shell profile exports it, so exporting it for good gives up the protection. A scheduled command that relied on the lone file, such as `lookout snapshot`, needs `--setting` or `LOOKOUT_CONFIG` once this is set. A value lookout cannot read as a boolean is refused, and so is a file with a line that looks like it sets the key, such as `explicit-only:`, `ExplicitOnly:` or an indented `explicit_only:`, where lookout finds no top-level `explicit_only` it can read: a misspelling or a broken file never leaves the setting open to being chosen by default. A file lookout cannot read at all is still chosen, so that its error is reported.
- **`event_window`**: how far back a workstream's events reach, and the window within which it counts as active. A Go duration such as `24h` or `90m`, with no unit for days, in whole seconds. The default is two weeks.
- **`discovery_window`**: how recently an item a git, GitHub or Claude Code input finds must have moved to be offered as unplaced. It defaults to `event_window`. A workstream's members, and the git branches and pull requests a key joins to it, are read at any age.
- **`forge_owners`**: the owners whose repositories, pull requests, issues and sessions this setting admits. Nothing else is read into it. A bare name is an account or organization on github.com, and an owner on another host is written `host/owner`. It is required once a `git`, `github` or `claude_code` input is configured, and a `github` input needs at least one github.com owner.
- **`key_pattern`**: a regular expression in Go's syntax, written in single quotes, with one capture group around a ticket key, such as `'([A-Z][A-Z0-9]+-[0-9]+)'`. On every read, an item whose key is the id of a supplied member joins that member's workstream, without being added to this file, and the readout and `lookout show` mark it as joined. The key is what the group captures in the first match: in a pull request's title, or else its branch; in a git branch's name; and in a Claude Code session's branch. Keys compare without regard to case, so `feature/abc-12` carries `ABC-12`. Only the first key counts, so under the example pattern a title such as `Fix UTF-8 in ABC-12` carries `UTF-8`; narrow the pattern to your tracker's prefixes where that happens. A key belongs to the first live workstream, in their stated order, that names a supplied member with that id. It never takes an item a live workstream names, or one a completed workstream still holds because it has not moved since. lookout also searches github.com pull request titles for each key that holds no quote, colon or space, so merged pull requests and colleagues' join too, but only those whose author the repository lists as an owner, member or collaborator, so your own pull request to an organization you contribute to without belonging joins only while `github` discovers it as your open one. A pull request that carries its key only in its branch joins only when it is your own open one within `discovery_window`. Claude Code sessions join only within `discovery_window`. A workstream that a key belongs to draws on every `git`, `github` and `claude_code` input, so one that fails marks it with `!`, since that input may have kept a join from it. `lookout dismiss` keeps a joined item out until it moves again, and `lookout remove` of an item whose key is a member's id leaves it joined. A pattern with no capture group or several, or one that matches an empty text, is refused.
- **`inputs`**: each input has a `name`, which uses the same characters as a setting name, and a `kind`:
  - **`git`**: the local branches of the repositories `repos` lists. An entry is a path or a glob pattern, `~/` is your home directory, and a relative entry is read from the configuration's directory. A pattern admits only directories that are repositories, and `**` is refused, so list each depth, such as `~/code/*` and `~/code/*/*`. The default branch is never offered.
  - **`github`**: open pull requests you authored and open issues assigned to you, within the github.com owners, plus every member at any age and state, and with `key_pattern`, the pull requests a key search finds. It takes no settings of its own.
  - **`claude_code`**: Claude Code sessions in the configuration directories `config_dirs` lists, as one item per repository and branch. As in `repos`, `~/` is your home directory and a relative path is read from the configuration's directory.
  - **`supplied`**: a file an outside tool writes in the [`supplied.v1`](schema/supplied.v1.json) contract. `supplied` is its path, read from the configuration's directory when relative. `max_age` is how old its data may get before the input counts as stale.
  - **`ignore_branches`** (on `git` and `claude_code`): branch patterns never offered, matched as Go's `path.Match` does, so `*` stops at `/`.
  - **`expect_records`** (any kind): an input that reads no items is degraded rather than healthy. Naming a supplied input's item in a workstream implies it.
- **`workstreams`**: in the order the readout lists them. `members` are `<input>:<id>` tokens:
  - `git` and `claude_code`: `owner/repo:branch`, or `host/owner/repo:branch` off github.com. A detached session is `owner/repo`.
  - `github`: `owner/repo#N`.
  - `supplied`: the id the supplier writes.

  `lookout unplaced` prints these tokens ready to paste. `completed: YYYY-MM-DD` marks a workstream finished. It leaves the readout, and a member touched after that date is offered again.

`lookout adopt`, `move`, `remove`, `rename`, `order` and `complete` edit this file in place and keep its comments and layout. They refuse a file whose YAML they cannot edit safely, such as one using anchors, tags, several documents or non-empty flow-style (`[...]`) workstream or member lists, and say what to change. Everything lookout records itself, the gesture log and the baseline, is under `$XDG_STATE_HOME/lookout/<setting>/` (`~/.local/state/lookout/<setting>/` by default), and is never hand-edited.

## License

[MIT](LICENSE)
