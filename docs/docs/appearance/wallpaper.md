---
sidebar_position: 3
---

# Wallpaper

Rune shows a wallpaper in every [window](../learn/layout-management.md#windows) that
has no tab to show. By default it is Rune's built-in logo, which plays an
intro when Rune starts. You can replace it with ASCII or Unicode text that
Rune renders directly, or give Rune a PNG image to show as a picture, up to the
whole window, or to ASCII encode. In the GUI, a shader can also paint the
whole window on the GPU, as a still or an animation.

Wallpaper settings live under `workspace`, so you can set one globally in
`~/.config/rune/config.yaml` or `~/.config/rune/config.star`. A project
can also provide its own wallpaper in `.rune/config.yaml` at the
workspace root, which applies once
you [trust that file](../config.md#where-the-config-lives).

Here, Rune converts a transparent Python logo into colored ASCII art. The
source image stays recognizable, while the empty area around the logo blends
into the workspace:

<img
  className="docs-figure wallpaper-figure"
  src="https://assets.rune.build/images/docs-rune/wallpaper/python-logo-example-v2.png"
  alt="Rune displaying a Python logo converted from a PNG into colored ASCII art, with the wallpaper configuration open beside it"
/>

## Which wallpaper Rune shows

Rune shows the first of these settings that is not empty:

1. [`wallpaper_shader`](#run-a-shader): a shader drawn on the GPU, in the
   GUI.
2. [`wallpaper_image`](#show-an-image): a PNG drawn as a picture.
3. [`wallpaper_image_ascii`](#encode-an-image-to-ascii): a PNG encoded as
   ASCII art.
4. [`wallpaper_ascii`](#use-ascii-art-directly): ASCII art drawn as written.

When all four are empty, which is the default, Rune shows
[its built-in logo](#the-built-in-logo). Setting one takes over from those
below it, so you never need to clear the others. A setting Rune cannot use,
such as an image it cannot open, is reported as a configuration error and
skipped in favor of the next one.

Where a shader cannot run, such as with `rune --tui`, Rune shows the next
wallpaper of the list instead, so you can pair a shader with a wallpaper for
the terminal.

## The built-in logo

Without a wallpaper of your own, Rune draws the Rune logo as a full-resolution
picture.

### The intro

The intro is the animation that plays over the logo when Rune starts: braille
dots fly in and assemble the logo, then bloom into its picture. Set
`animations.intro` to `false` to start on the logo directly, and run
`shaderrun intro` to play the intro at any time:

```yaml tab
animations:
  intro: false
```

```python tab
config["animations"]["intro"] = False
```

Only the built-in logo has an intro. A wallpaper of your own, shader, ASCII
art or image, replaces the logo together with its intro, so Rune starts on
your wallpaper without one.

The intro plays once, at startup, so it belongs to the wallpaper that your
config file in `~/.rune` sets. A project's `.rune/config.yaml` can change the
wallpaper of its windows, but not the intro. A change to `animations.intro`
takes effect the next time Rune starts.

## Use ASCII art directly

Choose `workspace.wallpaper_ascii` when you already have ASCII art and want
exact control over every character:

```yaml tab
workspace:
  wallpaper_ascii: |
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
config["workspace"]["wallpaper_ascii"] = """┌─────────────┐
│   R U N E   │
└─────────────┘"""
config["workspace"]["wallpaper_attr"] = attr(
    fg = "#D34728",
    bg = "default",
)
config["workspace"]["wallpaper_background_attr"] = attr(bg = "default")
```

Rune draws the art exactly as written. Both plain ASCII and Unicode drawing
characters work. Keep in mind that:

- Rune centers the art as one block, and the lines keep their alignment
  within it: a short line sits at the block's left edge, not in the middle.
  Pad it with leading spaces to center it.
- Tabs are not expanded. Each one takes a single cell, so indent with spaces.
- Art larger than the window is not scaled down. Rune cuts off whatever does
  not fit at the right and bottom edges.
- An empty string turns the ASCII wallpaper off, and Rune shows the built-in
  logo instead.

### Colors

`wallpaper_attr` styles the characters of the art: its `fg`, `bg`, and
`flags` such as `bold`. `wallpaper_background_attr` fills the rest of the
window, and its `flags` apply only to those surrounding cells.

As soon as `wallpaper_background_attr` sets anything other than defaults, its
`bg` also replaces the `bg` of `wallpaper_attr`, so the whole window shares
one background. To put the art on a background of its own, leave
`wallpaper_background_attr` at its defaults.

Without a `wallpaper_attr`, ASCII art is drawn in your theme's blue in the
GUI. In a terminal it is drawn in `#D34728` on a `#1e1e1e` background.

## Show an image

Choose `workspace.wallpaper_image` to show an image as a picture:

```yaml tab
workspace:
  wallpaper_image: ~/Pictures/wallpaper.png
```

```python tab
config["workspace"]["wallpaper_image"] = "~/Pictures/wallpaper.png"
```

A path alone draws the image as large as it fits in the central 60% of the
window's width and height, keeping its proportions. To size it yourself, set
`wallpaper_image` to a section:

| Property | What it does |
| --- | --- |
| `path` | Path to the PNG. `""` turns the picture off. |
| `width` | The most columns the picture takes up: a number of cells, such as `40`, or a percentage of the window's width, such as `"60%"`. Defaults to `"60%"`. |
| `height` | The most rows the picture takes up, in cells or as a percentage of the window's height. Defaults to `"60%"`. |
| `fit` | How the image is scaled into `width` and `height`: `contain` keeps all of it, `cover` fills them and cuts off evenly what overflows, and `fill` stretches it over them. `contain` and `cover` keep the image's proportions. Defaults to `contain`. |

The picture is centered in the window. To make it fullscreen, have it cover
the whole window:

```yaml tab
workspace:
  wallpaper_image:
    path: ~/Pictures/wallpaper.png
    width: 100%
    height: 100%
    fit: cover
```

```python tab
config["workspace"]["wallpaper_image"] = {
    "path": "~/Pictures/wallpaper.png",
    "width": "100%",
    "height": "100%",
    "fit": "cover",
}
```

The image must currently be a **PNG** of at most 8192 pixels on each side, and
its path follows the same rules as
[`wallpaper_image_ascii`](#encode-an-image-to-ascii). The transparent parts of
the image show the window's background, which `wallpaper_background_attr`
sets.

A terminal cannot draw pictures, so with `rune --tui` Rune encodes the image as
ASCII art in the same place, with the characters of
[`wallpaper_density_characters`](#choose-the-conversion-characters).

## Encode an image to ASCII

Choose `workspace.wallpaper_image_ascii` to have Rune turn an image into
colored characters as it renders:

```yaml tab
workspace:
  wallpaper_image_ascii: ~/Pictures/my-logo.png
  wallpaper_background_attr:
    bg: default
```

```python tab
config["workspace"]["wallpaper_image_ascii"] = "~/Pictures/my-logo.png"
config["workspace"]["wallpaper_background_attr"] = attr(bg = "default")
```

Before Rune drew pictures, `wallpaper_image` encoded the image as ASCII. To
keep that look, rename it to `wallpaper_image_ascii` in your config.

The image must currently be a **PNG**. `~` in the path expands to your home
directory, and `$VARIABLES` expand to their values. A relative path is
resolved against the directory Rune was started from, not against the config
file, so a project's `.rune/config.yaml` should use an absolute path, `~`, or
a variable.

Rune reads the image when it loads your configuration. After replacing the
file, run `workspacereload` to see the new image. In the GUI,
[`runectl screenshot`](../develop/runectl.md#screenshots) also reads it again,
in every workspace, before it takes the screenshot.

`wallpaper_attr` has no effect on an image wallpaper: every character takes
its color from the image. `wallpaper_background_attr` sets the background and
flags of every cell in the window, the image's characters included.

For the best results, start with an image that has:

- a **transparent background**, rather than a solid rectangle;
- a simple silhouette and clear contrast;
- large features that remain recognizable at terminal-cell resolution;
- a tight crop, without a large amount of unused canvas around the subject; and
- enough pixels to fill the window, since Rune enlarges small images without
  smoothing and they come out blocky.

Fully transparent pixels count as black, so they take the first character of
the ramp, a space by default. This lets your theme or transparent GUI
background show around the subject and avoids the box-shaped background
common with JPEG-like source art. Partial transparency is ignored: a
semi-transparent pixel is drawn in its full color, so soft anti-aliased edges
come out as a hard outline. Fully opaque photographs work, but tend to look
busier than logos and illustrations.

:::tip

Export the source as a PNG with alpha transparency. If your editor shows a
checkerboard around the subject rather than a white or black rectangle, the
background is probably transparent.

:::

## Choose the conversion characters

For images encoded as ASCII, by `wallpaper_image_ascii` or by `wallpaper_image`
in a terminal, `wallpaper_density_characters` controls the characters Rune uses
for different brightness levels. Write them from the least visible to the most
dense:

```yaml tab
workspace:
  wallpaper_image_ascii: ~/Pictures/my-logo.png
  wallpaper_density_characters: " ░▒▓█"
```

```python tab
config["workspace"]["wallpaper_image_ascii"] = "~/Pictures/my-logo.png"
config["workspace"]["wallpaper_density_characters"] = " ░▒▓█"
```

The first character should normally be a space so transparent and darkest
parts of the image disappear. A short ramp produces a bold, graphic result;
a longer ramp can preserve more detail.

Rune measures a pixel's brightness as the plain average of its red, green,
and blue values, and splits that range into equal bands, one per character.
The last character is the exception: only pure white gets it. With
`" ░▒▓█"`, a pixel darker than a quarter of full brightness becomes a space,
the next three quarters become `░`, `▒`, and `▓`, and only pure white becomes
`█`. To give the densest character a band of its own, repeat it at the end,
as in `" ░▒▓██"`.

Because brightness is a plain average, saturated colors read as dim: pure
red, green, or blue is a third as bright as white and lands low on the ramp.

Rune's default ramp, `" ▓▓▓▓"`, draws every pixel at least a quarter as
bright as white as a solid `▓` in that pixel's color, and drops everything
darker, black outlines included. Rune also uses it in place of an empty ramp,
which it reports as a configuration error.

## Run a shader

Choose `workspace.wallpaper_shader` to have the GUI paint every empty window
with a fragment shader written in [Kage](https://ebitengine.org/en/documents/shader.html),
a Go-like shading language that runs on the GPU:

```yaml tab
workspace:
  wallpaper_shader: ~/.rune/wallpaper.kage
```

```python tab
config["workspace"]["wallpaper_shader"] = "~/.rune/wallpaper.kage"
```

Or name one of Rune's [built-in shaders](#built-in-shaders):

```yaml tab
workspace:
  wallpaper_shader: plasma
```

```python tab
config["workspace"]["wallpaper_shader"] = "plasma"
```

A shader starts with the `//kage:unit pixels` directive and declares
`Fragment`, which returns the color of each pixel of the window,
premultiplied by its alpha. `dstPos` is the position of the pixel, from the
top-left corner of the window. This one fades from the theme's background
at the top to a color of your choice at the bottom, and sweeps a band of
light across the window:

```go
//kage:unit pixels

package main

var Resolution vec2
var Background vec4
var Progress float
var Tint vec4

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	uv := dstPos.xy / Resolution
	band := smoothstep(0.15, 0, abs(fract(uv.x-Progress+0.5)-0.5))
	return mix(Background, Tint, uv.y) + vec4(0.1)*band
}
```

Rune sets these variables, called uniforms, for every shader that declares
them:

| Uniform | Type | Value |
| --- | --- | --- |
| `Resolution` | `vec2` | The size of the window in pixels, its frame included unless `under_frame` is `false`. |
| `CellSize` | `vec2` | The size of a cell in pixels. |
| `Background` | `vec4` | The theme's background color, as the window paints it. |
| `Foreground` | `vec4` | The theme's foreground color. |
| `Progress` | `float` | How far the animation is through its loop, from 0 to just under 1. |
| `Frame` | `float` | The frame of the loop, from 0. |
| `Total` | `float` | The number of frames in a loop. |
| `Seed` | `float` | The `seed` setting, for shaders that vary with it. |

A shader that declares `Progress` or `Frame` is animated: it loops over
`duration`, so make it end where it starts to loop seamlessly. Any other
shader is a still, which costs nothing once drawn. Rune only animates the
windows on screen.

### Built-in shaders

Set `wallpaper_shader` to one of these names to run a shader that comes
with Rune, and tune it through its uniforms:

| Name | What it draws | Uniforms and their defaults |
| --- | --- | --- |
| `gradient` | A still gradient. | `From` and `To`, the colors at either end (`"default"`, the background, and `"blue"`); `Angle`, the direction in degrees clockwise from left to right (`90`, top to bottom). |
| `noise` | Clouds that change over the loop. | `From` and `To`, the colors of the lowest and highest clouds (`"default"` and `"blue"`); `Scale`, how many clouds span the height (`3`); `Speed`, how many times they change over a loop (`2`). Each `seed` draws different clouds. |
| `plasma` | Waves of color. | `Scale`, how many waves span the height (`4`); `Speed`, how many times they turn over a loop (`1`); `Hue`, a turn of the colors from 0 to 1 (`0`); `Strength`, how far they stand out from the background, from 0 to 1 (`0.3`). |
| `blaze` | A swaying fire. | `Speed`, how many times the flames change over a loop (`3`); `Hue`, a turn of the colors from 0 to 1, where 0 burns red (`0`); `SwapRedBlue`, `1` to swap red and blue (`0`); `Strength` (`0.5`). Each `seed` burns differently. |
| `inferno` | A fire. | The uniforms of `blaze`, with a `Speed` of `4`. |

`Speed` counts whole changes, so every built-in ends its loop where it
started.

```yaml tab
workspace:
  wallpaper_shader:
    shader: blaze
    seed: random
    uniforms:
      Hue: 0.6
      Strength: 0.3
```

```python tab
config["workspace"]["wallpaper_shader"] = {
    "shader": "blaze",
    "seed": "random",
    "uniforms": {"Hue": 0.6, "Strength": 0.3},
}
```

### Settings

To set how it runs, or the values of the shader's own uniforms, set
`wallpaper_shader` to a section:

| Property | What it does |
| --- | --- |
| `shader` | The name of a built-in shader, or the path to a `.kage` file. `""` turns the shader off. |
| `fps` | Frames per second of an animation, from 1 to 120. Defaults to `30`. |
| `duration` | How long one loop of an animation lasts, such as `"10s"`. Defaults to `"10s"`. |
| `animate` | `false` shows the first frame of an animated shader as a still. Defaults to `true`. |
| `seed` | The `Seed` uniform: a whole number, or `"random"` for a new one each time Rune loads your configuration. |
| `under_frame` | Whether the shader also runs under the frame of a tiled window, up to its line, so it fills the window to the edge. Defaults to `true`, as the default frame draws its lines along the outer edge of its cells. Set it to `false` with a `frame_charset` whose lines run through the middle of the cell, such as `│` and `─`, where the shader would show past the line; it then stops inside the frame. Frame images show the shader inside their lines only either way. |
| `uniforms` | Values for the uniforms the shader declares: a number for a `float` or an `int`, a list of numbers for a vector, a matrix or an array, and a color, such as `"blue"` or `"#D34728"`, for a `vec4`. Colors follow the theme. |

```yaml tab
workspace:
  wallpaper_shader:
    shader: ~/.rune/wallpaper.kage
    duration: 20s
    uniforms:
      Tint: "#D34728"
  wallpaper_image_ascii: ~/Pictures/my-logo.png
```

```python tab
config["workspace"]["wallpaper_shader"] = {
    "shader": "~/.rune/wallpaper.kage",
    "duration": "20s",
    "uniforms": {"Tint": "#D34728"},
}
config["workspace"]["wallpaper_image_ascii"] = "~/Pictures/my-logo.png"
```

The path follows the same rules as
[`wallpaper_image_ascii`](#encode-an-image-to-ascii), and Rune reads the file
when it loads your configuration: run `workspacereload` after editing it. A
file Rune cannot read, or that is not a shader, and a name that is not a
built-in are reported as configuration errors, as are settings it cannot
use. Errors that only show when the GPU compiles the shader are reported
once in a notification, and the window shows the next wallpaper meanwhile.

The shader's alpha composites over the window's background, so on a
[translucent window](./themes.md) a transparent shader lets the desktop show
through.

## Available settings

| Setting | What it does |
| --- | --- |
| `workspace.wallpaper_shader` | A [built-in shader](#built-in-shaders), or the path to a Kage shader, that the GUI draws over every empty window, or a section with its `shader`, `fps`, `duration`, `animate`, `seed`, `under_frame` and `uniforms` (see [Run a shader](#run-a-shader)). The terminal UI shows the next wallpaper instead. Defaults to a section whose `shader` is `""`, no shader. |
| `workspace.wallpaper_image` | Path to a PNG that Rune shows as a picture, or a section with its `path`, `width`, `height` and `fit` (see [Show an image](#show-an-image)). Defaults to a section whose `path` is `""`, no picture. |
| `workspace.wallpaper_image_ascii` | Path to a PNG that Rune encodes as colored ASCII art. Relative paths start from the directory Rune was started in. Defaults to `""`, no image. |
| `workspace.wallpaper_ascii` | ASCII art drawn as written and centered as a block. Defaults to `""`, no art. |
| `workspace.wallpaper_density_characters` | Brightness ramp used to encode images as ASCII, ordered from least to most dense. Defaults to `" ▓▓▓▓"`. |
| `workspace.wallpaper_attr` | Foreground, background, and flags of the characters of `wallpaper_ascii`. Ignored by image wallpapers, whose colors come from the image. |
| `workspace.wallpaper_background_attr` | Background and flags of the window behind any wallpaper, including the built-in logo. Once set, its `bg` also replaces the `bg` of `wallpaper_attr`. |
| `animations.intro` | Plays the intro of the built-in logo when Rune starts. Defaults to `true`. |

Rune shows the first of `wallpaper_shader`, `wallpaper_image`,
`wallpaper_image_ascii` and `wallpaper_ascii` that is not empty, else its
built-in logo (see
[Which wallpaper Rune shows](#which-wallpaper-rune-shows)). If Rune cannot open
or decode an image, it reports a configuration error and moves on to the next
wallpaper. A `width`, `height` or `fit` of `wallpaper_image` that Rune cannot
read is reported as a configuration error, and Rune uses its default. A
`wallpaper_ascii` that is not a string is reported as a configuration error
too, and Rune moves on to the built-in logo.

## What happens under the hood

A wallpaper is not drawn behind your code. Rune creates it as the content of
each window that has no tab to show: a window you open without one, or a
window whose tab you close while every other tab is already shown in a
window. Showing a tab in the window replaces it. This is why every empty
window in a split layout can show one independently.

The art of `wallpaper_ascii` is centered and drawn without conversion. The
picture of `wallpaper_image` is laid out on whole cells, for the size of the
GUI's font, so it is redrawn to fit when the window or the font size changes.
The shader of `wallpaper_shader` is rendered on the GPU at the window's size in
pixels, once for all the windows of the same size, and drawn as a picture
over the window.
For the PNG of `wallpaper_image_ascii`, Rune:

1. resizes the image, preserving its proportions, to fit the central 60% of
   the window's width and 80% of its height;
2. compensates for terminal cells being taller than they are wide, assuming
   a cell is 2.3 times as tall as it is wide;
3. measures each output pixel's brightness as the average of its red, green,
   and blue values and picks a character from the density ramp; and
4. uses the source pixel's color as that character's foreground color.

The result is generated for the current window size, so the same source image
adapts to different layouts and terminal dimensions.

## See also

- [Config](../config.md): config file locations and YAML versus Starlark
- [Themes](./themes.md): change the colors used by Rune and its terminal
