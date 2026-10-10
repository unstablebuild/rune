---
sidebar_position: 45
sidebar_label: Themes
---

export const Swatch = ({hex}) => (
  <span style={{
    display: 'inline-block',
    width: '0.9em',
    height: '0.9em',
    backgroundColor: hex,
    border: '1px solid var(--ifm-color-emphasis-300)',
    borderRadius: '2px',
    verticalAlign: 'middle',
  }} title={hex} />
);

# Themes

A theme controls the colors Rune draws with: the editor, the UI, and
syntax highlighting. Rune ships a set of themes out of the box, and you
can add your own entirely from configuration.

## How themes work

Rune renders everything through a small **named color palette** (the
standard terminal color names like `red`, `green`, `blue`, and so on). A
theme is simply a set of overrides for those names: it remaps each name to
the value you want. Because the whole interface references colors by name,
remapping the palette re-themes everything at once, even your terminal's
colors!

Themes live under the `gui` section of your config:

- `gui.themes` is a catalog of named themes.
- `gui.default_theme` picks which one is active at startup.

The default configuration ships sixteen themes and starts on `romero`.

A theme decides what each color name looks like; it does not decide which
parts of your code use which name. For per-capture control over code
colors, see [Syntax Highlighting](./syntax-highlighting.md).

## Switching themes

Change the active theme at any time with the `guitheme` command:

```
guitheme carmack
```

Run `guitheme` with no argument to return to the default. To preview the
named colors live, use the `colorPalette` command.

## What a theme overrides

A theme entry maps color names to values. Each value can be a hex string
(`"#ff2400"`) or another known color name (`"darkorange"`).

Three keys are special:

| Key | Sets |
| --- | --- |
| `foreground` | the default text color |
| `background` | the default background color |
| `cursor` | the cursor color |

Every other key remaps one of the named palette colors. These are
essentially the 8 regular ANSI colors plus the 8 bold (bright) ones, 16 in
total:

| Regular | Default | Bold | Default |
| --- | --- | --- | --- |
| `black` | <Swatch hex="#000000" /> | `gray` | <Swatch hex="#808080" /> |
| `maroon` | <Swatch hex="#800000" /> | `red` | <Swatch hex="#ff0000" /> |
| `green` | <Swatch hex="#008000" /> | `lime` | <Swatch hex="#00ff00" /> |
| `olive` | <Swatch hex="#808000" /> | `yellow` | <Swatch hex="#ffff00" /> |
| `navy` | <Swatch hex="#000080" /> | `blue` | <Swatch hex="#0000ff" /> |
| `purple` | <Swatch hex="#800080" /> | `fuchsia` | <Swatch hex="#ff00ff" /> |
| `teal` | <Swatch hex="#008080" /> | `aqua` | <Swatch hex="#00ffff" /> |
| `silver` | <Swatch hex="#c0c0c0" /> | `white` | <Swatch hex="#ffffff" /> |

## Creating a theme

Add a new entry under `gui.themes` and point `gui.default_theme` at it:

```yaml tab
gui:
  default_theme: mytheme
  themes:
    mytheme:
      foreground: "#e0e0e0"
      background: "#101010"
      cursor: "#ff8800"
      black: "#101010"
      white: "#e0e0e0"
      red: "#cc3333"
      green: "#33aa55"
      yellow: "#d6a23a"
      blue: "#3a78d6"
      fuchsia: "#c06fd0"
      aqua: "#3ac6c6"
      gray: "#202020"
      silver: "#888888"
      maroon: "#8a2a2a"
      olive: "#9a8030"
      navy: "#2a3a6a"
      purple: "#6a3a9a"
      lime: "#5fd06f"
      teal: "#3a9a9a"
```

You do not have to set every color. Any name you leave out keeps its
current value, so a theme can be as small as a couple of overrides.

Rune loads the themes in your config at startup. After adding one, restart
Rune or open a new window with `guiwindownew`, then activate it with
`guitheme mytheme`, or set `default_theme` to it to use it at startup.

## Share your theme

To share a theme, publish it as a [Rune package](../develop/packages.md)
in a public GitHub repository. A theme-only package needs just two files at
the repository root:

```text
.
├── config.yaml
└── README.md
```

Put your theme under `gui.themes` in `config.yaml`, using the same color
names and values as in [Creating a theme](#creating-a-theme). For example:

```yaml title="config.yaml"
gui:
  themes:
    mytheme:
      foreground: "#e0e0e0"
      background: "#101010"
      cursor: "#ff8800"
      blue: "#3a78d6"
```

For a repository you can copy, see the
[Redmond 95 theme](https://github.com/ernestrc/rune-theme-redmond95), which
ships the `redmond95` palette as a package.

Use `README.md` to show a screenshot, describe the palette, and tell
readers how to install and activate it. You do not need an extension or a
build step for a theme package. Add the `rune` and `theme` **GitHub repository
topics** so we can automatically include your theme in the packages section
of the Rune website. After pushing the repository, others can install it from
the [Rune console](./console.md):

```
pkg install github.com/ernestrc/rune-theme-redmond95
guitheme redmond95
```

Installing merges the theme into `gui.themes` and makes it available to
`guitheme` immediately, with no restart. It does not change the active theme
or `gui.default_theme`. Users can select it with `guitheme` or set
`gui.default_theme: redmond95` in their own config to use it at startup.
See [Packages](../develop/packages.md) for installation and versioning details.
