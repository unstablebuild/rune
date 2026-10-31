---
name: rune-hud
description: >
  Generate images and sounds for Rune's GUI and wire them into the user's
  config.yaml: window frames, the scroll bar, floating window bars, the
  command prompt frame and separator sprites, the editor and agent status
  bars, the quick menu, prompt buttons and the wallpaper. Use when the user
  wants to create, restyle or iterate on the look or sound of Rune's interface.
---

# Rune HUD images and sounds

You generate image files and sound files, then point the user's config at them.
You cannot run Rune commands, so every iteration ends with the user reloading.

## Files
- **Data directory:** `$RUNE_DATADIR` when set. Otherwise it is `~/.rune`, or
  whatever `rune --datadir` was given; dev builds often use `~/.runedev`. If you
  can't find `config.yaml` there, ask the user.
- **Config:** `$RUNE_DATADIR/config.yaml`. If `config.star` exists instead, edit
  it with `config["a"]["b"] = ...` statements.
- **Assets:** `$RUNE_DATADIR/hud/<theme>/v<N>/...`, sounds in
  `$RUNE_DATADIR/hud/<theme>/sounds/`. Copy any asset the user hands you from
  elsewhere (such as `~/Downloads`) into `hud/<theme>/` before referencing it.
- **Manifest:** keep `hud/<theme>/manifest.yaml` up to date:

  ```yaml
  theme: ember
  version: 3              # the v<N> the config points at
  background: "#080808"   # the theme background, see Colour continuity
  accent: "#d0622a"       # the one accent colour
  light: top-left         # where every surface is lit from
  cell: {width: 10, height: 20}   # device pixels, measured below
  scale: 4                # images are authored at scale × cell
  outsets:
    scroll_bar: {left: 1}
    window_bar: {top: 4, bottom: 2, sides: 1, inward: 3, horizontal_top: 1}
    separator: {top: 1, bottom: 1, inward: 1}
    prompt: {top: 1, bottom: 1, sides: 1}
  ```

- **Editing the config:**
  - Back it up once per session: `cp config.yaml config.yaml.bak-$(date +%Y%m%d%H%M%S)`.
  - Patch only the keys you change, as text. Never round-trip the file through a
    YAML library: it drops the user's comments.
  - When you replace a value, leave a one-line comment naming the previous
    version, so the user can roll back.

Layout of a theme:
```
hud/<theme>/
  manifest.yaml
  wallpaper.png
  sources/<name>.png, <name>.prompt.txt
  sounds/<cue>.wav
  v<N>/frame/{normal,focused,prompt}/<slot>.png
  v<N>/scroll/{top,middle,bottom,hover-top,hover-middle,hover-bottom}.png
  v<N>/window-bar/{left,middle,right}.png
  v<N>/separator/{left,horizontal,right}.png
  v<N>/status/<name>.png
  v<N>/quickmenu/{idle,hover,pressed}/<button>.png
  v<N>/button/{idle,selected,hover,pressed}/{left,middle,right}.png
```
Use lowercase kebab-case file names, with no spaces, `?`, `#` or `%`: they
would need percent-encoding in a `file://` URI.

## Rules that apply to every image
1. **Use absolute URIs**, except for the wallpaper. Image values are
   `file:///absolute/path.png` or `https://...`, with no `~` and no `$VARS`.
   `workspace.wallpaper_image` is the exception: it takes a plain path, which
   may use `~` and environment variables.
2. **Use a new URI for every iteration.** Images are cached by URI for 24 hours,
   so a file overwritten in place is never shown. Write each iteration to a new
   `v<N>` directory, or append `?v=<N>` to the URI: the cache key is the whole
   URI, while the file read ignores the query. Keep the previous `v<N>` for
   rollback. The wallpaper is not cached: overwrite it in place and reload.
3. **Formats:** PNG with alpha is preferred. JPEG, GIF and WebP also load, except
   for the wallpaper, which must be a PNG. Each side is at most 8192 px.
4. **Images are drawn as they are.** Attrs, dimming, b&w and the theme do not
   tint them, so bake the colours in.
   - Ship separate normal and focused frame sets.
   - Transparent pixels show the cell background behind them (for frames, the
     background of `frame_attr` or `focus_frame_attr`).
