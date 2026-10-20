# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `lookout read --json` writes a readout of one setting's workstreams, unplaced items, and input health, read from files outside suppliers write. Select the setting with `--setting` or `LOOKOUT_CONFIG`; a lone configuration file is used without either, and with several configured and none selected, lookout refuses. Each workstream lists the members no input supplied. (#5)
- A configuration file per setting, at `$XDG_CONFIG_HOME/lookout/<setting>.yml`, listing the expected inputs (`name`, `supplied` path, `max_age`, `expect_records`), the workstreams in their stated order with their `<input>:<id>` members, and an optional `event_window`. (#5)
- Input health computed from each supplied file: missing, unreadable, written for another input, or reporting its own error is `error`; stale, last attempt failed, stamped ahead of the clock, failing its schema, repeating an item id, reporting itself degraded, naming missing fields, or empty where records are expected is `degraded`. An input is expected to have records when `expect_records` says so or a workstream names one of its items. A supplier's report can lower health but never raise it. (#5)
- Published contracts in `schema/`: `supplied.v1` for suppliers, `readout.v1` for renderers, and `health.v1` for input health. (#5)
