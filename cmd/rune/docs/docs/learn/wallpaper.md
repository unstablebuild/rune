---
sidebar_position: 46
---

# Wallpaper

Rune shows a wallpaper whenever an editor pane has no open buffer. You can
replace the built-in Rune logo with ASCII or Unicode text that Rune renders
directly, or give Rune a PNG image to convert into colored text cells.

Wallpaper settings live under `workspace`, so you can set one globally in
`~/.config/rune/config.yaml` or `~/.config/rune/config.star`. A project
can also provide its own wallpaper in `.rune/config.yaml` at the
workspace root.

Here, Rune converts a transparent Python logo into colored text cells. The
source image stays recognizable, while the empty area around the logo blends
into the workspace:

<img
  className="docs-figure wallpaper-figure"
  src="https://assets.rune.build/images/docs-rune/wallpaper/python-logo-example-v2.png"
  alt="Rune displaying a Python logo converted from a PNG into colored text cells, with the wallpaper configuration open beside it"
/>

## Use text directly

Choose `workspace.wallpaper` when you already have text art and want exact
control over every character:

```yaml tab
workspace:
  wallpaper: |
    ┌─────────────┐
    │   R U N E   │
    └─────────────┘
  wallpaper_attr:
    fg: "#D34728"
    bg: default
  wallpaper_background_attr:
    bg: default
```

```python tab
config["workspace"]["wallpaper"] = """┌─────────────┐
│   R U N E   │
└─────────────┘"""
config["workspace"]["wallpaper_attr"] = attr(
    fg = "#D34728",
    bg = "default",
)
config["workspace"]["wallpaper_background_attr"] = attr(bg = "default")
```

Rune preserves the text as written and centers it in the empty pane. Both
plain ASCII and Unicode drawing characters work. `wallpaper_attr` sets the
text color, while `wallpaper_background_attr` controls the cells behind the
whole wallpaper.

## Convert an image to text

Choose `workspace.wallpaper_image` to have Rune turn an image into colored
characters as it renders:

```yaml tab
workspace:
  wallpaper_image: ~/Pictures/my-logo.png
  wallpaper_background_attr:
    bg: default
```

```python tab
config["workspace"]["wallpaper_image"] = "~/Pictures/my-logo.png"
config["workspace"]["wallpaper_background_attr"] = attr(bg = "default")
```

The image must currently be a **PNG**. `~` in the path expands to your home
directory.

For the best results, start with an image that has:

- a **transparent background**, rather than a solid rectangle;
- a simple silhouette and clear contrast;
- large features that remain recognizable at terminal-cell resolution; and
- a tight crop, without a large amount of unused canvas around the subject.

Transparent pixels become blank cells with Rune's default character ramp.
This lets your theme or transparent GUI background show around the subject
and avoids the box-shaped background common with JPEG-like source art. Fully
opaque photographs work, but tend to look busier than logos and illustrations.

:::tip

Export the source as a PNG with alpha transparency. If your editor shows a
checkerboard around the subject rather than a white or black rectangle, the
background is probably transparent.

:::

## Choose the conversion characters

For image wallpapers, `wallpaper_density_characters` controls the characters
Rune uses for different brightness levels. Write them from the least visible
to the most dense:

```yaml tab
workspace:
  wallpaper_image: ~/Pictures/my-logo.png
  wallpaper_density_characters: " ░▒▓█"
```

```python tab
config["workspace"]["wallpaper_image"] = "~/Pictures/my-logo.png"
config["workspace"]["wallpaper_density_characters"] = " ░▒▓█"
```

The first character should normally be a space so transparent and darkest
parts of the image disappear. A short ramp produces a bold, graphic result;
a longer ramp can preserve more detail. Rune's default is `" ▓▓▓▓"`.

## Available settings

| Setting | What it does |
| --- | --- |
| `workspace.wallpaper` | Text art rendered directly. |
| `workspace.wallpaper_image` | Path to a PNG that Rune converts to colored text cells. |
| `workspace.wallpaper_density_characters` | Brightness ramp used for image conversion, ordered from least to most dense. |
| `workspace.wallpaper_attr` | Foreground and background attributes for direct text art. Image colors come from the image itself. |
| `workspace.wallpaper_background_attr` | Background attributes behind either kind of wallpaper. |

If both `wallpaper_image` and `wallpaper` are set, the image takes precedence.
If Rune cannot open or decode the image, it reports a configuration error and
falls back to the text wallpaper. If neither is set, Rune uses its built-in
logo.

## What happens under the hood

A wallpaper is not drawn behind your code. Rune creates it as the content of
each empty editor pane. Opening a buffer replaces it, and closing the last
buffer in a pane reveals a new wallpaper. This is why every empty pane in a
split layout can show one independently.

Text art is centered and drawn without conversion. For a PNG, Rune:

1. resizes the image to the available pane while preserving its proportions;
2. compensates for terminal cells being taller than they are wide;
3. measures each output pixel's brightness and picks a character from the
   density ramp; and
4. uses the source pixel's color as that character's foreground color.

The result is generated for the current pane size, so the same source image
adapts to different layouts and terminal dimensions.

## See also

- [Config](../config.md): config file locations and YAML versus Starlark
- [Themes](./themes.md): change the colors used by Rune and its terminal
