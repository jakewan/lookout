# lookout

A single vantage point over everything you have in flight. lookout gathers recent activity from git, GitHub, AI coding sessions, and files supplied by other tools, groups it into workstreams you define, and reports how recently each one moved and whether every input it relies on is healthy.

lookout is at an early stage, and nothing is ready to install yet. Reading git repositories needs git 2.36 or later, and reading GitHub needs the [GitHub CLI](https://cli.github.com/), logged in to github.com (`gh auth login`) or with `GH_TOKEN` set. Reading Claude Code sessions needs the configuration directories that hold them, listed as `config_dirs`, and git, which places each session in the repository at its working directory when lookout reads. [docs/vision.md](docs/vision.md) describes the dashboard it is the data layer for.

Run `lookout snapshot` once a day to keep a baseline of how often new work appears, how it gets placed, and how stale your workstreams are. The first snapshot of a date is kept, so running it again later that day changes nothing.

## License

[MIT](LICENSE)
