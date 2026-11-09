# AGENTS

Conventions for any agent (human or AI) editing this docs repository.

## Key bindings

Always refer to key combinations using the syntax accepted by
`rune-go-sdk/term.ParseKeys`. That syntax wraps every key in angle brackets and
uses lowercase, hyphen-separated modifiers. Examples:

- `<ctrl-space>`, not `Ctrl-Space`, `Ctrl+Space`, or `^Space`.
- `<ctrl-c>`, not `Ctrl-C` or `C-c`.
- `<alt-meta-space>` on macOS, `<ctrl-alt-space>` elsewhere.
- `<esc>`, `<enter>`, `<space>`, `<tab>` for named keys.

Notes:

- Prefer the spelled-out modifier names (`ctrl`, `shift`, `alt`, `meta`) over
  their single-letter aliases (`c`, `s`, `a`, `m`). Both parse, but the
  spelled-out form is the canonical one Rune emits, so it stays consistent with
  error messages and rendered output.
- Modifier order in combinations is fixed by the parser
  (e.g. `<ctrl-shift-x>`, not `<shift-ctrl-x>`). See
  `docs/learn/key-syntax.md` for the full list of accepted combinations.

This matches the syntax users will see in `rune.star`, config snippets, and any
binding-related error message, so the docs stay copy-pasteable into a real
configuration without translation.

## Punctuation

Do not use em dashes (`—`) anywhere in the docs. They read as an AI tell and are
not part of this repo's voice. Rewrite each one with the punctuation that fits
the sentence.

## Editing user config (YAML)

Before editing a user's config file, read the WHOLE file, not just the section
you intend to change. This matters most for YAML configs.

Duplicate top-level keys break a YAML config. If a top-level key already exists
(for example, `command`), do not add a second one elsewhere in the file. Edit
the existing key in place. Introducing a duplicate key is a silent, highly
undesired outcome that corrupts the user's configuration, and reading only a
section makes it easy to miss the existing key.
