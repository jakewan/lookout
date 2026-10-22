# lookout

A single vantage point over everything you have in flight. lookout gathers recent activity from git, GitHub, AI coding sessions, and files supplied by other tools, groups it into workstreams you define, and reports how recently each one moved and whether every input it relies on is healthy.

lookout is at an early stage, and has no releases yet. To build it from a clone, run `just install`, which installs it to `~/.local/bin` using the toolchain `mise.toml` pins. Reading git repositories needs git 2.36 or later, and reading GitHub needs the [GitHub CLI](https://cli.github.com/), logged in to github.com (`gh auth login`) or with `GH_TOKEN` set. Reading Claude Code sessions needs the configuration directories that hold them, listed as `config_dirs`, and git, which places each session in the repository at its working directory when lookout reads. [docs/vision.md](docs/vision.md) describes the dashboard it is the data layer for.

Run `lookout snapshot` once a day to keep a baseline of how often new work appears and how stale your workstreams are. Read beside the gesture log in the same directory, it shows how each new item was placed. With a `github` input configured, each record also lists the pull requests you authored within your github.com `forge_owners` that merged since the last record that read them. To count them, skip records whose `merged_prs.failed` is set, since a failed read lists nothing, and group by each entry's `merged_at`: a merge GitHub's search finds late is listed on a later record. The first snapshot of a date is kept, so running it again later that day changes nothing.

## Configuration

Each setting, such as `personal` or `work`, has one configuration file, `$XDG_CONFIG_HOME/lookout/<setting>.yml` (`~/.config/lookout/<setting>.yml` by default). A setting name uses lowercase letters, digits, `-` and `_`. lookout reads exactly one file per invocation: the one `--setting <name>` names, or else the path in `LOOKOUT_CONFIG`, or else the only file in that directory. With several files there and neither given, it refuses rather than combining settings.

```yaml
event_window: 168h
discovery_window: 72h
forge_owners: [your-name, gitlab.com/your-name]
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

- **`event_window`**: how far back a workstream's events reach, and the window within which it counts as active. A Go duration such as `24h` or `90m`, with no unit for days, in whole seconds. The default is two weeks.
- **`discovery_window`**: how recently an item a git, GitHub or Claude Code input finds must have moved to be offered as unplaced. It defaults to `event_window`. A workstream's members are read at any age.
- **`forge_owners`**: the owners whose repositories, pull requests, issues and sessions this setting admits. Nothing else is read into it. A bare name is an account or organization on github.com, and an owner on another host is written `host/owner`. It is required once a `git`, `github` or `claude_code` input is configured, and a `github` input needs at least one github.com owner.
- **`inputs`**: each input has a `name`, which uses the same characters as a setting name, and a `kind`:
  - **`git`**: the local branches of the repositories `repos` lists. An entry is a path or a glob pattern, `~/` is your home directory, and a relative entry is read from the configuration's directory. A pattern admits only directories that are repositories, and `**` is refused, so list each depth, such as `~/code/*` and `~/code/*/*`. The default branch is never offered.
  - **`github`**: open pull requests you authored and open issues assigned to you, within the github.com owners, plus every member at any age and state. It takes no keys of its own.
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
