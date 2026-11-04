---
name: rune-extension
description: >
  Build, test, verify end to end with xsandbox, and package Rune extensions
  written with the Go or Python SDK. Loads Rune's published guides to the
  extension runtime, permissions, both SDKs and git packages, plus the rules
  the host enforces. Use when the user wants to create, debug, test or publish
  a Rune extension or package, or asks how Rune extensions work.
---

# Rune extensions

Rune's published guides to extensions, the SDKs and packages are appended
below under Reference. They are the source of truth: read the sections that
apply before writing code. This part adds the workflow and the rules that are
easy to get wrong.

This skill covers standalone extensions shipped as git packages. Rune's own
first-party extensions are built and released from the Rune repository; follow
that repository's instructions for them.

## Choose the integration
- **runectl:** a script, hook or one-off integration. No code to ship.
- **Plugin:** a Rune feature should call an existing CLI. A package with a
  `bin/` executable and a config overlay, no SDK.
- **Extension:** the user needs commands, key bindings, editor events, a
  persistent UI or a process that lives as long as the workspace.

Use the SDK the user asks for or the repository already uses. Otherwise use
Go: it has the complete API and a component framework for in-terminal UIs.
Python is in beta, needs no build step and adapts Rich and Textual. There is
no Rust SDK yet.

## Learn the API from the SDK
The guides show the shape of an extension, not every signature. Do not guess
names or signatures; read them.
- **Go:** run `go get github.com/unstablebuild/rune-go-sdk@latest`. Do not
  copy a version number from the guides. Then read `AGENTS.md` and
  `examples/snippets/` under
  `$(go env GOMODCACHE)/github.com/unstablebuild/rune-go-sdk@<version>/`, and
  look up `extensionapi`, `textapi`, `browserapi` and the other API packages
  with your code-navigation tools or `go doc`.
- **Python:** depend on `rune-sdk` in `pyproject.toml`, then read the
  installed package; `uv run python -c 'import rune_sdk; print(rune_sdk.__file__)'`
  prints where it is. The SDK's `examples/snippets` and the Theme Builder
  extension linked from the Python guide are complete references.
- **Responses:** `runectl <command> --format json` prints a real response
  before you write code against it.

## Layout
Follow the snippets example:

```
<repo>/
  config.yaml               package overlay: requirements, extensions.<id>
  main.go | main.py         metadata, handshake and wiring only
  <feature>.go | .py        a handler that takes SDK interfaces and holds the logic
  <feature>_test.go | tests/
  go.mod, go.sum            Go 1.25 or newer
  pyproject.toml, uv.lock   Python 3.11 or newer
```

## Rules the host enforces
Rune stops an extension at the handshake unless its metadata has:
- an extension ID equal to the id Rune started it under: the
  `extensions.<id>` key in `config.yaml` and the `<id>` in
  `extensions start <id>`;
- a non-empty developer ID, developer email, developer key and extension name;
- at least one permission.

While it runs:
- Every accessor is gated on a declared permission, and a call into an
  undeclared one is rejected. Declare exactly the permissions the code uses:
  the user is asked to approve each one.
- The setup function wires capabilities, registers commands and handlers, and
  returns. Never block in it. Run long-lived work in a goroutine bound to the
  `ctx` it receives, which stays live until shutdown, or in an asyncio task in
  Python.
- Stdout carries the handshake. Log to stderr only: `slog` in Go, which the
  SDK configures, or `logging` with `stream=sys.stderr` in Python.
- Return wrapped errors from command handlers; Rune shows them to the user.
  Notify only for successes, or for failures an event handler cannot return.

## Reach the workspace only through the SDK
The extension runs on the user's machine even when the workspace is on
another host (`ssh://`, `rune://`) or exists only in memory (`docs:///`).
Go's `os`, `os/exec` and `io/fs`, and Python's `open`, `pathlib`, `os`,
`shutil` and `subprocess`, reach the extension's machine, not the workspace.
Never use them for workspace files, commands, Rune's configuration or its
data directory. They seem to work in a local workspace and in xsandbox, where
the extension starts in the workspace directory, and break everywhere else.

| To | Go | Python | Permission |
|---|---|---|---|
| Read and write workspace files | `ws.FileSystem(ctx)`: `OpenFile`, `Stat`, `ReadDir`, `MkdirAll`, `Remove`, `URI` | `w.file_system()` | `permfs` |
| Show a file to the user | `ws.ResourceOpener(ctx)` | `w.resource_opener()` | `permopen` |
| Run a command | `ws.Executor(ctx)` | `w.executor()` | `permexec` |
| Read configuration | `ws.Config(ctx)` | `await w.config()` | `permcfg` |
| Keep state across sessions | `ws.Storage(ctx)` | `w.storage()` | `permstore` |
| Find a file the package ships | `ws.FindInstalledResource`, `ws.FindInstalledExecutable` | | `permfs` |