5. **The terminal UI ignores images** and draws the characters. The GUI also
   falls back when images are unavailable, except separator junctions try the
   frame's vertical side image first (see Fallbacks). Keep characters sensible.
6. **Size everything in cells.** An image covering w × h cells is drawn into
   (w·cw) × (h·ch) device pixels, where (cw, ch) is the cell size.
   - The cell size changes with the font size, `gui.column_width_offset`,
     `gui.line_height_offset` and the display.
   - Author at an integer multiple k of the exact size: (w·cw·k) × (h·ch·k).
   - `fill` images are stretched to the exact rectangle, so a wrong aspect ratio
     distorts them. `contain` images are letterboxed.

## Design rules
A HUD reads as one object only when every surface follows one system. Clean
geometric frames and icons work. Figures assembled from vector shapes look
childish, and ornament repeated on every small surface turns into clutter.

1. **Start from one reference surface.** Design the window frame first and get
   the user's approval. Every other surface reuses its rail thickness,
   material, light direction and palette. Record them in the manifest.
2. **One accent; states change light only.** Use the one accent for focus and
   for the keyboard-selected button. Hover brightens, pressed darkens or
   insets. No state introduces a new hue.
3. **One continuous surface.** The theme background is the canvas (see
   Colour continuity). Any fill meant to read as background is transparent or
   exactly that colour, never a near-black of your own.
4. **Ornament budget by surface:**
   - Wallpaper and status-bar end ornaments: the only places for figures
     (creatures, skulls, statues, scenes). They are painted art, see below.
   - Window frames, the command prompt frame and the window bar: rails and
     corners only. Corners may carry a small geometric motif; edges stay plain
     and tileable.
   - Command separator ends: true three-way T-junctions, never repurposed
     corners. A small triangular ornament is appropriate; preserve the rails
     above, below and inward, matching the frame's palette and lighting.
     The middle may reuse the frame's bottom-edge tile.
   - Prompt buttons: a border or bevel and a flat fill, nothing figurative. One
     prompt shows several buttons, so anything on a button multiplies.
   - Scroll bar: a plain thumb in the frame's material.
   - Quick menu: one icon per button, all in the same style, line weight and
     plate.
5. **Minimum size.** A figure needs about 3 rows (≈100 device px) to read.
   Below that, use a silhouette or a simple icon, or leave it out. Judge at
   true device size, never enlarged.
6. **Outsets hug the structure.** Window bar end outsets draw over window
   content: use them only for the bar's own rails and caps, keep T, B and I at
   0 or 1 unless the user asks for more, and leave every other outset pixel
   transparent.
7. **Joins.** Where surfaces meet, rails continue on the same pixel row or
   column, at the same thickness and colour: window bar ends over the frame's
   side edges (the edge's line sits at the left edge of column S in `left` and
   the right edge of column I in `right`), the scroll bar against the frame,
   and button ends against their middles. A few pixels off looks broken.
8. **Tiling.** Edge and middle slices repeat once per cell. Keep them flat
   along the tiling direction, or use a texture whose period divides the cell
   exactly. Test by repeating the slice 12× at true size.

## Colour continuity
The wallpaper, empty windows and every transparent pixel show the theme
background, so build the HUD on that exact colour.
- Read `gui.default_theme`, then its `background`: from `gui.themes.<name>` in
  the user's config when it's defined there, otherwise from the built-in list
  below. Record it in the manifest.
- Built-in backgrounds: furman `#202020`, romero `#000000`, carmack `#080808`,
  reynolds `#121212`, brevik `#080808`, thompson `#0f1114`, pike `#ffffea`,
  hopper `#000000`, kernighan `#0c0c0c`, wozniak `#120F1D`, auge `#270d06`,
  ritchie `#f8f8f8`, kelleher `#000000`, sanfilippo `#000000`, frost
  `#000000`, mullen `#0d0d1b`.
- The wallpaper's own background must be that colour, not a nearby one, and
  its edges must fade to it exactly. With `contain` the image's edges sit
  inside the window, so any step shows as a rectangle.
