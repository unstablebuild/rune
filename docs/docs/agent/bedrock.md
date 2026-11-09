---
sidebar_position: 5
---

# Amazon Bedrock

The Bedrock provider gives Rune Agent access to the models hosted on Amazon
Bedrock, including the Claude, Nova, Llama, Mistral, and DeepSeek families,
billed through your AWS account. It behaves like any other provider once
onboarded: its models appear in the model list, and you select them as
`bedrock/<model>`.

Bedrock is the one provider with two ways to authenticate. Use whichever fits
how your AWS account is set up:

| You have | Use |
| --- | --- |
| A Bedrock API key | [Store the key in Rune](#with-a-bedrock-api-key) |
| AWS sign-in on this machine (SSO, profiles, environment variables, instance roles) | [Your AWS credentials](#with-your-aws-credentials) |

Run `models providers bedrock status` at any time to see which of the two is
in effect.

## With a Bedrock API key

A Bedrock API key is the simplest path: no AWS tooling is needed on the
machine. Mint a long-term key in the AWS console under **Amazon Bedrock > API
keys**, then store it under a name together with its region:

```
models providers bedrock add work us-east-1
```

A Bedrock API key only works in the AWS region it was generated in, so the
region is part of the key, not a separate setting. Pass the region shown in
the top-right corner of the AWS console when you minted the key. Rune
verifies the key against Bedrock in that region before storing the pair, and
every request made with that key targets its region automatically.

Rune prompts for the key in a masked field, verifies it against Bedrock, and
stores it in local storage. Like the other
[API-key providers](./providers.md#api-key-providers-openai-anthropic-gemini-bedrock),
you can hold several named keys and switch between them with
`models providers bedrock use <name>`. Because each key carries its own
region, switching keys also switches regions, with no configuration edits
and no restart. Keys for different regions can sit side by side:

```
models providers bedrock add us-key us-east-1
models providers bedrock add eu-key eu-west-1
models providers bedrock use eu-key
```

## With your AWS credentials

If you already sign in to AWS on this machine, you do not need a Bedrock key
at all. When no key is stored, Rune signs Bedrock requests with the same
credentials the AWS CLI would use. Everything the standard AWS credential
resolution supports works here:

- environment variables (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`)
- shared credentials and config files (`~/.aws/credentials`, `~/.aws/config`)
- IAM Identity Center (SSO) sessions
- role assumption and `credential_process` helpers configured in a profile
- EC2 instance roles and container credentials

### Choosing a profile

Rune resolves credentials from one profile: `models.bedrock.profile` when
set, otherwise the `AWS_PROFILE` environment variable, otherwise the default
profile.

```yaml tab
models:
  bedrock:
    profile: "work"
```

```python tab
"models": {
    "bedrock": {"profile": "work"},
},
```

:::note
Prefer `models.bedrock.profile` over `AWS_PROFILE`. When Rune is launched
from the desktop rather than a shell, it does not inherit the environment
variables exported in your shell profile, so a setup that works in your
terminal can silently fall back to the default profile inside Rune.
:::

### Signing in with IAM Identity Center (SSO)

SSO users sign in the same way they do for the AWS CLI:

```
aws sso login --profile work
```

Run it in any terminal, including a terminal inside Rune. Rune reads the
resulting session the next time it talks to Bedrock, so there is nothing to
restart. Rune also refreshes the session automatically for as long as AWS
allows.

When the session expires beyond what can be refreshed, Bedrock requests fail
with an error saying the cached SSO session is expired or invalid. Run
`aws sso login` again and retry; the next request picks the new session up.

The profile Rune uses must be the one that carries your SSO configuration.
If your SSO setup lives in a named profile, set `models.bedrock.profile` as
shown above.

### Role assumption, MFA, and credential helpers

Anything expressible in an AWS profile works, because Rune resolves the
profile the standard way. That includes assuming a role from a source
profile, and delegating to an external credential helper via
`credential_process`. Flows that need an interactive prompt at request time,
such as MFA-gated role assumption, should go through a `credential_process`
helper that manages the prompt itself; Rune does not prompt for MFA codes.

## Choosing a region

Bedrock is a regional service, and model availability differs between
regions.

- With a stored API key, requests go to the region you named when adding
  the key. `models providers bedrock status` lists each key with its
  region, and `use` switches key and region together.
- With AWS credentials, the region resolves like the rest of your AWS
  setup: the environment or profile region, otherwise `us-east-1`.

## Private endpoints and gateways

If your network routes Bedrock traffic through a VPC endpoint or an internal
inference gateway, point Rune at it with `models.bedrock.base_url`:

```yaml tab
models:
  bedrock:
    base_url: "https://bedrock.internal.example.com"
```

```python tab
"models": {
    "bedrock": {"base_url": "https://bedrock.internal.example.com"},
},
```

The override applies to model invocation. Such endpoints do not serve
Bedrock's management API, so the model list comes from Rune's built-in
catalog instead of a live query.

## The model list

Bedrock grants model access per AWS account and region. Rune asks your
account which models it can actually invoke and lists those, preferring the
cross-region entries that serve on-demand traffic. If your credentials
cannot read that list, Rune falls back to a built-in catalog of the common
models. Entries your account has not been granted fail at request time until
you enable them in the AWS console.

Bedrock model names carry their AWS identifiers:

```
model bedrock/us.anthropic.claude-opus-4-5-20251101-v1:0
```

## Configuration reference

| Key | Type | Description |
| --- | --- | --- |
| `models.bedrock.profile` | string | Named AWS profile to resolve credentials from when no Bedrock API key is stored. |
| `models.bedrock.base_url` | string | Override the Bedrock runtime endpoint, for VPC endpoints and gateways. |
| `models.bedrock.reasoning_effort` | string | Default reasoning effort for Bedrock models that support extended thinking; individual chats override it with `chateffort` or `/effort`. See [reasoning effort](./providers.md#reasoning-effort). |
| `models.bedrock.cache_control` | string | Set to `default` to cache the stable part of each request and cut the cost of repeated context. |

## See also

- [Providers](./providers.md): the full provider list and the shared
  `models providers` commands.
- [Custom Model Provider](./custom-model-providers.md): connect an
  OpenAI-compatible endpoint with your own model catalog.
- [Rune Console](../learn/console.md): the `models` command and its
  subcommands.
