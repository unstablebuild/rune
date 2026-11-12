---
sidebar_position: 48
---

# Animations

Rune plays short animations when it starts, while a workspace opens, over the
command prompt, over tabs whose program is working, while you are away, and
when the cursor jumps. They come in two kinds:

- **Cell shaders** animate the characters and colors of the cells on screen.
  They are built into Rune, and play in the GUI and when Rune runs in a
  terminal.
- **The cursor effect** is a shader that draws over the pixels of the window
  when the cursor jumps. It plays in the GUI only, and you can install more
  cursor shaders or write your own.

Animations are set under `animations` in your [config](../config.md). Rune
reads them when it starts, so restart it after changing them.

## Cell shaders

Run `shaderrun <name> [<duration>] [<fps>]` to play any of them over the
screen, such as `shaderrun thunderstorm 10s`. They play for 1s at 30 frames per
second unless you say otherwise.

### Reveals

These uncover the screen they play over with what is under it, which is why
they suit [opening a workspace](#when-a-workspace-opens).

| Shader | What it does |
| --- | --- |
| `beams` | Beams of light sweep along the rows and columns, uncovering the text dimmed, then a diagonal wipe brings back its colors. |
| `binaryPath` | The binary code of each character snakes in from outside the screen and collapses into the character. |
| `burn` | Sets the screen alight character by character, the fire spreading from a random cell. |
| `decrypt` | Types the text out as ciphertext, then decrypts it character by character. |
| `expand` | Bursts the text out of the center of the screen, each character traveling to its place. |
| `matrix` | Rains streaks of symbols down the columns until they fill the screen, then resolves it cell by cell into the text. |
| `middleOut` | Spreads the text along the middle row, then moves each character up or down to its place. |
| `pour` | Pours the text in from one edge, line by line. |
| `print` | Prints the text row by row like a line printer, scrolling up as it goes. |
| `rain` | Rains the characters down from the top, row by row. |
| `slide` | Slides each line in from an edge, one character after another. |

### Fire

| Shader | What it does |
| --- | --- |
| `blaze` | A fire that loops seamlessly. |
| `blazeBlue` | `blaze` with blue flames. |
| `embers` | Embers burn in ring-like blobs and turn to ash under glittering sparks. |
| `flames` | Flames cross the screen from bottom to top. |
| `incendium` | `flames` and `embers` together, the rising sparks dragging characters along. |
| `inferno` | Another fire that loops seamlessly. |
| `infernoBlue` | `inferno` with blue flames. |

### Over the text

| Shader | What it does |
| --- | --- |
| `bomb` | A ring expands from the center, pushing the characters under it outward like a magnifying glass. |
| `crumble` | The characters crumble to dust and fall, are swept up, then drop back into place. |
| `rings` | Gathers the characters into concentric rings that spin, scatter and condense, then sends them home. |
| `spotlights` | Dims the screen and sweeps spotlights over it. |
| `thunderstorm` | Darkens the screen and lets a storm loose over it, with lightning that lights up the text. |
| `unstable` | Shuffles the characters, which rumble and heat up until they blow apart, then reassemble. |
| `vhsTape` | Plays the screen back like a worn tape, with tearing, glitches and snow. |

### Color and frames

| Shader | What it does |
| --- | --- |
| `grayFade` | Fades the colors of the screen to gray. |
| `pulse` | Pulses every cell toward a color and back. |
| `shine` | Sweeps a band of light diagonally across the screen. |
| `shineFrame` | `shine`, over the frames of windows only. |
| `radarFrame` | A radar sweep circles the frames of windows. |
| `nop` | Draws nothing. |

With the built-in logo as your [wallpaper](./wallpaper.md), `shaderrun intro`
also plays its intro.

## Where Rune plays them

Each animation below is on by default. Set it to `false` to turn it off, or, for
those that take settings, set `enabled: false` in its section.

### When Rune starts

`animations.intro` plays the intro of the built-in logo. See
[The intro](./wallpaper.md#the-intro).

### When a workspace opens

- `animations.loading_workspace` fades the screen to gray while a workspace
  loads. Turning it off also turns off `open_workspace`, which takes over from
  it.
- `animations.open_workspace` reveals the workspace once it has loaded. By
  default, a quick `burn` sweeps over the screen the workspace takes over.
  - `shader`: any cell shader, by name. The [reveals](#reveals) uncover the
    workspace from the screen it takes over.
  - `duration`: how long it plays, such as `"1500ms"`.

```yaml tab
animations:
  open_workspace:
    shader: matrix
    duration: 1500ms
```

```python tab
config["animations"]["open_workspace"]["shader"] = "matrix"
config["animations"]["open_workspace"]["duration"] = "1500ms"
```

### Over the command prompt

`animations.command_prompt` sends a radar sweep round the frame of the command
prompt while it is open.

- `color`: the color the sweep lights the frame in.
- `angular_width`: more than 0 and up to 1: how much of a full turn the sweep
  covers.
- `cycles`: how many turns it makes, 1 or more.

```yaml tab
animations:
  command_prompt:
    color: blue
    angular_width: 0.5
    cycles: 2
```

```python tab
config["animations"]["command_prompt"]["color"] = "blue"
config["animations"]["command_prompt"]["angular_width"] = 0.5
config["animations"]["command_prompt"]["cycles"] = 2
```

### Over working tabs

`animations.active_content_tab` plays over content tabs whose program reports
it is [working](./terminal/program-status.md), and
`animations.active_workspace_tab` over the workspace tabs that own one. Both
take the same keys:

- `enabled`: set to `false` to turn the effect off.
- `shader`: one of `blaze`, `burn`, `inferno`, `pulse` or `shine`.
- `fps`: how many times per second the effect redraws.
- `loop`: how long one loop of the effect lasts, such as `"2400ms"`. The
  flames of `blaze` and `inferno` take a few dozen loops to come round, so a
  shorter loop makes the fire faster.

Settings that only apply to one shader go under a key named after it, and are
only used while that shader is selected. All of them are optional:

| Shader | Setting | Effect |
| --- | --- | --- |
| `pulse`, `shine` | `color` | The colour the label is blended toward. Defaults to white. |
| `pulse`, `shine` | `intensity` | 0 to 1: how far the label is blended at the peak, where 1 reaches `color`. Subtle by default, so the label keeps its status colour. |
| `shine` | `band_width` | 0 to 1: how much of the label the band covers. Narrower reads as a glint, wider as a wash. |
| `shine` | `direction` | The way the band sweeps: `bottom_left_to_top_right` (default), `top_left_to_bottom_right`, `left_to_right`, `right_to_left`, `top_to_bottom` or `bottom_to_top`. |
| `blaze`, `inferno` | `color` | Turns the flames' red to this colour's hue, keeping their shading. Defaults to red; a grey also keeps them red. |
| `blaze`, `inferno` | `swap_red_blue` | Blue flames rather than red, which do not read as an error. Applied after `color`. |
| `burn` | `gradient` | The list of colours a character goes through as it burns. |
| `burn` | `duration` | 0 to 1: the part of a loop each character burns for. Shorter makes a narrower wave. |
| `burn` | `seed` | Picks another burn order. |
| `burn` | `origin` | 0 to 1: where across the label the wave starts, 0 being its left edge. `None` (the default) starts it at a random character. |
| `blaze`, `burn`, `inferno` | `animate_background` | `true` animates the cell background rather than only colouring the text: blaze and inferno set it on fire, and burn burns the label glyph by glyph. |

```yaml tab
animations:
  active_workspace_tab:
    shader: shine
    shine:
      color: green
      intensity: 0.8
      band_width: 0.4
```

```python tab
config["animations"]["active_workspace_tab"]["shine"] = {
    "color":      "green",
    "intensity":  0.8,
    "band_width": 0.4,
}
```

### Screensaver

`animations.screensaver` plays a shader once Rune has had no input for a while.
Each time it starts, it plays the next of the shaders you list, starting over
after the last one. The shader plays a pass at a time, over and over, until
input stops it. Each shader decides what it draws, including whether your
workspace still shows through.

- `shaders`: the [cell shaders](#cell-shaders) to play, by name, such as
  `matrix`, `rain` or `inferno`. A single name, such as `shaders: inferno`,
  plays just that one. An empty list means there is no screensaver.
- `idle_timeout`: how long Rune waits without input before it starts, such as
  `"10m"` (the default).
- `duration`: how long one pass lasts. Defaults to `"1m"`. The fires, `blaze`
  and `inferno`, keep their own pace, so a pass of one lasts as many whole
  cycles of the fire as it takes to cover `duration`: about 31s each for
  `blaze` and 100s for `inferno`.
- `time_shader`: how a pass plays the shader's animation:
  - `loop` (the default): from start to end.
  - `ping_pong`: to the end and back.
  - `reverse`: from end to start.
  - `accelerate`: starting slow and speeding up.
  - `decelerate`: starting fast and slowing down.
  - `boomerang`: to the end and back, slowing down at the end.
- `fps`: how many times per second it redraws. Defaults to 30.
- `enabled`: set to `false` to turn it off without removing your shaders.

Any key, mouse movement, click or paste stops it. That input is not swallowed:
it still reaches the editor, so the key that wakes Rune up is typed as usual.

```yaml tab
animations:
  screensaver:
    shaders: [matrix, rain, inferno]
    idle_timeout: 5m
    duration: 30s
    time_shader: ping_pong
```

```python tab
config["animations"]["screensaver"]["shaders"] = ["matrix", "rain", "inferno"]
config["animations"]["screensaver"]["idle_timeout"] = "5m"
config["animations"]["screensaver"]["duration"] = "30s"
config["animations"]["screensaver"]["time_shader"] = "ping_pong"
```

## Cursor effect

`animations.cursor` plays a shader over the window for a moment every time the
cursor jumps, such as to another window or another part of the file. It's off
by default, unlike the other animations, and plays in the GUI only. The
built-in shader is a flame that trails the cursor from where it jumped. Typing
plays nothing. Set it to `true` to play the flame, or set these keys:

- `enabled`: set to `true` to turn it on, or `false` to turn it off without
  removing your other settings.
- `shader`: the path of a cursor shader. `$VARS` in it are expanded. When it's
  empty, which is the default, Rune plays the built-in flame.
- `duration`: how long the shader plays after each jump, such as `"250ms"`
  (the default).

```yaml tab
animations:
  cursor:
    enabled: true
    shader: $HOME/.config/rune/cursor.kage
    duration: 400ms
```

```python tab
config["animations"]["cursor"]["enabled"] = True
config["animations"]["cursor"]["shader"] = "$HOME/.config/rune/cursor.kage"
config["animations"]["cursor"]["duration"] = "400ms"
```

### More cursor shaders

The [cursor pack](https://github.com/unstablebuild/rune-cursor-pack) bundles
more cursor shaders, such as a bolt of lightning that strikes the cursor, with
a preview of each. To install it, run this in the
[console](./console.md), then restart Rune:

```text
pkg install github.com/unstablebuild/rune-cursor-pack
```

The install turns the effect on with the lightning shader, unless your config
already sets `animations.cursor`: Rune keeps the values you set. The pack's
README lists the path of each shader to set as `shader`.

### Write your own

Cursor shaders are written in
[Kage](https://ebitengine.org/en/documents/shader.html), the Go-like shading
language of Ebitengine, which draws Rune's window. While it plays, the shader
draws every pixel of the window, which it reads with `imageSrc0At`: the window
as Rune drew it, without the cursor, which stays on top. The color it returns
replaces the pixel, so return `imageSrc0At(srcPos)` to leave a pixel unchanged,
or read another pixel to bend the text. Colors are premultiplied by their
alpha, so a color `c` goes over the window as `c + window*(1-c.a)`. Declare
`//kage:unit pixels` so that positions are in window pixels. Rune sets these
uniforms, which you declare only if you use them:

| Uniform | Type | Value |
| --- | --- | --- |
| `Time` | `float` | Seconds since the cursor jumped. |
| `Duration` | `float` | `duration`, in seconds. |
| `Cursor` | `vec4` | Where the cursor is: x, y, width and height in pixels. |
| `PreviousCursor` | `vec4` | Where the cursor jumped from. |
| `CursorColor` | `vec4` | The cursor color of the theme, premultiplied. |

Each shader decides how far the cursor must move to play it, with two
constants, measured in cells:

| Constant | Default | The shader plays when the cursor moves |
| --- | --- | --- |
| `MinJumpRows` | `2` | at least this many rows up or down. |
| `MinJumpColumns` | `9` | at least this many columns along the same row. |

With the defaults, typing, `Enter` and lines that wrap as you type play
nothing, and so do moves to the line above or below. While the shader plays,
it redraws the whole window up to 120 times a second, so each move that plays
it has a cost. A shader that sets both constants to `1` plays on every
keystroke, and pays that cost on every frame while you type. Smaller moves
keep a shader that is already playing going: `Cursor` follows them, while
`Time` and `PreviousCursor` stay those of the jump.

A shader that fails to compile, that declares one of the uniforms with another
type, or that sets one of the constants to anything but a whole number of at
least 1, turns the effect off and shows the error. Rune reads the shader when
it starts, so restart it to see your changes. This shader sends a ripple
through the text around the cursor on every keystroke, tinted with the cursor
color:

```go
//kage:unit pixels

package main

const MinJumpRows = 1
const MinJumpColumns = 1

var Time float
var Duration float
var Cursor vec4
var CursorColor vec4

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	center := Cursor.xy + Cursor.zw/2
	offset := dstPos.xy - center
	if length(offset) < 1 {
		return imageSrc0At(srcPos)
	}
	p := Time / Duration
	wave := exp(-abs(length(offset)-p*Cursor.w*6)/4) * (1 - p)
	window := imageSrc0At(srcPos - normalize(offset)*wave*6)
	tint := CursorColor * wave * 0.3
	return tint + window*(1-tint.a)
}
```

The shaders of the [cursor pack](https://github.com/unstablebuild/rune-cursor-pack)
are fuller examples.