- Leave `workspace.wallpaper_background_attr` unset, or give it the same `bg`.
- Opaque fills in frames, bars and buttons follow the same rule.
- If the art can't be matched, the alternative is a custom theme whose
  `background` is the art's colour (`gui.themes.<name>`, then
  `gui.default_theme`, then a restart). Ask first: it changes every window.

## Painted art
Figures (creatures, skulls, statues, scenes) are high-fidelity painted raster
art from an image-generation model. Never build them from SVG primitives or
procedural shapes. Draw vectors only for geometric parts: frames, rails,
bevels, buttons, the scroll bar and icons.

**Get the art:**
1. Use the best state-of-the-art image model you can reach, in this order:
   - an image-generation tool or skill in your tool list;
   - an MCP server that provides one;
   - a hosted API the user has a key for in the environment, such as OpenAI's
     image models, Google's Gemini or Imagen, or FLUX. Ask before spending the
     user's credits.

   With an OpenAI model and API key, or a Codex model with a Codex login,
   Rune offers `imagegen` for generation and editing. Account access and usage
   limits are enforced by the provider. Use it when it
   appears in your tool list; copy generated files into your sources directory
   and keep the originals.
2. If none is available, stop and say so. Offer your prompts so the user can
   generate the art, or ask for art they own. Never fall back to drawing
   figures yourself.
3. Keep each source and its prompt in `hud/<theme>/sources/`.

**Prompt for integration, not just the subject:**
- The style from the user's brief, plus the manifest's palette, accent and
  light direction.
- "Isolated on a flat `<background>` background", or a transparent background
  if the model supports one. No text, border, frame, vignette or drop shadow,
  and the whole subject in frame with a margin.
- The target rectangle's aspect ratio, at 2× its final device size or more.
- Generate several candidates. Show the user the best two or three,
  composited on the theme background at true size, before wiring any in.

**Post-process:**
- Status-bar ornaments: remove the background to alpha (the model's
  transparent output, a background-removal tool, or
  `-fuzz 3% -transparent "<background>"` when it's truly flat). Crop to the
  target's aspect, then downscale:
  `magick in.png -filter Lanczos -resize WxH! -unsharp 0x0.6+0.6+0 out.png`.
  Place them with `height 3`, `overflow` and `alt ""`.
- Wallpaper: sample a corner and confirm the background matches. Then
  feather the edges to transparent:

  ```sh
  W=$(magick identify -format %w in.png); H=$(magick identify -format %h in.png); F=$((W/12))
  magick in.png \( -size ${W}x${H} xc:black -fill white \
    -draw "rectangle $F,$F $((W-F-1)),$((H-F-1))" -blur 0x$((F/3)) -level 2%,98% \) \
    -alpha off -compose CopyOpacity -composite wallpaper.png
  ```

- Check the wallpaper's edges. All four must print 0:

  ```sh
  bg='#080808'
  magick wallpaper.png -background "$bg" -flatten flat.png
  for g in North South East West; do
    case $g in North|South) c=100%x2+0+0;; *) c=2x100%+0+0;; esac
    magick flat.png -gravity $g -crop $c +repage \( +clone -fill "$bg" -colorize 100 \) \
      -compose difference -composite -format "$g %[fx:round(maxima*255)]\n" info:
  done
  ```

## Measure the cell
Ask the user to paste this into a terminal tab of the Rune GUI. Your own shell
usually has no terminal attached, so it can't answer.

```sh
bash -c 'stty -echo -icanon min 0 time 10; printf "\033[16t"; IFS="[;t" read -r -d t _ _ ch cw; stty sane; echo "cell ${cw}x${ch}"'
```

- `cell x` with no numbers means it isn't the Rune GUI: the terminal UI and other
  terminals don't report a cell size.
- Measure before generating anything. Cells aren't always 1:2 (14×34, about
  1:2.4, is common), and `fill` stretches art drawn for the wrong ratio.
- Only if the user can't measure, assume ch = 2·cw and author at 96×192 px per
  cell, which scales down cleanly. Pixel art must be authored at the exact
  measured size.
- Ask again whenever the user changes the font size.

