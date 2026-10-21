# lookout

A single vantage point over everything you have in flight. lookout gathers recent activity from git, GitHub, AI coding sessions, and files supplied by other tools, groups it into workstreams you define, and reports how recently each one moved and whether every input it relies on is healthy.

lookout is at an early stage, and nothing is ready to install yet. Reading git repositories needs git 2.36 or later, and reading GitHub needs the [GitHub CLI](https://cli.github.com/), logged in to github.com (`gh auth login`) or with `GH_TOKEN` set. [docs/vision.md](docs/vision.md) describes the dashboard it is the data layer for.

## License

[MIT](LICENSE)
