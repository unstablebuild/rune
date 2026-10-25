# SDK

Rune has SDKs for Go and Python. Both build **extensions**:
standalone programs that Rune launches as child processes and talks to over a
private, local channel. Through that channel an extension registers commands,
reacts to editor and workspace events, drives windows and tabs, reads and
writes files, calls language servers and models, and serves persistent UIs.

The [Extensions guide](../extensions.md) covers the runtime model, the
permission system, and the `extensions` console command. This section covers
authoring: writing an extension with each SDK, iterating on it inside a
workspace, testing it, and packaging it for `pkg install`.

## Pick your SDK

| SDK | Repository | Notes |
| --- | --- | --- |
| [Go](./go.md) | [rune-go-sdk](https://github.com/unstablebuild/rune-go-sdk) | Complete API surface, plus a component framework for in-terminal plugins. |
| [Python](./python.md) | [rune-python-sdk](https://github.com/unstablebuild/rune-python-sdk) | Complete API surface, plus Rich and Textual adapters. Ships as source, with no build step. |
| Rust | [rune-rust-sdk](https://github.com/unstablebuild/rune-rust-sdk) | Coming soon. Async Rust on tokio, with an early-stage port under way. |

Whichever you choose, an extension has the same shape:

1. **Metadata.** Declare the extension's id, version, and the permissions it
   needs.
2. **Setup.** The SDK performs the handshake and hands your setup function a
   workspace handle whose typed clients call into Rune. Every call is gated
   on a permission you declared; an undeclared capability is rejected.
3. **Return.** Wire capabilities, register commands and event handlers, and
   return. The SDK owns the process lifetime until the workspace closes.

## The development loop

You never run your extension directly: Rune launches it and connects to it.
Start a work-in-progress extension in a workspace from the
[console](../../learn/console.md):

```
extensions start myext /path/to/entrypoint --config '{"key":"value"}'
```

The entrypoint can be a compiled binary, a `.py` script, a Go main package
directory containing `go.mod` and `go.sum`, or a `.rs` file inside a Cargo
project. Source and package entrypoints run through the matching language
package's toolchain automatically (see
[source and package extensions](../extensions.md#source-and-package-extensions)),
so you can iterate without a build step. The rest of the lifecycle is managed
from the same command:

| Command | What it does |
| --- | --- |
| `extensions status` | List every workspace extension with status, pid, and uptime. |
| `extensions info <id>` | Detailed state for one extension, including restart counts. |
| `extensions logs <id> [--tail <N>]` | What the extension wrote to stderr, where startup failures and crashes show up. |
| `extensions restart <id>` | Relaunch with the current config, the fastest way to pick up a change. |
| `extensions stop <id>` | Stop it. |

Permissions are enforced at two layers: a call into a capability the
extension never declared is rejected outright, and the first time it uses a
declared capability Rune prompts the user to allow or deny it. If a
capability seems unreachable, check `authorizer list` for a lingering
**deny always** decision and clear it with `authorizer revoke <permission>`.
Command approvals are bound to the files a command runs, so a command that
prompts again after you approved it usually runs a different file than the
one you approved, or one that changed: compare its prompt with
`authorizer executables`.

## Sample responses with runectl

[runectl](../runectl.md) wraps the same services the SDKs expose, and most of
its commands accept `--format json`. Running the equivalent command by hand
shows the exact shape of a response before you write code against it:

```bash
runectl storage list --format json
runectl lsp hover MySymbol --format json
```

## Ship it

Extensions are distributed from a **public git repository** and installed
with `pkg install <host>/<owner>/<repo>`, for example
`pkg install github.com/<owner>/<repo>`. Any host Rune can clone over HTTPS
works, including GitHub, GitLab, Bitbucket, and self-hosted servers. Rune
clones the repo, reads a `config.yaml` at its root that registers the
extension under `extensions.<id>`, provisions the language toolchain it
requires, and runs your extension from source, so one repository serves
every platform. Each guide ends with the packaging recipe for its language;
see [Packages](../packages.md#distributing-from-a-git-repository) for the
full format and how versions (latest, tags, commits) work.