## Surfaces
Abbreviations:
- **L**: the scroll bar outset `left`.
- **T, B, S, I, H**: the window bar outsets `top`, `bottom`, `sides`, `inward`
  and `horizontal_top`. Prompt buttons use T, B and S too, from their own outset.
  Command separator junctions use only T, B and I from their own outset.
- **Tiles →** / **tiles ↓**: one copy is drawn per cell, so the image must tile
  seamlessly in that direction.

| Surface | Key | Images | Cells (w × h) | Fit |
|---|---|---|---|---|
| Window frame | `browser.window_manager.frame_charset.graphics` | `topleft` `topright` `bottomleft` `bottomright` | 1 × 1 | fill |
| | | `horizontaltop` `horizontalbottom` (tiles →), `verticalleft` `verticalright` (tiles ↓) | 1 × 1 | fill |
| Focused frame | `browser.window_manager.focus_frame_charset.graphics` | same 8 | 1 × 1 | fill |
| Command prompt frame | `command.frame_charset.graphics` | same 8 | 1 × 1 | fill |
| Command separator | `command.separator_charset.graphics` | `left`, `right` | (1+I) × (T+1+B) | fill |
| | | `horizontal` (tiles →) | 1 × 1 | fill |
| Scroll bar | `browser.window_manager.scroll_bar_charset.graphics` | `top_char`, `middle_char` (tiles ↓), `bottom_char`, and the `hover_` version of each | (1+L) × 1 | fill |
| Floating window bar | `browser.window_manager.window_bar_charset.graphics` | `left` | (S+1+I) × (T+1+B) | fill |
| | | `right` | (I+1+S) × (T+1+B) | fill |
| | | `horizontal` (tiles →) | 1 × (H+1) | fill |
| Prompt buttons | `browser.prompt.images.<idle\|selected\|hover\|pressed>` | `left` | (S+1) × (T+1+B) | fill |
| | | `middle` (tiles →) | 1 × (T+1+B) | fill |
| | | `right` | (1+S) × (T+1+B) | fill |
| Editor status bar | `editor.status_bar.layout` | `.Image` elements | `width` × `height` | contain, or `fit "fill"` |
| Agent status bar | `extensions.rune-agent.config.status_bar.layout` | `.Image` elements | `width` × `height` | contain, or `fit "fill"` |
| Quick menu | `gui.quick_menu[i].sprite` | `idle` `hover` `pressed` | a square 4·cw px a side | contain |
| Wallpaper | `workspace.wallpaper_image` | a path, or `{path, width, height, fit}` | `width`/`height` in cells or `N%`, default 60% | `contain` (default), `cover` or `fill` |

Worked sizes at 96×192 px per cell:
- A frame slot is 96×192.
- Separator ends with T=1, B=1, I=1 are 192×576; the middle stays 96×192.
- A scroll bar slot with L=1 is 192×192.
- Window bar ends with T=4, B=2, S=1, I=3 are 480×1344, and `horizontal` with
  H=1 is 96×384.
- Prompt button ends with T=1, B=1, S=1 are 192×576, and `middle` is 96×576.
- An 18×3 status image is 1728×576.
- A quick menu button is 384×384.

### Requirements
- Frame and scroll bar images are drawn only while
  `browser.window_manager.frame` is true.
- Command separator images require `browser.window_manager.frame` and a
  visible manual/list separator: enable `command.show_manual` and open a
  command with manual content and enough prompt space to display it.
- Window bar images are drawn only while `browser.window_manager.window_bar`
  is true.
- Quick menu sprites move the bar from the native macOS bar to one Rune draws,
  on every platform. Set `sprite` on every button or on none; `symbol` isn't
  needed with one. Keep each button's `title` (its tooltip) and `command`.
- Prompt button images draw under the buttons of the prompts Rune opens through
  its standard prompt, such as the close-dirty-tabs prompt. Prompts built
  elsewhere, and the search box's buttons, keep their colours for now. Confirm
  on screen which prompts show them.

### Where the anchor cell sits in each image
Line up rails and seams across slices at these positions.

