---
sidebar_position: 47
sidebar_label: Syntax Highlighting
---

# Syntax Highlighting

Rune colors code from the parse tree, not from regular expressions. Every
color you see in a buffer comes from a rule you can change:
`editor.highlights` decides which parts of the syntax get which attributes,
and the active [theme](./themes.md) decides what those color names look
like.

## How highlighting works

1. Rune parses the buffer with the Tree-sitter grammar from the installed
   [language package](../languages/supported.md), and keeps that tree in
   sync as you type.
2. The package ships a highlighting query that tags ranges of the tree with
   **capture names**: `@keyword`, `@string`, `@comment`,
   `@function.method`, and so on.
3. `editor.highlights` maps each capture name to a set of terminal
   attributes — a foreground color, a background color, and text flags.
4. The theme resolves the color names to actual colors.

The split between the last two steps is the useful mental model: **the theme
decides what `yellow` looks like, the highlights decide what is yellow.**
Switching themes recolors everything without touching your capture rules,
and changing a capture rule applies to every theme.

A language package without a highlighting query still parses: the file opens,
folds, and is searchable structurally, but it is rendered without colors.

## Capture names and inheritance

Capture names are dotted, and they fall back to their prefix. When Rune
looks up a capture it tries the full name first, then drops one dotted
segment at a time:

```
keyword.function  ->  keyword
markup.link.url   ->  markup.link  ->  markup
```

So a single `keyword` entry colors `keyword.function`,
`keyword.operator`, and every other refinement, unless you give one of
them its own entry.

Any capture name that matches nothing at any prefix renders with your
terminal's default colors. That is the usual answer to "why is this not
colored?": the grammar emits a capture Rune has no rule for. Real examples
with no default entry include `constant`, `constructor`,
`punctuation.special`, and `embedded`. Adding an entry for the name is all
it takes:

```yaml tab
editor:
  highlights:
    constant:
      fg: red
    constructor:
      fg: aqua
```

```python tab
config["editor"]["highlights"]["constant"] = {"fg": "red"}
config["editor"]["highlights"]["constructor"] = {"fg": "aqua"}
```

## Configuring highlights

`editor.highlights` is merged key by key onto Rune's defaults, so you only
write the captures you want to change. The merge goes one level deeper than
the capture name too: adding `flags` to a capture keeps the default `fg`
for it.

```yaml tab
editor:
  highlights:
    comment:
      fg: silver
      flags: italic
    string:
      fg: green
    keyword:
      flags: bold
```

```python tab
config["editor"]["highlights"]["comment"] = {"fg": "silver", "flags": "italic"}
config["editor"]["highlights"]["string"] = {"fg": "green"}
config["editor"]["highlights"]["keyword"] = {"flags": "bold"}
```

Here `comment` and `string` get new colors, and `keyword` becomes bold
while keeping its default yellow foreground.

### Attributes

Each capture entry accepts three keys:

| Key | Value |
| --- | --- |
| `fg` | Foreground color |
| `bg` | Background color |
| `flags` | One flag name, or a list of them |

Colors are the named palette colors described in
[Themes](./themes.md#what-a-theme-overrides) (`red`, `yellow`, `aqua`,
`silver`, ...), common aliases such as `magenta` and `cyan`, any other W3C
color name, a hex literal like `"#d6a23a"`, or `default` to use the
terminal's own color. A name Rune does not recognize falls back to
`default` rather than raising an error, so check the spelling if a capture
comes out uncolored.

Valid flags are `bold`, `dim`, `italic`, `underline`, `strikethrough`,
`blink`, `reverse`, and `none` (`default` is a synonym for `none`). Pass
several at once as a list:

```yaml tab
editor:
  highlights:
    markup.link:
      fg: aqua
      flags: [underline, italic]
```

```python tab
config["editor"]["highlights"]["markup.link"] = {
    "fg": "aqua",
    "flags": ["underline", "italic"],
}
```

## Default highlights

These are the rules a stock Rune install starts from. Anything not listed
here — and not reachable through prefix inheritance — renders with terminal
defaults.

### Code

| Capture | Foreground | Flags |
| --- | --- | --- |
| `function` | default | |
| `function.builtin` | yellow | |
| `function.method` | default | |
| `type` | default | |
| `property` | default | |
| `variable` | default | |
| `operator` | default | |
| `keyword` | yellow | |
| `string` | magenta | |
| `escape` | default | |
| `number` | red | |
| `constant.builtin` | default | |
| `comment` | blue | |

### Markup

Emitted by Markdown and other prose grammars.

| Capture | Foreground | Flags |
| --- | --- | --- |
| `markup.heading` | yellow | bold |
| `markup.raw` | magenta | |
| `markup.raw.delimiter` | yellow | |
| `markup.link` | cyan | underline |
| `markup.link.url` | cyan | underline |
| `markup.link.label` | yellow | underline |
| `markup.link.text` | cyan | underline |
| `markup.list` | yellow | bold |
| `markup.quote` | blue | italic |
| `markup.strong` | yellow | bold |
| `markup.italic` | yellow | italic |
| `markup.strikethrough` | blue | strikethrough |
| `markup.underline` | yellow | underline |
| `markup.math` | magenta | |

### Legacy prose captures

Older upstream queries use the `text.*` family instead of `markup.*`.

| Capture | Foreground | Flags |
| --- | --- | --- |
| `text.title` | yellow | bold |
| `text.literal` | magenta | |
| `text.uri` | cyan | underline |
| `text.reference` | yellow | underline |
| `text.emphasis` | yellow | italic |
| `text.strong` | yellow | bold |

## Beyond colors

Capture names are not only about rendering. The same vocabulary drives
[structural search and navigation](./search.md): `jumptoast` and
`searchast` take a query file and a capture name, so learning the capture
names your language emits pays off twice.

If a language package ships queries you want to change — or you are adding
a language yourself — see the
[language integration guide](../develop/languages.md) for how Rune loads
`highlights.scm` and the other query files.
