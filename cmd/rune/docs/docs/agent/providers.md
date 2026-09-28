---
sidebar_position: 4
---

# Providers

Rune Agent works with a set of **model providers**. A provider is a service
that hosts models and authenticates your requests. Rune ships with built-in
support for the major cloud providers, plus [local models](./local-models.md)
and [custom OpenAI-compatible endpoints](./custom-model-providers.md). Every
model, whatever its provider, shows up in one flat list and is selected the
same way.

This guide covers the hosted cloud providers: how to onboard them, what each
one offers, and how to configure them in detail.

## The built-in providers

Rune has six built-in cloud providers:

| Provider | Sign-in | Models |
| --- | --- | --- |
| **OpenAI** | API key | GPT family |
| **Anthropic** | API key | Claude family |
| **Gemini** | API key | Gemini family |
| **Bedrock** | Bedrock API key or your AWS credentials | Claude, Nova, Llama, Mistral, and DeepSeek families hosted on Amazon Bedrock |
| **Codex** | Sign in with ChatGPT (OAuth) | GPT-5 Codex family |
| **Claude** | Sign in with Claude (OAuth) | Claude family through your Claude Pro/Max subscription |

The first four are the basic cloud providers: you onboard each by pasting an
API key. **Bedrock** additionally accepts the AWS credentials you already use
on the machine. **Codex** and **Claude** are browser sign-in providers. Codex
signs in to ChatGPT and talks to the ChatGPT Codex backend on your behalf.
Claude signs in to your Claude account and talks to Anthropic models through
your Claude Pro/Max subscription.

:::note
Rune already knows the catalog of models each provider serves, so you do not
declare them. Once a provider is authenticated, its models appear in the model
list automatically.
:::

## Onboarding a provider

You onboard providers with the `models` command from the
[Rune console](../learn/console.md). The provider subcommands live under
`models providers`:

```
models providers                # show all providers and their status
models providers <provider>     # show one provider's subcommands
```

How you authenticate depends on the provider.

### API-key providers (OpenAI, Anthropic, Gemini, Bedrock)

OpenAI, Anthropic, Gemini, and Bedrock are onboarded by storing an API key. Each
provider keeps a set of **named** keys, so you can hold more than one (for
example a personal and a work key) and switch between them. The same four
subcommands work for all four:

| Command | What it does |
| --- | --- |
| `models providers <provider> add <name>` | Opens a masked prompt to paste a key, tests it against the provider, then stores it under `<name>`. The first key you add becomes the active one. |
| `models providers <provider> use <name>` | Make a stored key the active one. Takes effect immediately, no restart needed. |
| `models providers <provider> status` | List the stored key names and show which is active. |
| `models providers <provider> remove <name>` | Delete a stored key. If it was active, another stored key is promoted automatically. |

To onboard OpenAI, for example:

```
models providers openai add work    # paste a key, store it as 'work'
models providers openai status      # confirm 'work' is the active key
```

The key is pasted into a masked prompt (each character shows as `*`, like a
password field), verified with a live request to the provider, and only stored
if the test succeeds. Keys are
kept in Rune's local storage, not in your config file, so you never commit a
secret. Anthropic, Gemini, and Bedrock work identically; just swap the provider
name:

```
models providers anthropic add work
models providers gemini add work
```

### Amazon Bedrock

Bedrock gives you Claude, Nova, Llama, Mistral, and DeepSeek models billed
through your AWS account. It is the one provider with two ways to authenticate.
Storing a Bedrock API key works exactly like the other API-key providers
above, with one extra requirement: the key is bound to a single AWS region,
so `add` takes the region alongside the key name:

```
models providers bedrock add work us-east-1
```

Without a stored key, Rune signs requests with the AWS credentials already
on the machine, including SSO sessions, named profiles, and instance roles.

The [Amazon Bedrock guide](./bedrock.md) covers both paths in detail:
signing in with IAM Identity Center (SSO), choosing a profile and region,
private endpoints, and how model access shapes the model list.

### Codex (Sign in with ChatGPT)

Codex does not use an API key. You sign in to your ChatGPT account and Rune
uses that session to reach the ChatGPT Codex backend:

```
models providers codex login    # opens a browser to sign in to ChatGPT
models providers codex status   # show the signed-in account and its plan
```