**Window frames:**
- Edges are one cell thick.
- Keep the line at the cell's outer edge, where the default `▔ ▁ ▏ ▕`
  characters sit, so content keeps its spacing.
- Corners must meet both edges at the cell boundary.

**Scroll bar:**
- The bar's own cell is the image's rightmost column.
- `top_char` and `bottom_char` are used only when the thumb spans 2 cells or
  more.
- The pointer grabs the bar from its outset cells too.

**Command separator:**
- In `left`, the anchor is column 0, row T; in `right`, column I, row T.
  The vertical rail runs along the outer edge and connects above and below.
- Join inward at the same pixel Y within the anchor row as the chosen
  `horizontalbottom` tile's rail, not necessarily the cell centre. Author the
  complete three-way junction: a successful image replaces the side sprite
  at its anchor, rather than drawing over that sprite.
- `horizontal` repeats once per interior cell, including the one-cell
  horizontal padding. It is always 1×1, regardless of junction outsets.
- Ends draw last. Their top/bottom/inward outsets are clipped to prompt bounds,
  not resized at edges. They reserve no layout space and expand no pointer hit
  area; they can cover content. Keep ornaments small and unused pixels clear.

**Window bar:**
- In `left`, the bar's first cell is at column S, row T.
- In `right`, the bar's last cell is at column I, row T.
- In `horizontal`, the bar is the last row.
- The close icon and title are drawn over the bar row.
- The end images are drawn last. Their outsets cover window content and are
  clipped to the window area. They don't change what the pointer can grab.
- Focused and unfocused windows share these images.

**Prompt buttons:**
- In `left`, the label's first cell is at column S, row T.
- In `middle`, the label is row T.
- In `right`, the label's last cell is at column 0, row T.
- A label one cell wide is covered by both `left` and `right`.
- The images go under the label's text. The label keeps the foreground of
  `browser.prompt.{text,highlight,hover,clicked}_attr` by state, and its
  background is dropped. Keep row T calm so the label stays readable, and tune
  those attrs' `fg` if it isn't.
- Prompts reserve room for the outsets, so a larger T+B makes prompts taller.
- Buttons are max(1, 2S) cells apart. With S ≥ 1, neighbouring buttons' outset
  columns touch with no gap, so keep the art off the outer edge of the outset.

**Status bar `.Image`:**
- The image is `width` × `height` cells, centred on its anchor: the first
  reserved cell.
- The anchor is at column ⌊w/2⌋, row ⌊h/2⌋ of the image, before `x_offset`,
  `y_offset` and the pixel offsets move it.
- An image taller than 1 row needs `overflow` to show its rows above and below
  the bar.

**Quick menu:**
- Buttons are square and contain-fitted, stacked half a cell apart from row 3.
- Draw all three states on the same canvas, so nothing jumps between states.
- Put any padding inside the art.

### Outset keys
```yaml
browser:
  window_manager:
    scroll_bar_charset: {graphics: {outset: {left: 1}}}
    window_bar_charset: {graphics: {outset: {top: 4, bottom: 2, sides: 1, inward: 3, horizontal_top: 1}}}
  prompt:
    images: {outset: {top: 1, bottom: 1, sides: 1}}
command:
  separator_charset: {graphics: {outset: {top: 1, bottom: 1, inward: 1}}}
```
Outsets are whole cells, 0 or more. Separator outsets default to zero and have
no `sides` or `horizontal_top` field.

### `mode`
Every `graphics` table takes a `mode`:
- `replace` (the default) hides the character under each image.
- `overlay` keeps it, showing through the image's transparent pixels in its own
  colour.
- For the window bar, `replace` sits the icon and title on the frame's
  background, and `overlay` keeps the bar's colours behind them.
- A separator's omitted mode inherits the effective command frame mode rather
  than independently defaulting to `replace`. A successful junction image in
  `replace` hides its glyph; `overlay` keeps the separator glyph, not the side
  sprite.

`browser.prompt.images` takes no `mode`.

### Fallbacks
- **Scroll bar:** a missing `top_char` or `bottom_char` uses the middle image,
  and a missing `hover_` image uses the plain one.