- The executor runs a command on the workspace host, in the workspace's
  environment, and the user approves what it runs.
- `ws.Config(ctx)` fetches the configuration Rune loaded, through
  `configrpc.FetchConfig`: the defaults, the user's `config.yaml` or
  evaluated `config.star`, and the workspace's `.rune/config.yaml` overlay.
  Parsing `$RUNE_DATADIR/config.yaml` yourself misses the defaults,
  `config.star` and the overlay.

## Configuration
- The config passed to setup is the extension's own `extensions.<id>.config`
  block as it was at start. Give every setting a default in the overlay and
  tolerate missing keys.
- `ws.Config(ctx)` holds the shared settings and the extension's own block,
  under `extensions.<id>.config`; other extensions' blocks are left out.
  Fetch it again whenever you need the current value.
- On macOS and Linux, Rune sends running extensions `SIGUSR2` whenever it
  reads its config file again, such as before each `runectl screenshot`.
  Both SDKs ignore it by default. To apply the change, replace the handler
  with the standard library from the setup function: in Go, `signal.Notify`
  as Reloading configuration shows; in Python,
  `asyncio.get_running_loop().add_signal_handler(signal.SIGUSR2, on_reload)`.
- `rune-sdk` 0.1.1 and earlier install no `SIGUSR2` handler, and Python exits
  on an unhandled one. With those versions, always install a handler, even
  one that ignores the change.
- Python's `w.config()` caches its first fetch. Fetch the new configuration
  with `rune_sdk.api.config.Client(w.raw_channel()).fetch()`.
- If the extension opens its own tabs with `WindowManager.Tab`, register a
  resource opener for their URI scheme so Rune can restore them when the
  workspace reloads.

## Unit test
- Write table-driven tests of the handler against fakes of the SDK
  interfaces. Go: `storagestub` for storage, `comptest` for components and
  `handlertest` for handlers. Python: each API package's `testing` module and
  `rune_sdk.tui.testing`.
- Go: `go mod tidy && go build ./... && go vet ./... && go test ./...`.
  Python: `uv lock && uv run pytest`.

## Verify end to end with xsandbox
xsandbox runs the extension against Rune's own extension host: the same
runner, handshake, TLS, token auth, permission checks and gRPC services, with
a headless browser in which the windows and tabs the extension opens are live.
A Starlark spec drives it and asserts what it does. This is how you verify
the extension yourself; do not hand the user an untested extension.

**Find it:** `command -v xsandbox`. The xsandbox package installs it in
`$RUNE_DATADIR/bin`, which is on the `PATH` of every command Rune runs. If it
is missing, stop and ask the user to run `pkg install xsandbox` in the Rune
console, then check again. Do not build or `go install` it yourself.

**Run it** from the repository root. Pass a built binary, or an interpreter
and script. A Go package directory or a bare `main.py` fails: the sandbox has
no `go` or `uv` package to run them through. Paths must not contain spaces.

```
# Go
go build -o "${TMPDIR:-/tmp}/<id>" . && xsandbox --spec xsandbox_spec.star -- "${TMPDIR:-/tmp}/<id>"
# Python
uv sync && xsandbox --spec xsandbox_spec.star -- "$PWD/.venv/bin/python" "$PWD/main.py"
```

Exit status 0 passes; 1 is a failed expectation, a timeout or a crash; 2 is a
usage error. A failure names the spec line and shows expected against
observed. `--verbose` streams the extension's stderr, and a failed run keeps
its temp directories, including `extension-stderr.log` and any crash report.
Log lines the host prints are not failures; the PASS or FAIL line is.

**Write the spec** in `xsandbox_spec.star` at the repository root:

```python
expect_metadata(id = "<id>", permissions = ["permcmd", "permnoti"])  # the exact set
config({"greeting": "hello"})       # the extensions.<id>.config block

c = expect_command("<command>")
invoke_command(c, args = ["world"])
expect_rpc("browser.Notifications/Notify", where = {"msg": contains("hello")})

wait_idle("200ms")
assert_no_unexpected_rpcs()
```

- Discover the calls to assert instead of guessing them. Write the
  metadata, one action, `wait_idle` and `assert_no_unexpected_rpcs()`, then
  run it. The failure lists every call the extension made, with its fields.
  Turn the calls that show the intended behavior into `expect_rpc` lines.
  Match only the fields that matter, with `contains()` or `regex()` for values
  that vary such as temp paths. Repeat for each command and event.
- Keep `assert_no_unexpected_rpcs()` last so extra calls fail the spec, such
  as a second window. Pass `ignore = [...]` only for noise such as
  `browser.EventPublisher/Publish`, the redraw requests.
