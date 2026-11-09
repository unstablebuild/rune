---
sidebar_position: 7
---

# Custom Model Provider

Rune can talk to any **OpenAI-compatible** HTTP endpoint as a first-class
model provider. Point it at a `base_url`, declare the models that endpoint
serves, and those models show up in Rune alongside the hosted providers
and your [local models](./local-models.md), with streaming and tool
calling intact.

This is the recommended way to use a self-hosted inference server (for
example a CUDA-accelerated `llama.cpp` server on a Linux GPU box), a
gateway/proxy in front of several providers, or any third-party API that
speaks the OpenAI wire format.

## When to use a custom provider

- **A shared or standalone inference server.** Rune's [local
  models](./local-models.md) run wherever the workspace runs (your machine,
  or the remote host for an SSH workspace). To share one inference server
  across people or workspaces, or to reach a GPU server that is not the
  workspace host, run a server yourself and connect to it as a custom
  provider.
- **A model or provider Rune does not ship a built-in for**, as long as it
  exposes an OpenAI-compatible `/v1/chat/completions` endpoint.
- **A local LLM runtime** you already run (Ollama, LM Studio, vLLM, and
  friends; see [Popular backends](#popular-backends)).
- **A gateway or proxy** (LiteLLM, an internal router) that fronts one or
  more upstream providers behind a single OpenAI-compatible URL.

## How it works

Under the hood the custom provider is an OpenAI **Chat Completions**
client (`/v1/chat/completions`), so any server that implements that
endpoint works. Three pieces of configuration drive it, all under
`models.custom`:

- **`url`**: the base URL of the server. Rune appends the standard
  OpenAI paths to it, so give it the API root (the part ending in `/v1`),
  not the full `/chat/completions` path.
- **`api_key`**: sent as a `Bearer` token in the `Authorization` header
  on every request. Many local servers ignore it; set a placeholder like
  `"sk-local"` when the server does not require auth.
- **`available_models`**: a map of **model name → context window** (in
  tokens). Each entry becomes a selectable model in Rune. The name is sent
  verbatim as the request's `model` field, so it must match what the
  server expects. The context-window number is what Rune uses to manage
  the conversation budget; set it to the model's real context length.

Because the catalog is explicit, the custom provider does **not** query
the server's `/v1/models` list; you declare exactly the models you want to
surface.

:::note
The custom provider uses the Chat Completions API, not the newer Responses
API. Reasoning-summary and Responses-only options do not apply to custom
models.
:::

## Configuration

Add a `models.custom` block to your config. A minimal example pointing at
a local server:

```yaml tab
models:
  custom:
    url: "http://localhost:8080/v1"
    api_key: "sk-local"
    available_models:
      my-model: 32768
```

```python tab
"models": {
    "custom": {
        "url": "http://localhost:8080/v1",
        "api_key": "sk-local",
        "available_models": {
            "my-model": 32768,
        },
    },
},
```

You can declare as many models as the server hosts. Each name → context
window pair is independent:

```yaml tab
models:
  custom:
    url: "http://localhost:8080/v1"
    api_key: "sk-local"
    available_models:
      qwen2.5-coder-7b: 32768
      llama-3.3-70b: 131072
```

```python tab
"models": {
    "custom": {
        "url": "http://localhost:8080/v1",
        "api_key": "sk-local",
        "available_models": {
            "qwen2.5-coder-7b": 32768,
            "llama-3.3-70b": 131072,
        },
    },
},
```

### Configuration reference

| Key                              | Type            | Description                                                                                 |
| -------------------------------- | --------------- | ------------------------------------------------------------------------------------------- |
| `models.custom.url`              | string          | Base URL of the OpenAI-compatible server (the `/v1` root). Required when models are declared. |
| `models.custom.api_key`          | string          | Bearer token sent on every request. Use a placeholder when the server needs no auth.        |
| `models.custom.available_models` | `map<string,int>` | Model name → context-window (tokens). Each entry becomes a selectable model.              |

:::caution
If you declare `available_models` without a `url`, Rune rejects the config
with `models.custom.available_models: ... require models.custom.url`. The
declared models would otherwise point at an empty base URL and fail at
request time.
:::

## Selecting a custom model

Custom models appear in the model list next to every other provider. Run
the `models` command from the [Rune console](../learn/console.md) to confirm they are
available.

Because a model name can exist under more than one provider, Rune
identifies models as `provider/name`. Your custom models use the `custom`
provider, so they are referenced as:

```
custom/my-model
custom/qwen2.5-coder-7b
```

A bare name (without the `custom/` prefix) resolves only when exactly one
provider exposes it; if the name is ambiguous, Rune asks you to prefix it
with the intended provider. To make a custom model the default for new
chats, point the `default` [alias](./intro.md#model-aliases) at its
qualified name:

```
models alias set default custom/qwen2.5-coder-7b
```

A model name may itself contain slashes, which is common for gateways that
namespace their catalog. Only the first `/` separates the provider, so
`custom/openrouter/free` selects the model named `openrouter/free` from the
`custom` provider.

## Popular backends

Any server that exposes an OpenAI-compatible `/v1/chat/completions`
endpoint works. The defaults below are typical; check your server's docs
for the exact host, port, and whether it requires an API key.

### llama.cpp server (`llama-server`)

The same project that powers Rune's [local models](./local-models.md)
ships a standalone server. Build it with CUDA (or another GPU backend) on
your GPU box, then run:

```
llama-server -m ./qwen2.5-coder-7b-instruct-q4_k_m.gguf \
  --host 0.0.0.0 --port 8080 -ngl 999
```

The OpenAI-compatible endpoint is at `/v1`. The `model` name `llama-server`
expects is the one you start it with (often a path or alias):

```yaml tab
models:
  custom:
    url: "http://localhost:8080/v1"
    api_key: "sk-local"
    available_models:
      qwen2.5-coder-7b-instruct: 32768
```

```python tab
"models": {
    "custom": {
        "url": "http://localhost:8080/v1",
        "api_key": "sk-local",
        "available_models": {
            "qwen2.5-coder-7b-instruct": 32768,
        },
    },
},
```

### Ollama

[Ollama](https://ollama.com) serves an OpenAI-compatible API at
`/v1`. Pull a model with `ollama pull qwen2.5-coder:7b`, then:

```yaml tab
models:
  custom:
    url: "http://localhost:11434/v1"
    api_key: "ollama"
    available_models:
      qwen2.5-coder:7b: 32768
```

```python tab
"models": {
    "custom": {
        "url": "http://localhost:11434/v1",
        "api_key": "ollama",
        "available_models": {
            "qwen2.5-coder:7b": 32768,
        },
    },
},
```

The model name must match what `ollama list` shows.

### vLLM

[vLLM](https://docs.vllm.ai) is a high-throughput GPU server. Start it with
`vllm serve <model>` and it exposes `/v1` (default port `8000`). The
`model` name is the Hugging Face repo you served:

```yaml tab
models:
  custom:
    url: "http://localhost:8000/v1"
    api_key: "sk-local"
    available_models:
      Qwen/Qwen2.5-Coder-7B-Instruct: 32768
```

```python tab
"models": {
    "custom": {
        "url": "http://localhost:8000/v1",
        "api_key": "sk-local",
        "available_models": {
            "Qwen/Qwen2.5-Coder-7B-Instruct": 32768,
        },
    },
},
```

### LM Studio

[LM Studio](https://lmstudio.ai) runs a local OpenAI-compatible server
(default port `1234`) once you enable it from the **Developer** tab. Use
the model identifier shown in the app:

```yaml tab
models:
  custom:
    url: "http://localhost:1234/v1"
    api_key: "lm-studio"
    available_models:
      qwen2.5-coder-7b-instruct: 32768
```

```python tab
"models": {
    "custom": {
        "url": "http://localhost:1234/v1",
        "api_key": "lm-studio",
        "available_models": {
            "qwen2.5-coder-7b-instruct": 32768,
        },
    },
},
```

### LiteLLM and other gateways

A gateway such as [LiteLLM](https://docs.litellm.ai) presents one
OpenAI-compatible URL in front of many upstream models. Point `url` at the
gateway, set `api_key` to the gateway's key, and list the model names the
gateway routes:

```yaml tab
models:
  custom:
    url: "http://localhost:4000/v1"
    api_key: "sk-gateway-key"
    available_models:
      gpt-4o-mini: 128000
      claude-3-5-sonnet: 200000
```

```python tab
"models": {
    "custom": {
        "url": "http://localhost:4000/v1",
        "api_key": "sk-gateway-key",
        "available_models": {
            "gpt-4o-mini": 128000,
            "claude-3-5-sonnet": 200000,
        },
    },
},
```

## Tool calling and capabilities

- **Tool calling** works when the backing model and server support
  OpenAI-style function/tool calls. For agent use, pick an
  instruction-tuned model; base models ignore tool definitions.
- **Context window** is governed entirely by the number you declare in
  `available_models`. If you set it higher than the server actually
  supports, long conversations will be rejected by the server; set it too
  low and Rune trims context earlier than necessary. Use the model's real
  context length.
- **Streaming** is on by default, just like the hosted providers.

## Troubleshooting

- **`custom provider not configured`**: a `custom/...` model was selected
  but `models.custom.url` is empty. Set the `url`.
- **`models.custom.available_models: ... require models.custom.url`**:
  you declared models without a `url`. Add the base URL.
- **401 / 403 from the server**: the `api_key` is wrong or missing. Set
  the token the server expects (or a placeholder for servers that ignore
  it).
- **404 on requests**: `url` likely includes the full path. Use the API
  root ending in `/v1`; Rune appends `/chat/completions` itself.
- **Model not found / wrong model**: the name in `available_models` must
  match exactly what the server expects in the request `model` field.
- **The model ignores tools**: you are likely pointing at a base model.
  Serve an instruction-tuned (`-it` / `-Instruct`) variant.

## See also

- [Local Models](./local-models.md): run GGUF models with Rune's built-in
  engine (no server required).
- [Rune Console](../learn/console.md): the `models` command lists every available
  model, including your custom ones.
