# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `lookout read --json` writes a readout of one setting's workstreams, unplaced items, and input health, read from files outside suppliers write. Select the setting with `--setting` or `LOOKOUT_CONFIG`; a lone configuration file is used without either, and with several configured and none selected, lookout refuses. Each workstream carries its member items as their suppliers wrote them, and lists the members no input supplied. Completed workstreams are left out and counted in `completed_count`; an item the user dismissed is left out of `unplaced` and counted in `dismissed_count` until it is touched again, and a completed workstream's item is left out of `unplaced` until it is touched after the day the workstream was completed. (#5)
- A configuration file per setting, at `$XDG_CONFIG_HOME/lookout/<setting>.yml`, listing the expected inputs (`name`, `supplied` path, `max_age`, `expect_records`), the workstreams in their stated order with their `<input>:<id>` members and an optional `completed` date, and an optional `event_window`. A setting's name, taken from the file name, must use lowercase letters, digits, `-` and `_` however the file is selected. (#5)
- Input health computed from each supplied file: missing, unreadable, written for another input, or reporting its own error is `error`; stale, last attempt failed, stamped ahead of the clock, failing its schema, repeating an item id, reporting itself degraded, naming missing fields, or empty where records are expected is `degraded`. An input is expected to have records when `expect_records` says so or a workstream names one of its items. A supplier's report can lower health but never raise it. (#5)
- `lookout adopt <input>:<id> <workstream> [--new]` adds an item to a live workstream, moving it out of any completed workstream that still lists it, `lookout order <workstream>...` moves workstreams to the front of the stated order, and `lookout complete <workstream>` marks one finished as of today. Each edits the configuration file in place, leaving every line it does not change exactly as written, and refuses rather than reformat a file it cannot edit that way or overwrite one that changed while it worked.
- `lookout dismiss <input>:<id>` hides an unplaced item until it is touched again, and `lookout unplaced` lists unplaced items one per line, each starting with the `<input>:<id>` token `adopt` and `dismiss` take. When the id holds a tab or line break the token is printed in double quotes with the character escaped, and `adopt` and `dismiss` accept it in that form; pass it to the shell in single quotes.
- A state directory per setting, at `$XDG_STATE_HOME/lookout/<setting>/`, holding a log of every `adopt`, `order`, `complete` and `dismiss` run against a configuration file that exists, with whether it succeeded, was refused, or failed, so the effort of keeping the configuration current can be measured.
- Published contracts in `schema/`: `supplied.v1` for suppliers, `readout.v1` for renderers, and `health.v1` for input health. (#5)