- The extension starts at the first step after `expect_metadata` and
  `config`. A spec without one never starts it and passes without checking
  anything.
- `expect_metadata` fails unless the permissions match exactly. It takes each
  permission's string value: `permfs` FileSystem, `permexec` Execute,
  `permpty` Terminal, `permwm` BrowserWindowManager, `permopen`
  BrowserResourceOpener, `permnoti` Notifications, `permint` Interrupt,
  `permed` Editor, `permcmd` Commands, `permstore` Storage, `permsyntax`
  SyntaxTree, `permcfg` Config, `permlsp` LSP, `permdap` Debugger, `permllm`
  LLM, `permpkg` Packages, `permsearch` Search.
- Each `expect_*` blocks until the earliest unconsumed matching call arrives,
  or its `timeout` expires, and consumes that call.

Spec builtins:

| Need | Builtins |
|---|---|
| Drive the extension | `invoke_command(c, args = [...])`, `invoke_repl_command(r)` after `expect_repl_command(name)`, `publish_event("selection", content = ...)` and other editor events, `open_resource(o, uri)` and `complete_resource(o, prefix)` after `expect_resource_opener(scheme)` |
| Check a UI | `w = expect_window(method = "browser.WindowManager/Floating")` (or `Split`, `Tab`, `Bar`), `out = render(w, width = 100, height = 30)`, then `if "text" not in out: fail(out)`; `send_key(w, "<down><enter>")` with handlertest key tokens; `render_graphics(...)` for images |
| Set up the host | `fs.write(path, content)` in the workspace; `stub_rpc(method, where = ..., respond = {...})` or `respond_error = {"code": ..., "message": ...}` to script answers and failures |

LSP, syntax, debugger and LLM calls answer empty by default; script what the
code needs with `stub_rpc`. Commit the spec, and run it from the test suite,
skipped when xsandbox is not installed, as Theme Builder's
`tests/test_xsandbox.py` does.

## Try it in Rune
xsandbox does not show real language servers, models, permission prompts or
the GUI's appearance. Once the spec passes, have the user start the extension.
You cannot run the console's `extensions` command; give the user the exact
line, with an absolute path and the id from the metadata:

```
extensions start <id> /abs/path/to/<package-dir|main.py> --config '{"key":"value"}'
```

- Point a Go extension at its main package directory, not at `main.go`.
- The matching language package must be installed. If it is not, the user
  runs `pkg install go` or `pkg install python` first.
- After each change, ask the user to run `extensions restart <id>`.
- Read the log yourself rather than asking for it. Each extension's stderr and
  Rune's `source=rune` lifecycle lines go to a `rune-extension-*.log` file in
  the OS temp directory; `ls -t "${TMPDIR:-/tmp}"/rune-extension-*.log` lists
  the newest first. JSON records also reach `$RUNE_DATADIR/debug.log`.
- In GUI mode, check a UI with `runectl screenshot` and inspect the PNG.
- If a capability fails without a prompt, check the declared permissions. If
  the user denied it before, they can clear it with `authorizer list` and
  `authorizer revoke <decision>`.

## Package and publish
Put `config.yaml` at the repository root:

```yaml
requirements:
  - go                                            # or python
extensions:
  <id>:
    path: '$RUNE_DATADIR/lib/$RUNE_PKG_ID'           # Go: the main package directory
    # path: '$RUNE_DATADIR/lib/$RUNE_PKG_ID/main.py' # Python: the entry script
    config:
      <setting>: <default>
```

- Rune aborts the install when `config.yaml` is missing or empty, or when
  `path` names something it cannot start.
- Commit `go.sum` or `uv.lock`. Rune runs Go modules in read-only module mode,
  and runs Python with `uv run --locked` when the lock is present.
- Push to any host Rune can clone over HTTPS, and tag releases such as
  `v1.2.0` so users can pin them. Users install with
  `pkg install <host>/<owner>/<repo> [version]`.
- Tutorials and sounds ship in the same overlay; see the Packages guide.

## Done means
1. The build, vet and tests pass.
2. An xsandbox spec covers every command, event handler and window, ends
   with `assert_no_unexpected_rpcs()`, and passes.
3. The metadata id, the `config.yaml` key and the `extensions start` id
   match, and the permissions match the accessors the code calls.
4. You searched the code for `os`, `os/exec` and `io/fs`, or `open`,
   `pathlib`, `os`, `shutil` and `subprocess`, and no use touches workspace
   files, commands, configuration or the data directory.
5. The user started the extension, and you read its log and found no errors.
   For a UI, you inspected a screenshot.
6. You told the user which console commands are still theirs to run.