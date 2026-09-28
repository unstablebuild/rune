# xsandbox

`xsandbox` impersonates the Rune editor's side of the extension
protocol so an extension binary — built with `rune-go-sdk` or an SDK
port in another language — can be exercised end-to-end without
running Rune.

It serves two purposes:

1. **Conformance**: verify that an SDK implementation speaks the
   extension protocol correctly (stdio handshake, TLS, auth token,
   gRPC services, streams).
2. **Benchmarking**: measure handshake and RPC timings so SDK
   implementations in different languages can be compared.

## Usage

```bash
xsandbox --spec spec.star -- ./bin/my-extension [ext-args...]
```

The sandbox launches the extension with the same environment Rune
sets (`RUNE_SOCKET`, `RUNE_DATADIR`, `RUNE_CERT`, `RUNE_TOKEN`,
`GOTUI_LOG_LEVEL`), performs the stdio JSON handshake, hosts every
workspace gRPC service on a unix socket with a self-signed TLS cert
and per-RPC token auth (exactly like Rune), and evaluates the spec.

Exit codes: `0` success, `1` expectation failure / timeout /
extension crash, `2` usage or setup error.

Flags:

| flag | description |
| --- | --- |
| `--spec path` | Starlark spec (required) |
| `--datadir dir` | host data dir (default: temp dir) |
| `--workspace dir` | workspace root (default: temp dir) |
| `--insecure` | disable TLS and token auth (debugging) |
| `--insecure-transport` | disable TLS only |
| `--insecure-auth` | disable per-RPC token auth only |
| `--timeout d` | overall run timeout (default 60s) |
| `--expect-timeout d` | default per-expectation timeout (default 10s) |
| `--grace d` | SIGTERM→SIGKILL grace period (default 5s) |
| `--verbose` | stream extension stderr + diagnostics |
| `--bench` | print per-method latency table |
| `--json path` | write the machine-readable report |

The extension's stderr is always captured to
`<datadir>/extension-stderr.log`. Temp dirs are kept for inspection
when a run fails.

## Spec language

Specs are Starlark, evaluated **top to bottom**. Builtins fall into
two phases:

- **Setup phase** (before the first expectation/action):
  `expect_metadata`, `config`. The extension is launched lazily by
  the first run-phase builtin.
- **Run phase**: everything else. Each `expect_*` builtin **blocks**
  until it is satisfied or its timeout expires; a failure aborts the
  spec with the source position and a diff of expected vs. observed.

### Matching model

Expectations form an ordered sequence in the spec, but each one is
satisfied by the **earliest unconsumed** recorded RPC that matches —
RPCs that arrived before the expectation executed still count. Every
match consumes its RPC, so two identical `expect_rpc` lines match two
distinct calls. `assert_no_unexpected_rpcs` fails if unconsumed RPCs
remain (minus `ignore` globs).

### Builtins

```python
# -- setup ----------------------------------------------------------
expect_metadata(id = "com.example.hello",
                permissions = ["permcmd", "permnoti"])  # exact set; id required
config({"greeting": "hello"})       # served in the handshake and config.Config

# -- files (any phase) ----------------------------------------------
fs.write("README.md", "# readme\n") # write under the workspace root;
                                    # also triggers workspace.Scheme/Watch

# -- scripted responses ----------------------------------------------
# Persistent responders for unary RPCs; first installed match wins.
stub_rpc("workspace.Files/Read", respond = {"data": "..."})
stub_rpc("browser.Notifications/Notify",
         where = {"msg": "boom"},
         respond_error = {"code": "unavailable", "message": "outage"})

# -- expectations -----------------------------------------------------
# Methods use proto service names: "workspace.Files/Read",
# "text.Editor/SubscribeCommand", "browser.Notifications/Notify", ...
# `where` matches proto fields (dotted paths, nested subset match,
# numbers normalized). Values may be exact or predicates.
expect_rpc("browser.Notifications/Notify",
           where = {"msg": contains("hello"), "level": present()},
           timeout = "5s")
expect_rpc("workspace.Files/Read", respond = {...})  # install + wait

h = expect_command("hello")          # command registration arrived
r = expect_repl_command("shell")     # REPL command registration arrived
o = expect_resource_opener("chat")   # resource opener for chat:// arrived

# -- actions (host -> extension) --------------------------------------
invoke_command(h, args = ["world"])  # dispatch and wait for completion
invoke_repl_command(r, args = [])
open_resource(o, "chat://host/1")    # ask for the content and show it
publish_event("open", uri = "file:///tmp/x", content = "...")

# -- rendering installed handlers -------------------------------------
# Handlers installed via Split/Bar/Tab/Floating/Open run in a real
# headless browser. expect_window blocks until the install stream for
# method is observed and returns a window handle; render draws the
# whole browser (compositing every installed handler) to a string;
# send_key delivers a handlertest-style key sequence to the focused
# handler and returns {"handled": bool, "quit": bool}.
w = expect_window(method = "browser.WindowManager/Split", timeout = "5s")
out = render(w, width = 80, height = 24)   # rendered grid, trailing blanks trimmed
if "count=0" not in out:                   # assert with `in` / fail()
    fail("unexpected render:\n" + out)
res = send_key(w, "<space>")               # tokens per handlertest grammar
out = render(w)                            # re-render to observe the change

# -- synchronization ---------------------------------------------------
wait_idle("1s")                       # no RPC activity for 1s
assert_no_unexpected_rpcs(ignore = ["workspace.Files/*"])
```