`login` opens your browser to complete the OAuth flow, then stores the
resulting credentials locally and refreshes them as needed. Once signed in,
the Codex models appear in the model list like any other provider's.

### Claude (Sign in with Claude)

Claude lets Rune Agent use Anthropic models through your Claude Pro or Claude
Max subscription. You sign in with Claude; no Anthropic API key is required.

This is different from running Anthropic's standalone Claude Code CLI inside
Rune. For the CLI integration, see the [Claude Code guide](./claude-code.md).

Rune has two built-in ways to use Claude-family models:

| Provider | Authentication | Billing / quota |
| --- | --- | --- |
| `anthropic/...` | Anthropic API key | Anthropic API usage |
| `claude/...` | Claude OAuth sign-in | Claude Pro/Max Agent-SDK usage credit |

Use `anthropic` when you want API-key billing. Use `claude` when you want to use
your Claude subscription.

To sign in, run:

```
models providers claude login
```

Rune opens a browser for the Claude OAuth flow, stores the resulting credential
locally, and refreshes it when needed. Check the account and plan with:

```
models providers claude status
```

Once authenticated, Claude models appear alongside the rest of your models. Use
the `claude/` provider prefix when a model name is ambiguous:

```
models
model claude/claude-opus-4-6
```

You can also make a Claude subscription model the default for new chats:

```
models alias set default claude/claude-opus-4-6
```

Most users do not need any `models.claude` config. Authentication is managed by
`models providers claude login`, not by config files. If you front the Claude
subscription endpoint with a proxy, set a base URL override:

```yaml tab
models:
  claude:
    base_url: "https://claude-proxy.internal"
```

```python tab
"models": {
    "claude": {"base_url": "https://claude-proxy.internal"},
},
```

Reasoning effort for Claude-family models is shared with the Anthropic provider,
so configure it under `models.anthropic.reasoning_effort`.

:::note
Claude subscription usage in Rune draws from Claude's Agent-SDK credit, which is
separate from your Claude.ai chat limits. If that credit is exhausted, enable
usage credits ("extra usage") in your Claude account under **Settings > Usage**.
:::

## Selecting a model

After a provider is authenticated, run `models` (or `agent models`) in the
[Rune console](../learn/console.md) to see its models in the list. Because the same
model name can be served by more than one provider, Rune identifies models as
`provider/name`, for example
`openai/gpt-5.4`, `anthropic/claude-opus-4-6`, `claude/claude-opus-4-6`, or
`codex/gpt-5.4`. A bare name resolves when only one provider exposes it;
otherwise prefix it with the provider.

Set the default model for new chats by pointing the `default` alias at it:

```
models alias set default anthropic/claude-opus-4-6
```

