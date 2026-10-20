# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `lookout read --json` writes a readout of one setting's workstreams, unplaced items, and input health, read from files outside suppliers write. Select the setting with `--setting` or `LOOKOUT_CONFIG`; with several configured and none selected, lookout refuses. (#5)
- Published contracts in `schema/`: `supplied.v1` for suppliers, `readout.v1` for renderers, and `health.v1` for input health. (#5)