- **Command prompt frame:** slots `command.frame_charset.graphics` leaves out
  keep the normal window frame's images, forming the effective command frame.
- **Command separator:** omitted `graphics.horizontal` inherits that effective
  frame's `graphics.horizontalbottom`. Explicit `horizontal: ""` clears only
  the image, keeping the glyph rule. Missing, cleared, loading or failed
  junction images use the corresponding vertical side image if available,
  otherwise the separator rune; terminal rendering always uses runes. An
  unavailable horizontal image uses the frame's `horizontalbottom` rune, with
  `horizontal_left`/`horizontal_right` near the ends. Ends never inherit corners.
- **Quick menu:** `hover` defaults to `idle`, and `pressed` to `hover`. At most
  24 buttons.
- **Prompt buttons:** `idle` is required whenever any other state is set.
  `selected` (the button the keyboard is on) and `pressed` fall back to `hover`,
  and `hover` to `idle`.

### Minimal command separator config
These absolute URIs are placeholders, not existing assets. Replace them with
newly written asset paths and merge these keys into the user's config:

```yaml
browser:
  window_manager:
    frame: true
command:
  show_manual: true
  separator_charset:
    graphics:
      left: file:///absolute/path/hud/theme/v1/separator/left.png
      right: file:///absolute/path/hud/theme/v1/separator/right.png
      outset: {top: 1, bottom: 1, inward: 1}
```

Omitting `horizontal` reuses the effective frame's bottom image (if configured);
omitting `mode` inherits its mode. Set `horizontal` to a new absolute URI only
when a distinct middle tile is needed.

### Keys that look like they take images but don't
Never write images to these:
- `notifications.frame_charset.graphics` and
  `extensions.rune-agent.config.input_box_frame_charset.graphics`: accepted, but
  never drawn.
- `.Image` in `terminal.plugin.bar_layout` or `editor.<mode>.message_bar.layout`:
  a config error.
- `browser.frameunion_charset` and `browser.window_manager.close_icon`:
  characters only.

Characters `\U00100008`–`\U00100011` are glyphs Rune's GUI font draws itself.
Keep them under `overlay` images: they show through the transparent pixels.

Animations (`animations.*`: the cursor, command prompt, tabs and screensaver)
are shaders, not images. They're out of scope for this skill.

### Status bar image directives
`{{ .Image | src "file:///…/left.png" | width 18 | height 3 | reserve 3 | overflow | x_offset 8 | z_index 10 | alt "" }}`

- `src`: required.
- `width`: cells or `N%`; default 2.
- `height`: rows; default 1.
- `reserve`: cells the element takes up in the layout; defaults to `width`.
- `x_offset`, `y_offset`: cells.
- `x_pixel_offset`, `y_pixel_offset`: device pixels.
- `fit`: `contain` (default) or `fill`.
- `z_index`: 0 or more draws over the text; negative draws under it.
- `overflow`: lets the image extend past the bar.
- `max_cut`: default 1.
- `alt`: drawn when the image can't be shown. The default is a cross glyph, so
  set `alt ""`.

Keep the layout's text fields (`.Status`, `.Model`, `.Filepath`, ...) as they
are.

## Sounds
- **Registry:** `sounds:` maps a name to a file path, another name, or `""` to
  silence it.
  - Paths may use `$RUNE_DATADIR`, `$HOME` and `file://`.
  - Name your own sounds `hud-<theme>-<cue>`. A name can't contain `/`.
- **Reserved names:** `error`, `warning`, `info` and `success`. Mapping one of
  them overrides its built-in tone everywhere Rune plays it.
- **Installed pack:** check `sounds:` for `uisfx-<pack>-<cue>` entries before
  generating anything.
  - Packs: arcade, cinematic, dreamy, glass, mechanical, minimal, organic,
    rubber, scifi, soft, studio and zen.
  - The 78 cues include hover, press, release, select, open, close, error,
    warning, success, blocked, expand, collapse, toggle-on and toggle-off.
  - Reference a cue by name (`click: uisfx-glass-press`). Write new files only
    for sounds the pack doesn't have.
