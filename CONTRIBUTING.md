# Contributing to Rune

Thanks for your interest in improving Rune. This document covers the
licensing terms for contributions and the practical mechanics of sending a
change.

## Licensing

Rune is licensed under the GNU General Public License, version 3 or (at your
option) any later version; see [LICENSE](LICENSE) for the full text.

Contributions are accepted under that same license: unless you state otherwise,
anything you intentionally submit for inclusion in Rune is licensed under
GPL-3.0-or-later. You keep ownership of your contribution, and the Developer
Certificate of Origin below is how you certify that you have the right to
submit it.

## Sign your work

Rune uses the [Developer Certificate of Origin](DCO) rather than a contributor
license agreement. There is nothing to sign and no account to create. Instead
you certify the DCO once per commit, by adding a `Signed-off-by` trailer.

Git writes the trailer for you with `-s`:

```bash
git commit -s -m "Fix the thing"
```

which appends a line like:

```
Signed-off-by: Jane Developer <jane@example.com>
```

Use your real name and an address you can be reached at; they must match the
commit author. A status check verifies that every commit in a pull request
carries the trailer.

If you forget, amend the most recent commit with:

```bash
git commit -s --amend --no-edit
```

or sign off an entire branch at once with `git rebase --signoff main`, then
force-push.

All participation in the project is subject to our
[Code of Conduct](CODE_OF_CONDUCT.md).

## Ways to contribute

We're happy to review:

- Bug fixes, especially with a reproducing test (see the bug-fix policy
  below).
- Documentation fixes and improvements.
- Small, focused enhancements to existing features.

For anything larger — a new `tui.Component`/`tui.Handler`, a new
extension surface, or a behavior change to Rune Agent — please open a
discussion or issue first. Large PRs without prior agreement on the
approach are unlikely to be merged as-is, even if the code is correct.

Always add unit tests with your fixes and new features, and add integration tests when the
change crosses one or several package boundaries. Also, always test your changes manually
to confirm they do what you intended. Tests can encode our assumptions about how the
system around Rune behaves, and the only way to catch a wrong assumption is to run Rune
and try it.

## Development

```bash
make                 # Build the repository binaries
make debug           # Build with race detection where applicable

make rune
make rune-agent

make test            # Run tests with race detector
make test-e2e        # Also run the e2e suites (requires docker)
make coverage        # Generate coverage report

make lint            # Run golangci-lint
make format          # Run go fmt
make generate        # Regenerate generated files
```

Run a single test:

```bash
go test -race -run TestName ./path/to/package/
```

Before opening a PR, run:

```bash
make generate && make lint && make test
```

## Code conventions

- Goroutines must run their body under `debug.CapturePanicReport` so
  panics are captured into a crash report before crashing the
  process. See `AGENTS.md` for the exact pattern.
- Comments should explain the non-obvious *why*, not *what* or *how*. Don't add comments
  that restate the adjacent code, describe a diff ("now also handles
  X"), or add docstrings to code you didn't change.
- Prefer table-driven tests. `tui.Component` implementations should use
  `comptest`; `tui.Handler` implementations should use `handlertest`.

## Bug-fix policy

If you're fixing a bug, practice TDD:

1. Add a test that reproduces the bug.
2. Fix the bug.
3. Verify the new test passes.

PRs that fix a bug without a regression test are unlikely to be merged.

## License headers

New files need a license header. `make license` only adds headers to
files that don't already have one — if a file carries a stale or
non-canonical header, force-apply the canonical one:

```bash
bluectl license -f LICENSE_HEADER <new files>
```

[`bluectl`](https://github.com/unstablebuild/blue) is a tool we use for
various workflows. Install it with:

```bash
go install github.com/unstablebuild/blue/cmd/bluectl@latest
```

## Commit messages

Pull requests are squashed, so the PR title and description become the
commit message on `main`.

Match the existing style in `git log`:

- Short, imperative, sentence-style subject, starting with a
  capitalized verb: `Add`, `Update`, `Fix`, `Remove`, `Upgrade`, or
  `Revert`.
- Keep the subject on the user-visible or code-level change (the
  "what"), with no trailing period.
- Add a short body explaining *why* the change is being introduced,
  separated from the subject by a blank line.
- Wrap subject and body lines to 90 columns.
- Don't list the validation commands you ran unless asked.
- For performance work, include measured before/after numbers when you
  have them.

Good examples:

```
Add AGENTS.md project instructions support
Fix userMsgIdx after compaction to prevent index out of range panic
Update /clear confirmation to render inline instead of in a floating window
```

## AI-assisted contributions

Thus, we do not discriminate against the use of AI to contribute to Rune. What matters is
that there's a human in the loop who understands the change being proposed and can answer
questions about it. The more high-quality contributions you have, the higher you'll rank
in our review queue.

## Pull request expectations

- Keep a PR about one thing — don't mix a bugfix with a refactor or an
  unrelated feature.
- Include tests for behavior changes where practical.
- Include screenshots or recordings for visible TUI changes.
- Respond to review comments; that moves a PR forward faster than
  pinging maintainers directly.