Predicates: `present()`, `contains(s)`, `regex(pattern)`.
For streams, `where` matches any client message on the stream.

`expect_window` never consumes the install RPC, so it composes with
`expect_rpc` and `assert_no_unexpected_rpcs`. `render` defaults to an
80x24 viewport; `send_key` key tokens follow the same grammar as
`handlertest.SequenceTestCase.InputSequence` (e.g. `<space>`, `<c-d>`,
`<enter>`).

`open_resource` calls the extension's resource opener the way Rune does
when it restores a tab: the uri must have the opener's scheme, and the
call returns once the extension has returned the content of that uri,
failing with the error the extension returned. The content is shown as
the tab of the uri in the focused window, so `render` draws it and
`send_key` reaches it. Like any stream, the registration itself can also
be matched with `expect_rpc("text.Editor/SubscribeResourceOpener")`, and
the content's stream with `expect_rpc("text.Editor/OpenResource")`.

## Report

`--json` emits handshake duration, wall time, per-expectation
results, observed metadata, unexpected RPCs, and per-method latency
(count/min/avg/p50/p95/max) so runs can be diffed across SDK
implementations.

## Notes

- The extension command is split on spaces (same as Rune's runner),
  so paths must not contain spaces.
- The spec's `expect_metadata(id=...)` must match the extension's
  metadata id; the handshake rejects mismatches, mirroring Rune.
- `internal/fixtureext` is a reference extension used by the e2e
  tests; its spec lives in `testdata/pass.star`.
- `testdata/sampleext` is a standalone SDK extension (its own Go
  module, compiled by the e2e tests with the local toolchain). Its
  `api` command exhaustively exercises EVERY method of every interface
  reachable through `extensionapi.Workspace` (all 64 LSP methods, all
  34 debugger methods, filesystem, executor, terminal, storage, window
  manager, notifications, resource opener, interrupter, editor, parser,
  LLM, config, plus the Commands/RawConn accessors), and its `wm`
  command installs `tui.Handler`s through the window manager, so the
  `testdata/sample_api.star` spec `expect_rpc`s all 143 methods in the
  DSL itself (the Go e2e test only checks the spec passes), so it
  doubles as an end-to-end exercise of the spec language while
  asserting whole-API per-method RPC capture, including
  handler-carrying streams. `WindowManager/Floating` is the sole
  method the `api` command never calls, so `sample_api_diff.star`
  uses that gap as its negative-coverage assertion.
- The four browser services (`WindowManager`, `ResourceOpener`,
  `Notifications`, `EventPublisher`) are served by a real headless
  `browser.Component` in synchronous mode, so handlers installed via
  `Split`/`Bar`/`Tab`/`Floating` are live and can be rendered and
  driven with `render` / `send_key` (see the Spec language section).
  `sampleext`'s `counter` command splits a panel that renders
  `count=N` and increments on `<space>`; `testdata/sample_render.star`
  asserts the exact rendered output before and after key input.
