# Keep Personal Details Out

This is a public repository. Keep personal and identifying details out of everything that ships or is published — code, comments, test fixtures, commit messages, issues, and pull requests.

## What not to introduce

- Real names and email addresses.
- Absolute home-directory or machine-specific paths (e.g. `/home/<user>/…`, `/Users/<user>/…`). Use relative paths, repo-relative paths, or placeholders.
- Machine, host, or network names.
- Private or internal project names, and references to private repositories or internal tooling.
- Credentials of any kind — tokens, keys, passwords — even example-looking ones.
- Anything describing how a particular organization runs its issue tracker, what it permits, or what tooling a contributor has configured for their job. lookout supports trackers generically; one employer's setup is configuration, never repository content.

## Sensible boundaries

- **Necessary attribution is fine.** The LICENSE copyright line and normal git authorship are expected and stay.
- **Generic examples are fine.** Placeholder `owner/repo` slugs, invented ticket keys, and example config that carry no real personal data are encouraged.
- When an example needs a path or name, use an obvious placeholder (`owner/repo`, `/path/to/config.yml`, `example.com`, `your-name`, `ABC-123`) rather than a real one.
- **Test fixtures are a live surface here rather than a theoretical one.** The specs seed repository slugs, branch names, session working directories, and ticket keys — exactly the shape of string that carries a real project name, checkout path, or employer identifier when written from habit.

## The disclosure sweep

`just sweep` checks the tracked tree and every commit not yet on a remote against a denylist each maintainer keeps at `$XDG_CONFIG_HOME/lookout-sweep/patterns`, outside the repository, since a committed denylist would publish the names it guards. The pre-push hook runs the same sweep over what the push publishes: each ref being pushed, its name, an annotated tag's tagger and message, and every commit the destination does not hold, including its author's and committer's names and email domains. An email's local part is not checked, since git authorship publishes the author's own address by design.

Text composed for GitHub never passes through that hook. Before posting a pull request title or body, an issue, a review or comment, or a squash-merge commit message, write it to a file and run `just sweep-file <path>` on it (paths resolve from the repository root).

A refusal names a match in an unpushed commit by its commit. A later commit cannot clear it, because the push still publishes the earlier one: rewrite that unpushed commit instead. A name match prints the name, term included, so keep sweep output out of anything public.

A token sweep cannot catch prose. Re-read anything describing trackers or work settings against the last bullet under What not to introduce; a passing sweep does not settle it. When the pattern file is absent or lists no terms, the sweep warns and passes, so a warning means nothing was checked.

## Why

A public repo is read by anyone. Scattering personal or environment-specific details leaks information, dates the content to one machine or person, and makes the project read as someone's private workspace rather than a reusable tool. Keeping it impersonal keeps it portable and respectful of privacy.
