---
sidebar_position: 50
---

# Docs workspace

These docs are not just a website. The exact site you are reading right now is
baked into the Rune binary and surfaced as a workspace you can open from inside
the editor, so the documentation lives next to your code and your agent instead
of in a separate browser tab. Everything you would find on
[docs.rune.build](https://docs.rune.build) is here too, ready to read offline.

## Ask with `help`

Open Rune's [command prompt](./command-prompt.md), type `help<enter>`, and Rune
opens the docs site as a workspace and brings up an agent through the
[`?` rune-agent command](../agent/intro.md#quick-questions-with-) so you can ask
about anything in the documentation.

<div className="docs-figure help-command-figure" role="img" aria-label="Running help in the command prompt to open the docs and ask the agent a question" />

:::info

Fast models are better suited for this kind of query: a quick lookup rewards
low latency far more than deep reasoning. A snappy model like
`gemini/gemini-3.5-flash` answers almost as fast as you can read. To point the
query agent at one, set the `query` model alias; see
[Model aliases](../agent/intro.md#model-aliases).

:::

## Browse the docs yourself

You do not have to go through the agent. To read the documentation manually,
open the docs site as a workspace with the `docs`
[alias](./aliases.md), which runs `workspaceopen docs:///`:

```
docs
```

The pages are flattened to the workspace root, so the file tree starts with
`intro.md` and you can open any page directly, for example `config.md` or
`develop/extensions.md`. It is an in-memory, read-only snapshot: there is no shell,
process executor, or network attached to it.
