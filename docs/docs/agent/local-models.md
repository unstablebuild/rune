---
sidebar_position: 6
sidebar_label: Local Models (beta)
title: Local Models
---

# Local Models <span className="badge badge--secondary" style={{fontSize: '0.5em', verticalAlign: 'middle'}}>Beta</span>

Rune can run language models on the same host as your workspace, with no
API key and no data going to a third-party provider. Download a model,
install the local model server once, and point Rune at it: the same
private model list appears alongside your hosted providers, with streaming
and tool calling intact.

This guide covers what local models can do, how to install the server,
how to download and run models, and how to tune them for your hardware.

## Why run a model locally

- **Privacy**: prompts and code stay on the workspace host and never go to
  a third-party model provider.
- **No cost or rate limits**: once a model is downloaded, you can use it
  as much as you like, offline.
- **Runs where your workspace runs**: local models work on macOS and
  Linux, on CPU or GPU. For an [SSH workspace](../learn/ssh.md) the model
  runs on the remote host, so inference happens next to your code, not on
  your laptop. See [Server requirements](#server-requirements) for the
  per-platform GPU details.

Local models are first-class in Rune: they show up in the model list
alongside hosted providers, support streaming, tool calling, and (for
models that ship a vision projector) image inputs.

## What you need

Two things: the local model server, and a model to run.

- **The local model server.** Install it once from the
  [console](../learn/console.md) with
  [`pkg install`](../learn/console.md#installing-packages-with-pkg):

  ```
  pkg install llama-server
  ```

  Rune manages this server for you after that. It starts a server on
  demand when you first use a local model, keeps it warm for follow-up
  requests, and stops it when it goes idle. You never launch, configure,
  or connect to it by hand. Install it in the workspace where you will run
  models: for an [SSH workspace](../learn/ssh.md), run the command there so
  the server lands on the remote host. If you try to use a local model
  before the server is installed, Rune tells you to run the command above.

- **A model in GGUF format.** GGUF is the standard single-file format
  for quantized local models, and it is what nearly every community model
  on the Hugging Face Hub publishes. You also need enough RAM (or VRAM)
  to hold the model: as a rough rule of thumb, a model quantized to
  `Q4_K_M` needs a little more memory than its file size on disk. A 7B
  model at `Q4_K_M` is around 4.5 GB; a 3B model is closer to 2 GB.

:::info[Why a separate server?]
Under the hood, Rune runs your local models through
[llama.cpp](https://github.com/ggml-org/llama.cpp)'s model server. Keeping
it as an installable package means Rune can ship a build tuned for your
platform and GPU, and pick up upstream improvements without a full Rune
upgrade. You still manage everything from inside Rune; the `models local`
commands and `models.local` configuration below are all you touch.
:::

### Server requirements

The published `llama-server` package is built the same way as llama.cpp's
own release binaries, which sets a floor on how old a host it will run on:

- **macOS**: macOS **13.3** (Ventura) or newer, on Apple Silicon or Intel.
  Metal GPU acceleration is included.
- **Linux**: a **glibc 2.35** or newer runtime (for example Ubuntu 22.04+,
  Debian 12+, Fedora 36+, or a comparably recent distribution) on x86-64 or
  arm64. The build carries its own C++ runtime, so no separate `libstdc++`
  install is needed, and it picks the best CPU instruction set for the host
  automatically.

If your host is older than this — an long-term-support server on an older
glibc, say — the packaged server will not start. In that case, build
`llama-server` yourself from
[llama.cpp](https://github.com/ggml-org/llama.cpp) on that host and point
Rune at your binary with the `models.local.server_bin_path` setting (see
[Advanced tuning](#advanced-tuning)). Rune then manages your hand-built
server exactly as it would the packaged one.

## Downloading a model

Use the `models local` command from the [Rune console](../learn/console.md). To pull
a model, give it a reference in the familiar `owner/repo:tag` form. Any GGUF model
from [Hugging Face](https://huggingface.co/models?library=gguf) can be downloaded like this:

```
models local download unsloth/gemma-3n-E2B-it-GGUF:Q4_K_M
```

A progress bar tracks the download. When it finishes, Rune prints where
the files landed and reminds you to list them.

A few things worth knowing about references:

- The part after `:` is the **quantization tag** (here `Q4_K_M`). Lower
  numbers mean smaller files and lower memory use at some cost to
  quality. `Q4_K_M` is a good default; try `Q5_K_M` or `Q6_K` if you have
  the memory to spare, or a `Q3` / `Q2` variant if you are tight.
- When you omit the host, Rune assumes **Hugging Face**
  (`huggingface.co`). These are all equivalent:

  ```
  models local download unsloth/gemma-3n-E2B-it-GGUF:Q4_K_M
  models local download hf.co/unsloth/gemma-3n-E2B-it-GGUF:Q4_K_M
  models local download huggingface.co/unsloth/gemma-3n-E2B-it-GGUF:Q4_K_M
  ```

- You can pin an exact build by digest instead of a tag:

  ```
  models local download huggingface.co/owner/repo@sha256:abc123…
  ```

- Any OCI v2 registry works, not just Hugging Face, as long as the
  repository is packaged as a GGUF model (the same layout Ollama uses).
  Point the reference at the registry's host:

  ```
  models local download my-registry.example.com/team/qwen2.5-coder-GGUF:Q4_K_M
  ```

  A generic container image (an app image, a base OS image, and so on) is
  not a model; even though it lives in an OCI registry, it has no GGUF
  weights, so Rune cannot run it.

Run the `help models local download` command to see this reference from
inside Rune.

:::tip
Whatever the registry, the repository must contain GGUF weights; that is
the one universal requirement.

On **Hugging Face** specifically, those weights are served through an OCI
endpoint that is only opened for GGUF repositories. If a Hugging Face pull
is rejected as not GGUF-compatible, look for a sibling repo whose name ends
in `-GGUF`; those are the community-converted, ready-to-run builds.
:::

## Listing and deleting models

See everything you have downloaded:

```
models local list
```

Each entry is shown by its full reference (for example
`huggingface.co/unsloth/gemma-3n-E2B-it-GGUF:Q4_K_M`). That reference is
also the model's name everywhere else in Rune.

Remove a model and reclaim disk space:

```
models local delete huggingface.co/unsloth/gemma-3n-E2B-it-GGUF:Q4_K_M
```

Models are stored in an Ollama-compatible cache under
`$RUNE_DATADIR/models` by default. You can change that location with the
`models.local.models_cache_dir` setting (see [Configuration](#configuration)).

## Using a local model

A downloaded model behaves like any other model in Rune. Local models use
the `llamacpp` provider, so their full `provider/model` name is `llamacpp/`
followed by the reference you see in `models local list`, for example
`llamacpp/huggingface.co/unsloth/gemma-3n-E2B-it-GGUF:Q4_K_M`.

To make a local model the default for new chats, point the `default`
[model alias](./intro.md#model-aliases) at it with the `models alias`
command from the [Rune console](../learn/console.md):

```
models alias set default llamacpp/huggingface.co/unsloth/gemma-3n-E2B-it-GGUF:Q4_K_M
```

Aliases resolve anywhere a model is accepted, so you can point any of them
(`default`, `query`, `compact`, `dream`) at a local model, and clear it
again with `models alias remove default`. Run `models` to list every
available `provider/model` name, including your local ones next to your
hosted providers, so you can mix and match: a local model for quick,
private edits and a hosted one for heavier work.

## Configuration

All local-model settings live under `models.local`. The defaults are
chosen to "just work" on most machines; you usually only need to touch
these when tuning for your hardware or a specific model.

```yaml tab
models:
  local:
    # Where downloaded models are stored. Empty uses $RUNE_DATADIR/models.
    models_cache_dir: ""
    # Layers to offload to the GPU. -1 offloads as many as the GPU fits.
    # Set to 0 to run purely on the CPU.
    n_gpu_layers: -1
    # Generation threads. 0 lets the server pick a good value.
    threads: 0
    # Enable flash-attention when your GPU supports it (saves memory).
    flash_attention: false
    # Cap tokens generated per reply. 0 means "until the model stops".
    max_output_tokens: 0
    # How long a model server may sit idle before Rune stops it. Empty
    # uses the default (5m). Accepts any Go duration, e.g. "10m", "1h".
    idle_timeout: ""
    # How many local model servers may run at once. When starting another
    # would exceed this, Rune stops the least-recently-used idle one.
    # 0 uses the default (1).
    max_servers: 0
```

```python tab
"models": {
    "local": {
        "models_cache_dir": "",
        "n_gpu_layers": -1,
        "threads": 0,
        "flash_attention": False,
        "max_output_tokens": 0,
        "idle_timeout": "",
        "max_servers": 0,
    },
},
```

The most useful knobs:

- **`n_gpu_layers`**: controls GPU offloading. The default `-1` offloads
  the whole model to the GPU, which is what you want for speed. If a model
  is too large for your VRAM, lower this number to offload only some
  layers (the rest run on the CPU), or set it to `0` to run entirely on
  the CPU.
- **`flash_attention`**: turn this on if your GPU supports it to reduce
  memory use, especially at large context sizes.
- **`max_output_tokens`**: a hard cap on reply length. Leave it at `0`
  unless you want to keep responses short.
- **`idle_timeout`**: how long a warm model server may sit unused before
  Rune stops it to free memory. Raise it if you switch back and forth and
  want to avoid reload pauses; lower it to reclaim memory sooner.
- **`max_servers`**: how many local models Rune keeps loaded at once. The
  default is `1`. Raise it to `2` or more (memory permitting) if you
  routinely alternate between local models and want both kept warm.

### Sampling

Sampling controls how the model picks its next token: higher
temperature is more creative, lower is more deterministic. Sensible
defaults are applied automatically; override them under
`models.local.sampling` only if you know you want to:

```yaml tab
models:
  local:
    sampling:
      temperature: 0.8
      top_k: 40
      top_p: 0.95
      min_p: 0.05
```

```python tab
"models": {
    "local": {
        "sampling": {
            "temperature": 0.8,
            "top_k": 40,
            "top_p": 0.95,
            "min_p": 0.05,
        },
    },
},
```

For coding and tool use, a lower `temperature` (for example `0.2`–`0.4`)
usually gives steadier, more predictable output. Any sampling field you
leave out keeps its built-in default.

### Advanced tuning

These settings are rarely needed. Reach for them only when a specific
model or a custom server build calls for it.

```yaml tab
models:
  local:
    # Prompt-processing batch size. 0 uses the server default.
    batch_size: 0
    # Minimum matching chunk (tokens) for KV-cache reuse across turns.
    # 0 uses prefix matching only.
    n_cache_reuse: 0
    # Override the chat template embedded in the GGUF. Empty uses the
    # model's own template.
    chat_template: ""
    # Loopback address the model server binds to. Empty uses 127.0.0.1.
    host: ""
    # How long to wait for a server to become ready before giving up.
    # Empty uses the default (120s).
    startup_timeout: ""
    # Explicit path to the model server binary. Empty resolves the one
    # installed by `pkg install llama-server`, then your PATH.
    server_bin_path: ""
    # Extra command-line arguments passed through to the model server.
    extra_args: []
```

```python tab
"models": {
    "local": {
        "batch_size": 0,
        "n_cache_reuse": 0,
        "chat_template": "",
        "host": "",
        "startup_timeout": "",
        "server_bin_path": "",
        "extra_args": [],
    },
},
```

- **`chat_template`**: only set this if a model's built-in template is
  broken or missing. Most GGUF models carry a working template already.
- **`server_bin_path`** and **`extra_args`**: escape hatches for running
  a hand-built server or passing flags Rune does not expose. Leave them
  empty unless you know you need them. Set `server_bin_path` to an absolute
  path to a `llama-server` you built yourself — for example on a host whose
  glibc is older than the packaged build requires (see
  [Server requirements](#server-requirements)). Rune still manages that
  binary's lifecycle for you.

## Capabilities and limits

- **Tool calling** works with instruction-tuned models whose chat
  template declares tools (Qwen, Gemma, DeepSeek, Hermes, and other
  ChatML-family models). Base, non-instruction-tuned models will ignore
  tool definitions; pick an `-it` / `-Instruct` variant for agent use.
- **Image inputs** are supported when a model ships a paired vision
  projector (an `mmproj` GGUF). Without one, images are dropped and only
  the text of a message is sent.
- **Warm reuse**: a loaded model serves requests one at a time, and Rune
  keeps the previous turn's cache so follow-up messages that share a
  prefix respond faster. Rune starts a server per model on demand, keeps
  it warm, and stops it once it goes idle; tune that with `idle_timeout`
  and `max_servers` (see [Configuration](#configuration)).

## Workspace search

Rune indexes the code of every workspace you open, so you can describe
the code you are looking for instead of guessing the words it uses.
Search from the command prompt with
[`searchsemantic`](../learn/search.md#search-by-meaning), which floats the
results in a picker with a preview of why each matched:

```
searchsemantic where is the retry backoff computed
```

or from the [Rune console](../learn/console.md) with `search`:

```
search where is the retry backoff computed
```

Results are ranked by both meaning and words, best first, each opening at
the most relevant line. Results that trail far behind the best ones are
listed under *Weaker matches*. A query shaped like an identifier, such as
`search CodeLookup.definition`, jumps to where that symbol is
defined.

Semantic indexing is off by default. Set `search.semantic_enabled` to
`true` (`True` in Starlark) and restart Rune to enable it.
Regexp indexing (`search.regex_enabled`) and symbol indexing
(`workspace.symboldb`) are independent of this opt-in.

Once enabled, keyword search works as soon as the workspace is scanned.
Searching by meaning needs the [local model server](#what-you-need) and a small
embedding model. The first `searchsemantic` offers to set both up for
you, and to upgrade the local model server when the installed version
is too old to run the model; or download the model yourself, once:

```
models local download ggml-org/embeddinggemma-2-GGUF:Q8_0
```

Until it is downloaded, `search` answers by words alone and a
notification reminds you of the command. Once it is, Rune encodes the
workspace in the background and meaning search turns on by itself. The
embedding model runs in its own server, so searching never unloads the
chat model you are using, and it stops after a couple of idle minutes.

**What is indexed**: source files, a repository's docs and settings
(Markdown, YAML, TOML and the like) and Jupyter notebooks, cell by cell.
Rune leaves out what git ignores, hidden folders, dependencies and build
output (`node_modules`, `vendor`, `build`, `dist`, `target`, ...),
generated and minified files, data files, license files, keys and
credential files, and files too large to have been written by hand.
Saving a file re-indexes just that file.

Two more commands manage the index:

```
search status    # files by state, vectors, and what was left out
search reindex   # rebuild the index from scratch
```

Scripts and tools can run the same search from a Rune terminal with
[`runectl search semantic`](../develop/runectl.md):

```
runectl search semantic --limit 5 where is the retry backoff computed
```

Search is configured under `search`. The independent `regex_enabled` and
`semantic_enabled` settings replace the old `enabled` switch:

```yaml tab
search:
  # Off, `searchregex` reads every file instead of using its index.
  regex_enabled: true
  # Opt in to the semantic index for `search` and `searchsemantic`.
  semantic_enabled: false
  # The local embedding model that encodes code and queries.
  model: "huggingface.co/ggml-org/embeddinggemma-2-GGUF:Q8_0"
  # Characters a stretch of indexed code holds at most (whole lines).
  code_stretch: 1800
  # Workspace-relative folders never read, besides what git ignores.
  excluded_folders: []
  # File extensions never read, e.g. ["sql", "csv"].
  excluded_extensions: []
  # How long the embedding model stays loaded after the last search.
  # Empty uses the default (2m).
  idle_timeout: ""
```

```python tab
"search": {
    "regex_enabled": True,
    "semantic_enabled": False,
    "model": "huggingface.co/ggml-org/embeddinggemma-2-GGUF:Q8_0",
    "code_stretch": 1800,
    "excluded_folders": [],
    "excluded_extensions": [],
    "idle_timeout": "",
},
```

The `search` [model alias](./intro.md#model-aliases) picks the model
queries are encoded with. Leave it unset to use `search.model`.

## Choosing a model

Some starting points that run well locally:

- **Qwen2.5-Coder** (3B / 7B): strong coding and tool use; great default
  for agent work on a laptop.
- **Gemma** instruction-tuned builds: solid general-purpose chat with
  tool support.

Look for the `-GGUF` repositories on the Hugging Face Hub, pick an
instruction-tuned (`-it` or `-Instruct`) variant for agent and chat use,
and start with the `Q4_K_M` tag. Move up to `Q5_K_M` or `Q6_K` if you
have the memory and want a quality bump.

## Troubleshooting

- **"install llama-server via the console"**: the local model server is
  not installed on the workspace host yet. Run `pkg install llama-server`
  from the [console](../learn/console.md) (on the remote host for an SSH
  workspace), or set `models.local.server_bin_path` to your own build,
  then try the model again.
- **The server fails to start on Linux (missing `GLIBC_…`, or exits
  immediately)**: your host's glibc is older than the packaged build
  requires. Build `llama-server` yourself on that host from
  [llama.cpp](https://github.com/ggml-org/llama.cpp) and set
  `models.local.server_bin_path` to it (see
  [Server requirements](#server-requirements)).
- **"not compatible with the llama.cpp OCI endpoint"**: the repository
  does not serve GGUF weights through the OCI endpoint. Use a
  community-converted `-GGUF` repository instead.
- **Out of memory or very slow**: the model is too large for your GPU.
  Lower `n_gpu_layers`, turn on `flash_attention`, or download a smaller
  quantization (a lower `Q` tag or a smaller parameter count).
- **First reply is slow, later ones are fast**: expected. The first
  request starts and loads the model server; it then stays warm until it
  goes idle. Raise `idle_timeout` if reloads bother you.
- **The model ignores tools**: you are likely running a base model. Pull
  the instruction-tuned (`-it` / `-Instruct`) variant.

## Acknowledgements

Local inference in Rune stands on the shoulders of
[llama.cpp](https://github.com/ggml-org/llama.cpp) and the GGML community.
Our heartfelt thanks to Georgi Gerganov and the entire community of
contributors whose work on high-performance kernels, the GGUF model
ecosystem, samplers, and chat templating makes fast, private, on-device
models possible. Rune's local model support would not exist without their
engineering and generosity.