The default model is not a config-file setting: it is stored with the rest of
your [model aliases](./intro.md#model-aliases).

You can also switch a focused chat's model with the `chatmodel` command-prompt
command. See the [agent intro](./intro.md#models) for the model-selection
commands.

## Configuration

Authentication is managed entirely through the `models providers` commands
above, so the config file holds no secrets. What you configure under `models`
is provider **behavior**: which base URL to talk to, how much the model should
reason, and how reasoning is summarized.

These settings are optional. With a default install and an authenticated
provider, every provider works without any `models` block at all.

### Reasoning effort

OpenAI, Anthropic, Gemini, and Bedrock each take a `reasoning_effort` that
controls how much the model thinks before answering. Higher effort trades
latency and token cost for more thorough reasoning. The accepted levels are:

```
none  minimal  low  medium  high  xhigh  max
```

Not every model supports every level. `reasoning_effort` is a standing default
applied to whichever model you are using: models that support the level honor
it, and models that do not fall back to their own default without warning you.
Leave it unset to always use the model's default.

```yaml tab
models:
  openai:
    reasoning_effort: "high"
  anthropic:
    reasoning_effort: "high"
  gemini:
    reasoning_effort: "medium"
  bedrock:
    reasoning_effort: "medium"
```

```python tab
"models": {
    "openai": {"reasoning_effort": "high"},
    "anthropic": {"reasoning_effort": "high"},
    "gemini": {"reasoning_effort": "medium"},
    "bedrock": {"reasoning_effort": "medium"},
},
```

You can also change effort per conversation with `/effort` in a chat, or set
the session default with `agent effort <level>`. Unlike the config value,
these are explicit per-session choices, so Rune warns you when the current
model cannot honor the level you asked for.

### Reasoning summary

`models.reasoning_summary` controls how Rune surfaces a reasoning model's
thinking. It applies to OpenAI and Codex and is one of:

| Value | Meaning |
| --- | --- |
| `auto` | Let the provider choose (the default). |
| `concise` | Show a short summary of the model's reasoning. |
| `detailed` | Show a fuller summary. |
| `disabled` | Do not show reasoning summaries. |

```yaml tab
models:
  reasoning_summary: "concise"
```

```python tab
"models": {
    "reasoning_summary": "concise",
},
```

### Base URL overrides

Each provider talks to its standard endpoint out of the box. You can override
the `base_url` to route through a proxy, gateway, or compatible alternative
endpoint, while still using that provider's authentication and model catalog.

```yaml tab
models:
  openai:
    base_url: "https://openai-proxy.internal/v1"
  anthropic:
    base_url: "https://anthropic-proxy.internal"
  gemini:
    base_url: "https://gemini-proxy.internal"
  codex:
    base_url: "https://codex-proxy.internal"
  claude:
    base_url: "https://claude-proxy.internal"
```

```python tab
"models": {
    "openai": {"base_url": "https://openai-proxy.internal/v1"},
    "anthropic": {"base_url": "https://anthropic-proxy.internal"},
    "gemini": {"base_url": "https://gemini-proxy.internal"},
    "codex": {"base_url": "https://codex-proxy.internal"},
    "claude": {"base_url": "https://claude-proxy.internal"},
},
```

Leave `base_url` unset to use the provider's default. For Codex, the default
is the ChatGPT Codex backend; for Claude, the default is Anthropic's Claude
subscription endpoint using Claude Code-compatible request headers. Override
either only if you front it with a proxy. Bedrock also takes a `base_url`
for VPC endpoints and gateways; see the
[Amazon Bedrock guide](./bedrock.md#private-endpoints-and-gateways).

:::note
To point at a fully self-hosted or third-party OpenAI-compatible server (where
you declare your own models and key), use a
[custom provider](./custom-model-providers.md) instead. Base-URL overrides here
keep the built-in provider's authentication and known model catalog.
:::

### Configuration reference

| Key | Type | Description |
| --- | --- | --- |
| `models.reasoning_summary` | string | Reasoning summary level for OpenAI and Codex: `auto`, `concise`, `detailed`, or `disabled`. |
| `models.openai.base_url` | string | Override the OpenAI API base URL. |
| `models.openai.reasoning_effort` | string | Reasoning effort for OpenAI models. |
| `models.anthropic.base_url` | string | Override the Anthropic API base URL. |
| `models.anthropic.reasoning_effort` | string | Reasoning effort for Anthropic models. |
| `models.gemini.base_url` | string | Override the Gemini API base URL. |
| `models.gemini.reasoning_effort` | string | Reasoning effort for Gemini models. |
| `models.bedrock.profile` | string | Named AWS profile to sign requests with when no Bedrock API key is stored. |
| `models.bedrock.base_url` | string | Override the Bedrock runtime endpoint, for VPC endpoints and gateways. |
| `models.bedrock.reasoning_effort` | string | Default reasoning effort for Bedrock models that support extended thinking; individual chats override it with `chateffort` or `/effort`. |
| `models.bedrock.cache_control` | string | Set to `default` to cache the stable part of each request and cut the cost of repeated context. |
| `models.codex.base_url` | string | Override the Codex backend base URL. |
| `models.claude.base_url` | string | Override the Claude subscription backend base URL. |

## See also

- [Amazon Bedrock](./bedrock.md): both Bedrock authentication paths in
  detail, including SSO sign-in and private endpoints.
- [Custom Model Provider](./custom-model-providers.md): connect any
  OpenAI-compatible endpoint with your own model catalog.
- [Local Models](./local-models.md): run GGUF models on your own machine with
  no API key.
- [Claude Code](./claude-code.md): run Anthropic's standalone CLI inside Rune.
- [Rune Console](../learn/console.md): the `models` command and its subcommands.
