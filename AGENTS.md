# AGENTS.md

This file provides project instructions for coding agents working in this repository.

## Project Overview

This repository contains the Rune editor/TUI application plus the integrated Rune Agent codebase.

Key entrypoints:

- `cmd/rune` — the main Rune application
- `cmd/rune-agent` — the Rune Agent extension binary and packages
- `auth` — the account contract shared with the API server
- `internal/` — every non-`main` package (`ide`, `text`, `term`, `workspace`,
  `handler`, `component`, `llm`, `cell`, `debug`, ...)

The repository also contains substantial TUI/editor infrastructure built on `github.com/unstablebuild/rune-go-sdk`, and many UI/component patterns mirror the conventions used in the sibling `blue` repository.

New non-`main` packages go under `internal/`. The supported extension API is the
separate `rune-go-sdk` module.

### Language integrations

Before researching, planning, implementing, or reviewing support for a new
programming language, read
[`cmd/rune/docs/docs/develop/languages.md`](cmd/rune/docs/docs/develop/languages.md)
in full. Treat that guide as the required integration checklist, not optional
background reading. It defines how language extensions, project and tool
management, LSP initialization, Tree-sitter assets, core symbol resolution,
debugging, packaging, and the language-specific Rune-core test suites fit
together.

Do not consider a new language or language feature complete unless it follows
the guide's testing pattern. In particular, add or extend the corresponding
language suite in Rune core for each affected layer, using the real language
server, debugger adapter, native syntax queries, and committed `testdata`
project where the guide requires them.

`auth` is the one exception: the account claims, roles and endpoint paths in it
are a wire contract with the API server, which lives in a different module and
therefore cannot import `internal/`. Both ends must deserialize the same token,
so the package is public to keep them from drifting. Keep it that way — nothing
else here is public API, and `auth` should stay free of editor concerns.

## Common Commands

```bash
# Main builds
make                 # Build the repository binaries
make debug           # Build with race detection where applicable

# Individual binaries
make rune
make rune-agent

# Testing
make test            # Run tests with race detector
make test-e2e        # Also run the e2e suites (requires docker)
make coverage        # Generate coverage report

# Code quality
make lint            # Run golangci-lint
make format          # Run go fmt
make generate        # Regenerate generated files
make license

# Good pre-submit validation
make generate && make lint && make test
```

Run a single test:

```bash
go test -race -run TestName ./path/to/package/
```

## License headers

`make license` only adds headers to files missing one. When you add new
files, force-apply the canonical header so a file that already carries
a license-looking but non-canonical comment is rewritten:

```bash
bluectl license -f LICENSE_HEADER <new files>
```

## Repository Architecture

### Main Rune application

- `cmd/rune/` contains the main editor application.
- `cmd/rune/ide/extension/runner.go` is the key integration point for built-in extensions, including Rune Agent.

### Rune Agent

- `cmd/rune-agent/extension/` — workspace extension registration and event handling
- `cmd/rune-agent/dialogue/dialoguemanager/` — conversation orchestration and persistence
- `cmd/rune-agent/dialogue/dialoguetui/` — TUI chat rendering and interaction
- `cmd/rune-agent/llm/` — provider abstraction and model registries
- `cmd/rune-agent/agent/` — agent loop, tools, skills, prompts, task handling
- `cmd/rune-agent/memory/` — memory retrieval and durable memory compilation support
- `cmd/rune-agent/streamiterator/` — gRPC stream iteration helpers

### Key Rune Agent patterns

- Thread safety via `sync.Map` and `sync.Mutex`
- Context cancellation propagated through streaming operations
- Configuration is exposed through the extension config system
- The agent relies heavily on semantic code navigation tools and structural search

## Goroutines