- **Hooks:**
  - `terminal.bell`;
  - `program_status.sounds` with `blocked`, `error`, `done` and `working`;
  - `gui.quick_menu_sounds` with `hover` and `click`, which play only with
    sprites.
  - Any command: make it a `command.aliases` entry whose steps end with
    `guisound <name>`, then bind the alias, or use it as a quick menu `command`.
- **No hooks** exist for prompt buttons, window bars, the scroll bar or status
  bars. Don't invent keys for them.
- **Files:**
  - WAV, MP3 or Ogg Vorbis, decoded to 48 kHz stereo.
  - Only the first 10 s plays.
  - Suggested lengths: hover 20 to 60 ms, click 40 to 120 ms, status cues 150
    to 600 ms.
  - Start with no leading silence, peak at -3 dBFS, and keep hover quieter than
    click.
- **Volume:** `gui.sound_volume`, from 0 to 1. The user can audition a sound
  with `guisound <name>`.

```yaml
sounds:
  hud-ember-hover: $RUNE_DATADIR/hud/ember/sounds/hover.wav
  hud-ember-click: $RUNE_DATADIR/hud/ember/sounds/click.wav
  warning: uisfx-glass-warning
terminal: {bell: uisfx-glass-blocked}
program_status: {sounds: {blocked: warning, error: error, done: success}}
gui: {quick_menu_sounds: {hover: hud-ember-hover, click: hud-ember-click}}
```

## Apply and verify
End every iteration by telling the user exactly what to run.

| Change | Takes effect |
|---|---|
| `browser.*`, `command.*`, `editor.status_bar`, `extensions.*`, `sounds`, `terminal.bell`, `workspace.wallpaper_image` | `:workspacereload` in the focused workspace |
| `gui.*` (quick menu, quick menu sounds, `sound_volume`), `program_status.*`, `animations.*`, the home screen | Restart Rune. `:guiwindownew` opens a fresh process to preview them |
| A sound file overwritten at the same path | The next time it plays |
| The wallpaper file overwritten at the same path | `:workspacereload` |
| Any other image overwritten at the same URI | Never: bump `v<N>` |

Separator sprites require a compatible Rune binary: rebuild/restart it first
when adopting this feature, then use `:workspacereload` for command settings.

**Errors:**
- Config errors appear as notifications after the reload.
- One bad URI rejects its whole `graphics` table, and the previous images stay.
- An unreadable wallpaper is a config error.
- Other missing or unreadable images use their slot fallback; separator
  junctions try the vertical side image before the rune, as described above.
  Load failures log a warning to the file named by `log_path`.

## Loop
1. Read the config, the manifest and the theme background.
2. Measure the cell, or reuse the manifest's if the font size hasn't changed.
3. Agree on a brief with the user: style, palette, accent, light direction,
   and which anchors get painted figures.
4. Work one surface per iteration: the window frame first, then the surfaces
   that join it, then the rest. Never ship the whole HUD at once.
5. Compute each image's pixel size from the table above.
6. Generate the images (figures per Painted art) and sounds.
7. Check your work:
   - view each PNG;
   - compose mocks at the true cell size and inspect the seams: a frame row
     (corner, 3 edges, corner), a window bar (`left`, 4 × `horizontal`,
     `right`), a 6-cell scroll thumb, and a prompt button (`left`, 4 ×
     `middle`, `right`) with its label drawn on row T.
   - preview the full command separator at true size, joining vertical rails
     above and below both ends. Include interior padding, check the middle's
     pixel-Y alignment, and test narrow prompt clipping without shrinking the
     junction art. Check inherited middle, cleared middle and failed-image
     fallbacks, plus replace/overlay glyph behaviour.
   - compose the real scene at true device size on the theme background: a
     floating window with its bar beside a framed window, a prompt with three
     buttons, the status bar and the wallpaper. View it at 100%, never
     enlarged or on another background. Count the figures and accent colours
     in view against the Design rules.
   - run the wallpaper edge check.
8. Write the files to a new `v<N>` directory and update the manifest.
9. Patch the keys, using `file:///` URIs to the new files.
10. Tell the user which reload to run and what to look at, and ask for a
    screenshot. Fix any difference from your mock before starting the next
    surface.
