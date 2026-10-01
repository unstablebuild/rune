---
sidebar_position: 1
---

# Go SDK

The [Go SDK](https://github.com/unstablebuild/rune-go-sdk) covers Rune's
complete extension API. Install it into your module:

```bash
go get github.com/unstablebuild/rune-go-sdk
```

Requires Go 1.25 or newer.

## A basic extension

The example below is the wiring half of the SDK's
[`examples/snippets`](https://github.com/unstablebuild/rune-go-sdk/tree/main/examples/snippets),
a complete extension that adds a `snippets` command for storing and reusing
text. `main` does only metadata and wiring; the logic lives in a handler
typed against SDK interfaces so it stays testable.

```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
)

func main() {
	meta := extensionapi.Metadata{
		DeveloperID:      "rune-sdk-examples",
		DeveloperKey:     "1234",
		DeveloperEmail:   "your@email.com",
		ExtensionID:      "snippets",
		ExtensionName:    "Snippets",
		ExtensionVersion: "0.1.0",
		Permissions: extensionapi.NewPermissions(
			extensionapi.PermissionStorage,
			extensionapi.PermissionEditor,
			extensionapi.PermissionCommands,
			extensionapi.PermissionFileSystem,
			extensionapi.PermissionBrowserResourceOpener,
			extensionapi.PermissionBrowserWindowManager,
			extensionapi.PermissionNotifications,
		),
	}

	if err := extensionapi.ServeWorkspaceExtension(
		extensionapi.FuncWorkspaceExtension(run), meta,
	); err != nil {
		slog.Error("snippets exited", "error", err)
		os.Exit(1)
	}
}

// run wires capabilities, subscribes to editor events, and registers the
// command, then returns. ServeWorkspaceExtension owns the lifetime.
func run(ctx context.Context, ws *extensionapi.Workspace, cfg config.Config) error {
	s := newSnippets(
		ws.Storage(ctx),        // persist snippets across sessions
		ws.Editor(ctx),         // insert bodies at the cursor
		ws.FileSystem(ctx),     // back scratch buffers with real files
		ws.ResourceOpener(ctx), // open those buffers in a tab
		ws.WindowManager(ctx),  // focus the tab that was opened
		ws.Notifications(ctx),  // report success/failure to the user
	)

	events := []textapi.EventType{
		textapi.EventTypeSelection,
		textapi.EventTypeFlush,
		textapi.EventTypeClose,
	}
	if err := ws.Editor(ctx).SubscribeEvents(events, s); err != nil {
		return fmt.Errorf("subscribe events: %w", err)
	}

	manual := textapi.CommandManual{
		Name:     "snippets",
		Summary:  "Insert, edit, copy, or delete reusable text snippets.",
		Synopsis: "[insert|edit|copy|delete] <name>",
	}
	if err := ws.RegisterCommand(manual, s); err != nil {
		return fmt.Errorf("register command: %w", err)
	}
	return nil
}
```

`ServeWorkspaceExtension` writes the metadata to Rune, reads back the
connection config, and serves until shutdown. Each `Workspace` accessor
returns a typed client into the host:

| Package | Capability | Accessor |
| --- | --- | --- |
| `extensionapi` | Handshake, `Workspace`, command registration | (none) |
| `textapi` | Editor and text operations, editor events, commands | `Editor`, `RegisterCommand` |
| `storageapi` | Document storage and persistence | `Storage` |
| `browserapi` | Windows, tabs, resource opening, notifications | `WindowManager`, `ResourceOpener`, `Notifications` |
| `workspaceapi` | URI resolution, file operations, watchers, command execution | `FileSystem`, `Executor` |
| `semanticapi` | Language-server operations (definition, references, rename, diagnostics, and more) | `LSP` |
| `llmapi` | LLM access (models, token counting, messages) | `LLM` |
| `debugapi` | Debug-adapter control | `Debugger` |
| `syntaxapi` | Tree-sitter structural search and queries | `Parser` |
| `config` | Merged host configuration | `Config` |

Every accessor is gated on the matching permission declared in `Metadata`.

## Restore tabs across sessions

When a workspace is reloaded or reopened, Rune restores the window layout and
the files in it, but it cannot recreate the content of a tab your extension
created with `WindowManager.Tab`. It shows a placeholder tab with the same
URI, icon and name where the tab was, and asks your extension to open it
again once you register a resource opener for the scheme of those tab URIs:

```go
if err := ws.RegisterResourceOpener("snippets", s); err != nil {
	return fmt.Errorf("register resource opener: %w", err)
}
```

`s` implements `textapi.ResourceOpenHandler`, whose `OpenResource`
receives the tab URI and returns its content, a `browserapi.Handler`, the way
`WindowManager.Tab` would have been given it. Rune shows the content in the
placeholder's place, wherever the user keeps the tab by then, and closes it
once the tab is closed; do not create the tab or install it into a window
yourself. The URI remains the tab's identity, e.g. for
`WindowManager.SetTabActivity`. A returned error is shown on the
placeholder, which the user can close, and a later registration, e.g. after
a restart, is asked again. Only tabs that were shown in a tiled window are
restored.

A resource opener is not a command: it never appears in the command prompt.
If your extension restarts and registers the scheme again, the new
registration replaces the old one. Rune versions that predate resource
openers fail the registration with a `codes.Unimplemented` status.

## Run and debug it

Build your binary, then start it in a workspace from the
[console](../../learn/console.md):

```
extensions start snippets /path/to/snippets --config '{"key":"value"}'
```

To skip the build step during development, point `extensions start` at the
main package directory instead. The directory must contain `go.mod` and
`go.sum`; Rune runs it as `go -C <package-directory> run .` with the
[`go` package's](../../languages/go.md) toolchain (see
[source and package extensions](../extensions.md#source-and-package-extensions)).

```
extensions start snippets /path/to/snippets-package
```

Use `extensions logs snippets` to read what the process wrote to stderr
(write diagnostics with `slog`), and `extensions restart snippets`
to pick up a rebuild. The full lifecycle and the permission prompts are
covered in [the development loop](./index.md#the-development-loop).

## Test it

Keep `main` thin and put the logic in a handler typed against SDK
interfaces, as in the example above, so it can be exercised against fakes in
table-driven tests. For extensions that serve UIs, the SDK ships dedicated
harnesses: `comptest` for components and `handlertest` for handlers. The
[`examples/snippets`](https://github.com/unstablebuild/rune-go-sdk/tree/main/examples/snippets)
extension shows the layout and the patterns worth copying.

## Package it

A Go extension is distributed from a **public git repository**: no archive
to build and no per-platform binaries, because Rune compiles it from source
on the user's machine. Put the module at the repo root next to a
`config.yaml` that requires the `go` package and points `path` at the
main package directory:

```yaml title="config.yaml"
requirements:
  - go
extensions:
  snippets:
    path: '$RUNE_DATADIR/lib/$RUNE_PKG_ID'
    config:
      # defaults surfaced in the user's config, editable like any other setting
```

Commit both `go.mod` and `go.sum`: Rune runs the module in read-only module
mode, so the checked-out `go.sum` must already pin every dependency. Push it
to any host Rune can clone over HTTPS (GitHub, GitLab, Bitbucket, or a
self-hosted server) and users install it by its `<host>/<owner>/<repo>` ID:

```
pkg install github.com/<owner>/<repo>
```

Rune clones the repo, installs the `go` package (which provides the
toolchain) first because the overlay requires it, and runs the module as
`go -C <package-directory> run .` (see
[source and package extensions](../extensions.md#source-and-package-extensions)).
Tag a commit to give users a pinnable version. See the
[packages guide](../packages.md#distributing-from-a-git-repository) for the full format
and how versions work.