All spawned goroutines must run their body under
`debug.CapturePanicReport` (from this repository's `internal/debug` package) so
that any panic is captured into a crash report and logged instead of
silently taking down the process. This applies to every goroutine
spawn site, including short-lived helpers, background workers, and
goroutines started from production code paths.

```go
go debug.CapturePanicReport(func() {
    // goroutine body
})
```

Do not wrap goroutine bodies in ad-hoc `recover()` blocks in place of
`debug.CapturePanicReport`; the helper is the single source of truth
for panic capture and crash-report generation.

## How to implement a `tui.Component` or `tui.Handler`

The UI elements in this repository are built on `github.com/unstablebuild/rune-go-sdk`.
That SDK is centered around three abstractions:

1. **Component** — drawable/resizable UI element
2. **Handler** — component that also handles events and manages cursor/selection
3. **Event loop** — polls terminal events, routes them, redraws, and flushes output

### Interface flavors

#### `component.Responsive` and `handler.Responsive`

Designed to allow collection components (List, FrameUnion, Container, ResponsiveList, etc.)
to vertically compose responsive children given only the collection width during `Resize`.

- `Height(width int) int` must return a **hint**
- Do **not** assume `Resize` will be called with that exact height
- If the component receives less height than needed, it must scroll or truncate vertically
- Text wrapping must happen at the width boundary; do not rely on horizontal scrolling for text

#### `component.Scrollable` and `handler.Scrollable`

Used when the runtime should display a scroll bar next to the component/handler.
Implement this for vertically scrollable content or for collections of responsive children.

#### `component.Floating` and `handler.Floating`

Used for components that can calculate ideal dimensions from known content.
`Dimensions` should return ideal size, not merely echo the last `Resize` dimensions.

## Testing Guidance

- Prefer table-driven tests
- `tui.Component` implementations should use the `comptest` package when appropriate
- `tui.Handler` implementations should use the `handlertest` package when appropriate
- See examples in `cmd/rune-agent/dialogue/dialoguetui/*_test.go`
- Tests that spawn a real external process (a language server, a debugger, a
  shell in a pty, a docker container) or depend on the host environment (a
  toolchain on `PATH`, the network) must carry the `//go:build e2e` tag so they
  stay out of `make test`, which must remain hermetic and fast. Run them with
  `make test-e2e`. The `e2e` suffix in a filename is not the criterion: a test
  that only uses in-process fakes or checked-in fixtures belongs in `make test`
  regardless of its name. Keep shared fakes and harnesses in untagged files so
  the hermetic tests in the same package can still use them.

### `handlertest.SequenceTestCase.InputSequence`

`InputSequence` is a **concatenation of tokens** with no separators. Each token becomes one `KeyComb`.

Token forms:

1. **Single character**: any rune except `'<', '>', '\\', ' '`
2. **Escapes**:
   - `\\` → `\`
   - `\>` → `>`
   - `\<` → `<`
3. **Named key**: `<name>` (case-insensitive), where `name` is one of:
   - keys: `f1..f12`, `insert`, `delete`, `home`, `end`, `pgup`, `pgdn`, `up`, `down`, `left`, `right`, `tab`, `enter`, `esc`, `space`, `backspace`
   - mouse: `mouse-left`, `mouse-middle`, `mouse-right`, `mouse-release`, `mouse-wheel-up`, `mouse-wheel-down`
4. **Modified key**: `<mods-key>` where `mods` may use:
   - `c|ctrl`, `s|shift`, `a|alt`, `m|meta`
   - examples: `<c-f1>`, `<m-left>`, `<a-enter>`, `<c-s-a-tab>`
5. **Modifier-only tokens**:
   - `<ctrl> <shift> <alt> <meta>` and supported combos

Invalid forms include:

- literal space (use `<space>`)
- raw `>`
- raw `<` without a matching `>`
- raw `\` or `\` followed by anything other than `\`, `<`, `>`

## Code comments

Comments should explain *why*, not *what* or *how* — the code already
shows what it does. Do not pepper code with narrative comments.

- Do not add comments that restate the adjacent code in prose.
- Do not add comments describing what you just changed (e.g.
  `// now also handles X`, `// removed Y`).
- Do not add docstrings, header banners, or type annotations to code
  you did not change.
- Docstrings and export API doc comments **must** explain the
  contract and clarify edge cases. Don't explain how it works or what
  calls it.

## Bug-fix policy

If you are fixing a bug, practice TDD:

1. add a test that reproduces the bug
2. fix the bug
3. verify the new test passes

Do not mark a task complete without validating the fix.

## Commit Messages

When creating commit messages for this repository, match the existing subject style in recent history.

- Use short, imperative, sentence-style subjects.
- The subject **must** start with one of these allowed verbs: `Add`, `Update`,
  `Fix`, `Remove`, `Upgrade`, `Revert`, or `Refactor`. This is an exhaustive
  allowlist, not a set of examples.
- Keep the subject focused on the user-visible or code-level change (the "what").
- Add a short description on the reason why the change is being introduced (the "why").
- The commit subject **must** be shorter than 90 characters.
- Every commit body line **must** be wrapped at 90 characters or fewer.
- Do not include routine validation command lists in commit messages unless explicitly requested
- For performance-oriented commits, include measured before/after timings or percentages when available

Good examples:

- `Add AGENTS.md project instructions support`
- `Update plan skill with critical loop exit section`
- `Fix userMsgIdx after compaction to prevent index out of range panic`
- `Update /clear confirmation to display inline`

## Review checklist

Before considering a task done, verify:

- code compiles
- relevant tests pass, whole suite via `make test` passes.
- formatting/linting expectations are satisfied
- any new behavior is covered by tests when practical
