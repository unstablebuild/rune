---
sidebar_position: 2
sidebar_label: Python SDK (beta)
title: Python SDK
---

# Python SDK <span className="badge badge--secondary" style={{fontSize: '0.5em', verticalAlign: 'middle'}}>Beta</span>

The [Python SDK](https://github.com/unstablebuild/rune-python-sdk) covers
Rune's complete extension API and adapts the Python TUI ecosystem (Rich and
Textual) for serving UIs into Rune windows. Install it with:

```bash
pip install rune-sdk
```

Requires Python 3.11 or newer. The TUI adapters are extras:

```bash
pip install "rune-sdk[rich]"     # RichAdapter
pip install "rune-sdk[textual]"  # TextualAdapter
```

## A basic extension

The example below is the wiring half of the SDK's
[`examples/snippets`](https://github.com/unstablebuild/rune-python-sdk/tree/main/examples/snippets),
a complete extension that adds a `snippets` command for storing and reusing
text. `main.py` does only metadata and setup; the logic lives in a
`Snippets` handler typed against SDK clients so it stays testable.

```python
# main.py
import logging
import sys

from examples.snippets.handler import extend
from rune_sdk.extension import Metadata, Permission, serve_workspace_extension

META = Metadata(
    developer_id="rune-sdk-examples",
    developer_email="your@email.com",
    developer_key="1234",
    extension_id="snippets",
    extension_name="Snippets",
    extension_version="0.1.0",
    permissions=frozenset(
        {
            Permission.STORAGE,
            Permission.EDITOR,
            Permission.COMMANDS,
            Permission.FILE_SYSTEM,
            Permission.BROWSER_RESOURCE_OPENER,
            Permission.BROWSER_WINDOW_MANAGER,
            Permission.NOTIFICATIONS,
        }
    ),
)

if __name__ == "__main__":
    logging.basicConfig(level=logging.INFO, stream=sys.stderr)
    serve_workspace_extension(extend, META)
```

```python
# handler.py (the setup body: wire and register, then return)
import asyncio
from typing import Any

from rune_sdk.api import text
from rune_sdk.extension import Workspace


async def extend(w: Workspace, _config: dict[str, Any]) -> None:
    s = Snippets(w)  # holds w.storage(), w.editor(), w.file_system(), etc.

    # React to editor events in a background task.
    sub = await w.editor().subscribe_events(
        text.EventType.SELECTION,
        text.EventType.FLUSH,
        text.EventType.CLOSE,
    )
    asyncio.create_task(s.handle_events(sub))

    manual = text.CommandManual(
        name="snippets",
        summary="Insert, edit, copy, or delete reusable text snippets.",
        synopsis="[insert|edit|copy|delete] <name>",
    )
    await w.register_command(manual, s.handle_command, s.complete)
```

`serve_workspace_extension` writes the metadata to Rune, reads back the
connection config, and serves until shutdown. The `Workspace` accessors
return typed clients into the host that share one channel:

| Package | Capability | Accessor |
| --- | --- | --- |
| `rune_sdk.extension` | Handshake, `Workspace`, command registration | (none) |
| `api.text` | Editor and text operations, editor events, commands | `editor()`, `commands()`, `register_command()` |
| `api.storage` | Document storage and persistence | `storage()` |
| `api.browser` | Windows, tabs, resource opening, notifications, interrupts | `window_manager()`, `resource_opener()`, `notifications()`, `interrupter()` |
| `api.workspace` | URI resolution, file operations, watchers, processes, ptys | `file_system()`, `executor()`, `terminal()` |
| `api.semantic` | Language-server operations (definition, references, rename, diagnostics, and more) | `lsp()` |
| `api.llm` | LLM access (models, token counting, messages) | `llm()` |
| `api.debug` | Debug-adapter (DAP) control | `debugger()` |
| `api.syntax` | Tree-sitter structural search and queries | `parser()` |
| `api.config` | Workspace configuration | `config()` |

Every accessor is gated on the matching `Permission` declared in `Metadata`.

## Declare dependencies in pyproject.toml

A Python extension is a regular Python project. Rune runs a `.py`
entrypoint with `uv run`, using the `uv` that the `python` package
provides and the nearest `pyproject.toml` above the script as the
project. `uv` builds the environment it declares on first launch:

```toml
# pyproject.toml
[project]
name = "snippets"
version = "0.1.0"
requires-python = ">=3.11"
dependencies = ["rune-sdk"]
```

Depend on an extra (`rune-sdk[rich]`, `rune-sdk[textual]`) when your
extension uses the TUI adapters. Rune pins `uv` to the extension's own
project, so the environment comes from this `pyproject.toml`, never from
whatever project the workspace happens to be in. Commit the generated
`uv.lock` with a packaged extension. When it is present, Rune launches the
project with `uv run --locked`, so startup fails rather than silently changing
the dependency graph. A source extension without `uv.lock` remains convenient
for early local development, but it is not reproducible packaging.

## Complete Rich extension

[`rune-extension-themebuilder`](https://github.com/unstablebuild/rune-extension-themebuilder)
is a complete, packaged extension that uses `rune-sdk[rich]`. Its
`themebuilder` command opens a centered floating window with a live code
preview and an interactive 16-color ANSI palette.

Use it as a reference for the parts that a minimal wiring example leaves out:

- [`main.py`](https://github.com/unstablebuild/rune-extension-themebuilder/blob/main/main.py)
  registers a command, installs a floating handler, requests redraws through
  the interrupter, and sends notifications.
- [`themebuilder.py`](https://github.com/unstablebuild/rune-extension-themebuilder/blob/main/themebuilder.py)
  embeds `RichAdapter` in a custom handler that implements keyboard and mouse
  input, preferred floating dimensions, dynamic rendering, and cleanup. Rich
  renders the interface; the handler performs coordinate-aware mouse hit
  testing around that layout.
- [`tests/test_themebuilder.py`](https://github.com/unstablebuild/rune-extension-themebuilder/blob/main/tests/test_themebuilder.py)
  tests interaction and rendering with regular `pytest` and
  `rune_sdk.tui.testing`.
- [`spec.star`](https://github.com/unstablebuild/rune-extension-themebuilder/blob/main/spec.star)
  and
  [`tests/test_xsandbox.py`](https://github.com/unstablebuild/rune-extension-themebuilder/blob/main/tests/test_xsandbox.py)
  exercise the real extension handshake, command registration, floating
  window, rendering, key input, notifications, and clipboard integration
  through `xsandbox`.
- [`config.yaml`](https://github.com/unstablebuild/rune-extension-themebuilder/blob/main/config.yaml)
  and
  [`pyproject.toml`](https://github.com/unstablebuild/rune-extension-themebuilder/blob/main/pyproject.toml)
  show the complete Git package and Python project manifests.

The extension also demonstrates a useful adapter boundary: `RichAdapter` is
responsible for drawing Rich renderables into Rune cells, while the enclosing
handler remains responsible for Rune events, application state, hit testing,
and host API calls.

## Run and debug it

Start your script in a workspace from the
[console](../../learn/console.md):

```
extensions start snippets /path/to/snippets/main.py --config '{"key":"value"}'
```

Rune runs the `.py` entrypoint through `uv` as described above (see
[source and package extensions](../extensions.md#source-and-package-extensions)),
so there is no build step: edit the script and `extensions restart snippets`.

Use `extensions logs snippets` to read what the process wrote to stderr;
configure `logging` to write there, as in the example above. The full
lifecycle and the permission prompts are covered in
[the development loop](./index.md#the-development-loop).

## Test it

Keep `main.py` to metadata and setup and put the logic in a handler typed
against SDK clients, as in the example above, so it can be exercised with
`pytest` in table-driven tests. Each API package ships a `testing` module
with fake-service harnesses, UI handlers are tested with the
`rune_sdk.tui.testing` harness (`draw_handler`, `run_handler_sequence`), and
the SDK's own
[`tests/e2e`](https://github.com/unstablebuild/rune-python-sdk/tree/main/tests/e2e)
shows extensions tested end to end against the `xsandbox` extension host. The
[Theme Builder tests](https://github.com/unstablebuild/rune-extension-themebuilder/tree/main/tests)
provide a smaller reference that combines focused handler tests with one
end-to-end Starlark behavior spec.

## Package it

A Python extension is distributed from a **public git repository**: no
archive to build, no launcher, no bundled interpreter, one repository for
every platform. Put the project (its `pyproject.toml` and sources) at the
repo root next to a `config.yaml` that requires the `python` package and
points `path` at the entry script:

```yaml title="config.yaml"
requirements:
  - python
extensions:
  snippets:
    path: '$RUNE_DATADIR/lib/$RUNE_PKG_ID/main.py'
    config:
      # defaults surfaced in the user's config, editable like any other setting
```

Push it to any host Rune can clone over HTTPS (GitHub, GitLab, Bitbucket, or
a self-hosted server) and users install it by its `<host>/<owner>/<repo>`
ID:

```
pkg install github.com/<owner>/<repo>
```

Rune clones the repo, installs the `python` package (which provides `uv`)
first because the overlay requires it, and runs `main.py` with `uv run`
against the checked-out `pyproject.toml`, which declares the extension's pip
dependencies. Tag a commit to give users a pinnable version. See the
[packages guide](../packages.md#distributing-from-a-git-repository) for the complete
walkthrough, including how tags and commits select a version.
