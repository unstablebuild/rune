---
name: rune-hud
description: >
  Edit Rune's configuration and generate sprites to refine its UI:
  window frames, scroll bars, floating window bars, command prompts, editor
  and agent status-bar layouts, the quick menu, prompt buttons, wallpaper and
  sounds. Discover the configuration, edit it, run runectl screenshot and
  inspect the result yourself. This is configuration and asset editing, not
  Rune source development. Use when the user wants to create, restyle or
  iterate on Rune's appearance or sounds.
---

# Rune HUD images and sounds

Own the edit-and-verify loop. Read the user's configuration and existing assets,
make the requested changes, run `runectl screenshot`, inspect the PNG and fix
what looks wrong. Do not ask the user to measure cells, run diagnostic commands
or provide screenshots you can capture yourself. The configuration and
status-bar layout reference is included in this file; no other document or
Rune source checkout is needed.

Use the user's brief and existing theme to make design decisions. If a detail
is unspecified, choose a consistent default and proceed. Do not stop for
approval between surfaces or revisions unless the user asked for checkpoints.

## Scope: configuration and assets only
- Edit the user's active configuration, theme assets, manifest and backups.
  Generate or edit the requested sprites and wallpaper; generate sounds only
  when requested. Keep unrelated configuration and artwork unchanged.
- Do not locate, read or modify Rune's source code to carry out an appearance
  request. Do not write parser tests, run test suites, build Rune, install a
  development toolchain or implement new template features.
- This boundary still applies when the current workspace happens to be Rune's
  repository. The target is the user's configuration and theme, not the project
  open in the editor. Restrict file discovery to those configuration and asset
  locations instead of searching source directories.
- Use the syntax and examples below. If the requested effect is unsupported,
  explain the limitation and use a supported configuration or artwork approach;
  do not turn the styling task into a code change.
- Verify appearance with `runectl screenshot` and image inspection, not source
  analysis or test programs.

## Checking your work
Take a baseline screenshot before editing. Run `runectl screenshot` yourself,
then open the PNG at the printed path with your image-reading tool. Use the
rendered result to judge proportions, spacing, legibility and seams. Repeat
after each revision; run captures sequentially.

Before each capture, Rune reads the active configuration file again and applies
these keys to every open workspace:

- `browser.window_manager`: `frame_attr`, `focus_frame_attr`, `frame_charset`,
  `focus_frame_charset`, `scroll_bar_attr`, `scroll_bar_charset`,
  `scroll_bar_char`, `scroll_bar_hover_char`, `window_bar`, `window_bar_charset`,
  `close_icon` and `close_icon_attr`;
- `browser.frameunion_charset`;
- `browser.prompt`: `text_attr`, `highlight_attr`, `hover_attr`,
  `clicked_attr`, `background_attr` and `images`;
- `command.frame_charset`, `command.separator_charset` and
  `command.show_manual`;
- `editor.status_bar`: `layout`, `background_attr` and `foreground_error_attr`;
- `extensions.rune-agent.config.status_bar`, every key;
- `gui`: `quick_menu`, `quick_menu_sounds`, `themes`, `default_theme` and
  `sound_volume`;
- `program_status.sounds`;
- `terminal.bell`;
- `workspace`: `wallpaper_image`, `wallpaper_image_ascii`, `wallpaper_ascii`,
  `wallpaper`, `wallpaper_attr`, `wallpaper_background_attr` and
  `wallpaper_density_characters`;
- `sounds`.

Rune also reads again every image these keys name, including a file you
overwrote at the same path. Existing prompts retain their style until reopened.
`gui.default_theme` and `gui.sound_volume` apply only when they differ from the
values Rune read last, so a theme or volume the user picked since stays. The
agent redraws its own status bar a moment after Rune reloads; if a capture
still shows the old agent bar, capture again.
Do not assume every configuration key is live-reloaded: the exceptions are
listed under Apply and verify. Inspect the surfaces visible in the capture;
do not claim to have verified a prompt or state that was not shown.

## Files
- **Data directory:** read `$RUNE_DATADIR` yourself with
  `printf '%s\n' "$RUNE_DATADIR"`, including when it names a development
  installation. Do not dump the full environment, which can contain credentials.
- **Config:** read `$RUNE_DATADIR/config.yaml`, or `config.star` if the YAML file
  is absent. If the user supplied another config path, edit that file instead.
  Edit Starlark with `config["a"]["b"] = ...` statements. Do not create a new
  default config over an unidentified installation. Ask only if these checks
  leave a genuine ambiguity or access blocker, not as a routine first step.
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
  outsets:
    scroll_bar: {left: 1}
    window_bar: {top: 4, bottom: 2, sides: 1, inward: 3, horizontal_top: 1}
    separator: {top: 1, bottom: 1, inward: 1}
    prompt: {top: 1, bottom: 1, sides: 1}
  ```

- **Editing the config:**
  - Back up the active config once per session, using a timestamped filename.
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
2. **Overwrite an image in place only if a key under Checking your work names
   it.** Rune caches images by URI for 24 hours by default, but
   `runectl screenshot` reads those images again, so you can overwrite them
   while you iterate. Save each version you ship in a new `v<N>` directory
   and keep the previous one for rollback.
   - The wallpaper is not cached: overwrite it in place.
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
6. **Use screenshots to size and position the art.** Configuration dimensions
   are in text cells; source images are scaled by Rune. Reuse existing asset
   dimensions when suitable, or start with a high-resolution raster matching
   the target's approximate proportions. Do not block on exact pixel metrics.
   - `fill` stretches to the configured rectangle; `contain` preserves the
     source aspect ratio and can leave empty space.
   - Apply a first version, inspect the screenshot, then adjust source
     proportions, dimensions, offsets or outsets. Repeat until it fits.
   - Judge pixel art in the rendered screenshot as well. Recheck it after font
     or display settings change.

## Design rules
A HUD reads as one object only when every surface follows one system. Clean
geometric frames and icons work. Figures assembled from vector shapes look
childish, and ornament repeated on every small surface turns into clutter.

1. **Start from one reference surface.** For a new theme, establish the window
   frame first and verify it in a screenshot. For a targeted request, start
   with that surface and match the existing theme. Reuse its rail thickness,
   material, light direction and palette; record them in the manifest.
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
5. **Minimum size.** A figure needs about 3 rows to read.
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
   along the tiling direction. Test by repeating the slice 12×, then inspect
   the joins in Rune's screenshot.

## Colour continuity
Match background fills to the active theme. Transparent image pixels reveal
whatever is underneath: usually the theme background, but also status colours,
text or other images. Use that intentionally for status-bar ornaments.
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
  `gui.default_theme`). Ask first: it changes every window.

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
- Generate candidates as needed, choose the best match to the brief and verify
  it in Rune. Do not require the user to pick a candidate before proceeding
  unless they requested alternatives.

**Post-process:**
- Status-bar ornaments: remove the background to alpha (the model's
  transparent output, a background-removal tool, or
  `-fuzz 3% -transparent "<background>"` when it's truly flat). Crop to the
  target's aspect, then downscale:
  `magick in.png -filter Lanczos -resize WxH! -unsharp 0x0.6+0.6+0 out.png`.
  Start with `height 3` and `alt ""`. Add `overflow` only when the image must
  cross the editor or chat component's outer bounds.
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
| Quick menu | `gui.quick_menu[i].sprite` | `idle` `hover` `pressed` | square, each side as wide as 4 text columns | contain |
| Wallpaper | `workspace.wallpaper_image` | a path, or `{path, width, height, fit}` | `width`/`height` in cells or `N%`, default 60% | `contain` (default), `cover` or `fill` |

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
- An image can extend above the bar into editor or chat content without
  `overflow`. `overflow` permits drawing outside that component's bounds.

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

## Editor and agent status-bar layouts

Use this reference to edit the user's configuration without a Rune source
checkout. Dimensions below are configuration units, not measurements the user
must supply. Start with the existing layout and refine the result using
screenshots. Preserve the user's useful status information while changing its
presentation.

### Configuration paths and activation

| Bar | Layout key | Other relevant keys |
|---|---|---|
| Editor | `editor.status_bar.layout` | `background_attr.bg`, `foreground_error_attr.fg` under `editor.status_bar` |
| Agent | `extensions.rune-agent.config.status_bar.layout` | `enabled`, `background_attr`, `status`, and the gauge settings below, under `extensions.rune-agent.config.status_bar` |

`runectl screenshot` applies both bars' keys to the open editors and chats and
to those opened later, and reads their images again, so you can overwrite an
image at the same path while you iterate.

Merge the examples into the existing configuration; do not replace unrelated
keys or remove the user's useful fields. In Starlark, the equivalent paths are
`config["editor"]["status_bar"]["layout"]` and
`config["extensions"]["rune-agent"]["config"]["status_bar"]["layout"]`.

### Layout syntax

A layout is a single-line string containing literal text and actions such as
`{{ .Status | bold }}`. It uses a restricted template syntax, **not a general
Go template**.

- Field names are case-sensitive. The complete field lists are below.
- Pipes apply the styling directives below; image directives apply only to
  `.Image`.
- `.ShiftRight` is a pivot: elements before it align left, elements after it
  align right, with the unused space between them. Use at most one pivot.
- Use each ordinary field at most once. Repeating `.Status`, for example, does
  not create independently styled copies: the occurrences share one component
  and the last occurrence's formatting. Multiple `.Image` elements are allowed.
- `if`, `else`, `range`, variables, nested field lookups, `printf`, and arbitrary
  expressions are not supported.
- There is no `.Text`, `.Space`, or `.Padding` field, nor a `prefix`, `suffix`,
  `pad`, `align`, `flex`, `format`, or `attr` directive. Use literal text and the
  spacing rules below. `width` cannot be used on text fields.
- Keep YAML layout strings quoted. Single-quoted YAML lets the pipeline's
  double quotes remain unescaped. Avoid multiline layout strings: line breaks
  are not a row-layout feature.

#### Text styling directives

These directives apply to the field and its attached literal text:

| Directive | Syntax |
|---|---|
| Foreground | `fg "silver"`, `fg "#c0c0c0"`, `fg "default"` |
| Background | `bg "navy"`, `bg "#101828"`, `bg "default"` |
| Style flags | `bold`, `italic`, `underline`, `reverse`, `blink`, `dim`, `strikethrough` |

Colours accept lowercase W3C colour names, six-digit `#RRGGBB` values and
`default`. Named colours can be remapped by Rune's active theme. Style flags
take no arguments. These directives do not tint image pixels.

Runtime status colours take precedence over layout colours. If a runtime
attribute sets either foreground or background, its colour pair replaces the
layout's pair rather than filling in only the missing side. Style flags are
combined. A missing background falls back to the bar's background. `default`
does not force a dynamically coloured field to ignore its runtime colour.

#### Literal text, padding and separators

Literals belong to an adjacent field and inherit its attributes; they are not
independently styled segments. Their placement matters:

1. Text before the first field is its prefix.
2. On the left side of `.ShiftRight`, text after a field attaches to that field
   until the first **two consecutive spaces**. Those two spaces and the
   remaining text become the next field's prefix.
3. On the right side, text through the first two-space separator stays with
   the preceding field; any remaining text prefixes the next field.
4. Trailing text attaches to the last field.
5. Put the first action immediately after `.ShiftRight`. This avoids an editor
   parser difference: the agent preserves a literal directly after the pivot,
   but the editor can attach it to the invisible pivot instead.

For example, on the left side:

```gotemplate
{{ .Status }} {{ .Model }}
```

The single space has the Status colour. In this version:

```gotemplate
{{ .Status }}  {{ .Model }}
```

The two spaces prefix Model and have Model's colour. Adding several spaces
*after* Status therefore does not necessarily extend its coloured background.
For a coloured area before Status, place padding in its prefix; see the
transparent-ornament example below.

In the agent bar, an empty value hides its attached literals too. Do not attach
a separator that must always remain visible to an optional field such as
`.Elapsed` or `.MaxTokens`.

### Editor fields

| Field | Display |
|---|---|
| `.Status` | Editor mode or status message; mode-dependent attributes are supplied by the editor. |
| `.Filepath` | Workspace-relative filepath and dirty, recovered or read-only indicator. |
| `.GitShortRef` | Git HEAD short reference; `untracked` when the reference cannot be obtained. |
| `.GitDiffAdd` | Added-line count. |
| `.GitDiffDel` | Deleted-line count. |
| `.Language` | Syntax language ID, or `unknown`; parser errors can change its foreground. |
| `.CursorLine` | One-based cursor line. |
| `.CursorColumn` | One-based cursor column. |
| `.TotalLines` | Number of buffer lines. |
| `.DiagError`, `.DiagWarn`, `.DiagInfo` | Accepted by the parser but currently not populated; do not rely on them as working diagnostic counters. |
| `.Image` | Image placement; see Image directives. |
| `.ShiftRight` | Right-alignment pivot. |

Example:

```yaml
editor:
  status_bar:
    background_attr: {bg: "#202020"}
    foreground_error_attr: {fg: red}
    layout: ' {{ .Status | bold }}  {{ .Filepath }}{{ .ShiftRight }}{{ .Language | fg "silver" }}  {{ .CursorLine }}:{{ .CursorColumn }} '
```

Only the background of `editor.status_bar.background_attr` is used; use field
pipelines for static foregrounds. Only the foreground of
`foreground_error_attr` is used, for language-parser errors. Filepath and Git
fields also have runtime attributes for dirty/read-only/error states.

The editor does not have the agent's `status.<PHASE>.attr` configuration.
Vim and Helix supply their mode colours:

- Vim: INSERT green, DELETE red, YANK/CASE fuchsia, SHIFT orange, COMMENT lime,
  VISUAL blue, V-LINE teal, V-BLOCK aqua, REPLACE yellow, SEARCH silver.
- Helix: SELECT blue, INSERT green, GOTO/JUMP fuchsia, MATCH lime, VIEW orange,
  REPLACE yellow, BRACKET aqua, SEARCH silver.
- Those coloured modes use black foreground. NORMAL uses layout defaults.

When space is tight and Filepath is present, the editor drops the Git branch,
Git statistics, Language and TotalLines in that order before further clipping.
Do not design an ornament that relies on those optional fields always existing.

### Agent fields

| Field | Display |
|---|---|
| `.Spinner` | Current phase's animation frame; the default IDLE state shows a static icon. |
| `.Status` | `IDLE` between turns; `ASKING` while waiting for an answer; otherwise the active task description or phase name. |
| `.Conversation` | Conversation name. |
| `.Elapsed` | Current turn duration, or previous turn duration when idle; hidden when no positive duration is available. |
| `.Model` | Model name. |
| `.Provider` | Provider name. |
| `.Effort` | Reasoning effort, or `default` when unset. |
| `.MaxTokens` | Output-token budget; hidden when nonpositive. |
| `.TokensSent` | Cumulative input-token count. |
| `.TokensReceived` | Cumulative output-token count. |
| `.ContextTokens` | Current context occupancy. |
| `.ContextWindow` | Model context-window capacity. |
| `.ContextPct` | Context occupancy percentage, such as `42%`. |
| `.Cache` | Cache-hit percentage; hidden before any input tokens have been sent. |
| `.CachePct` | Cache-hit percentage, including `0%` before input. |
| `.ContextGauge` | Context gauge labelled occupancy/capacity. |
| `.CacheGauge` | Cache-hit gauge labelled `cache N%`. |
| `.Image` | Image placement; see Image directives. |
| `.ShiftRight` | Right-alignment pivot. |

Token counts are formatted as integers below 1000, otherwise rounded `k`/`m`
values. They do not include a `tokens` suffix. Durations use `Ns`, `Nm Ns` or
`Nh Nm`. Percentages already include `%`; do not add a second one.

Status is padded on the right to the longest phase name in the configured
palette. Longer task descriptions can extend it. Its colour follows the phase
even when its text is a task description.

Example:

```yaml
extensions:
  rune-agent:
    config:
      status_bar:
        enabled: true
        background_attr: {fg: silver, bg: "#202020"}
        layout: '{{ .Spinner }} {{ .Status | bold }}  {{ .Conversation }}{{ .ShiftRight }}{{ .Elapsed }}  {{ .Model }}  {{ .ContextPct }}'
```

The agent uses both `fg` and `bg` from `background_attr`; the background fills
the row. It does not automatically fill the whole row with the phase colour.

#### Agent phase colours

`status.<PHASE>.attr` colours Status and Spinner, including their ordinary
literal padding. `status.<PHASE>.animation.ch` is a sequence of spinner frames;
one rune is a still icon. `animation.attr` overrides Spinner colours only.

| Phase | Shipped foreground | Shipped background |
|---|---|---|
| `IDLE` | white | silver |
| `SENDING` | black | teal |
| `REASONING` | white | navy |
| `RECEIVING` | black | fuchsia |
| `EXECUTING` | default | red |
| `COMPACTING` | default | gray |
| `ASKING` | black | yellow |
| `ERROR` | default | maroon |

All shipped phase attributes include bold. The IDLE spinner has yellow
foreground and the ERROR spinner red foreground.

Override a phase without changing the others:

```yaml
extensions:
  rune-agent:
    config:
      status_bar:
        status:
          REASONING:
            attr: {fg: white, bg: "#244860", bold: true}
            animation:
              ch: "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
              attr: {fg: yellow}
```

Unmentioned phases keep their defaults. Within one phase, specifying `attr`
replaces that attribute value, and specifying `animation` replaces that
animation configuration; specify all values you need within each. Omitting one
of the two retains its default. Animation colour overrides inherit unspecified
colours from the phase; animation style flags combine with the phase's flags.

Use the phase palette to change dynamic colours. `.Status | bg "red"` is a
static default, not an override of every runtime phase. Other fields and image
reservations do not inherit the phase palette.

Background glyphs such as `█▓▒░` attached to Status use the status background
as their foreground over the bar background. This creates coloured fade edges.
Ordinary spaces instead extend the solid status background.

#### Agent gauges

These keys belong under `extensions.rune-agent.config.status_bar`:

| Key | Meaning |
|---|---|
| `gauge_width` | Width of each gauge including its caps; default 18. |
| `gauge_empty_attr` | Attributes for the empty track and its label. |
| `gauge_cap_attr` | Attributes for the end caps. |
| `gauge_start_rune`, `gauge_end_rune` | First rune of each string is the cap. Empty strings omit caps, as the defaults do. |
| `context_gauge_fill_attrs` | Ordered list of attribute stops for the context gauge. Defaults to black text on green → yellow → red. |
| `cache_gauge_fill_attrs` | Ordered list of attribute stops for the cache gauge. Defaults to black text on red → yellow → green. |

Example:

```yaml
extensions:
  rune-agent:
    config:
      status_bar:
        gauge_width: 14
        gauge_empty_attr: {fg: silver, bg: "#202020"}
        gauge_cap_attr: {fg: silver}
        gauge_start_rune: "["
        gauge_end_rune: "]"
        context_gauge_fill_attrs:
          - {fg: black, bg: green}
          - {fg: black, bg: yellow}
          - {fg: white, bg: red}
        cache_gauge_fill_attrs:
          - {fg: white, bg: red}
          - {fg: black, bg: green}
        layout: '{{ .Spinner }} {{ .Status }}{{ .ShiftRight }}{{ .ContextGauge }}  {{ .CacheGauge }}'
```

Gauge pipelines style the surrounding literals, not the internally coloured
track. Use the gauge keys for the track. Preserve existing `shader`,
`shader_fps` and `shader_loop` settings when changing a layout; those configure
animation rather than layout elements.

#### Narrow agent bars

Elements are dropped in this order as width runs out: MaxTokens, Conversation,
CacheGauge, Cache/CachePct, Provider, Effort, TokensSent/TokensReceived, numeric
context fields, Model, ContextGauge, Elapsed. Status is then clipped. Spinner
and image reservations are not in that drop list. Keep reservations small and
do not assume a nearby optional field will always hold an ornament in place.

### Image directives

Only `src` is required. All image directives apply only to `.Image`.

| Directive | Argument and default | Limits / meaning |
|---|---|---|
| `src` | Quoted absolute `file:///`, `https://` or `http://` URI | No `~` or environment-variable expansion. |
| `width` | Integer columns, default `2`, or quoted `"N%"` | 1–4096 cells or 1–100% of bar width. |
| `height` | Integer rows, default `1` | 1–4096. |
| `reserve` | Integer columns | Layout space consumed; defaults to numeric width, or **0 for percentage width**. Range 0–4096. |
| `x_offset` | Integer columns or quoted signed `"N%"`, default `0` | Positive moves right; negative left. ±4096 cells or ±100% of bar width. |
| `y_offset` | Integer rows, default `0` | Positive moves down; negative up. ±4096. |
| `x_pixel_offset`, `y_pixel_offset` | Integer device pixels, default `0` | Additional displacement, ±65536. Usually start with cell offsets and adjust visually. |
| `fit` | `"contain"` (default) or `"fill"` | Preserve source proportions or stretch to the image rectangle. No `cover`. |
| `z_index` | Signed 32-bit integer, default `0` | Layering described below. |
| `overflow` | No arguments; absent by default | Allow the image past the editor/chat bounds, up to the screen edge. |
| `max_cut` | Integer columns, default `1` | Hide the image when its layout element extends more than this far past the bar's end. Range 0–4096. Not an image crop percentage. |
| `alt` | Quoted string, default cross glyph `\uf00d` | Use `""` to avoid a failure marker. Newlines are permitted; other control characters are not. |
| `ttl` | Quoted duration, default `"24h"` | Nonnegative byte-cache lifetime, such as `"1h"`. Not a live-reload interval. |

#### Anchor, reservation and clipping

The anchor is the first reserved cell, after any literal prefix of the Image
element. With `reserve 0`, it is where the next element starts. The image is
centred on the anchor, then translated by its offsets. Even widths/heights
place the centre half a cell left/up of the anchor cell's centre. Percentage
widths and offsets truncate toward zero.

The reserved space and the drawn rectangle are independent:

- Increase `reserve` to push following text away; increase `width` to enlarge
  the art without necessarily moving the text.
- Use `reserve 0` for art layered behind an existing segment.
- An image taller than one row can overlap editor/chat content above the bar
  without `overflow`. Add `overflow` only to cross the component's outer bounds.
- Floating windows still cover overflowing images.
- Alt text remains clipped to the component, draws over text regardless of
  `z_index`, and follows cell offsets but not pixel offsets.

For a 6-column image starting at the next element's position, use `reserve 0`
and `x_offset 3`. Verify the result in a screenshot instead of asking for the
cell's pixel dimensions.

#### Image layering and transparency

| `z_index` | Placement |
|---|---|
| `0` or greater | Above text. |
| `-1073741824` through `-1` | Below text, above cell backgrounds. |
| Below `-1073741824` | Below backgrounds too; only cells without an explicit background reveal it. |

Higher values draw above lower values; later images win ties. Alpha holes show
the underlying layer. A transparent hole is not automatically filled with the
current status colour: the cells underneath it must belong to Status or Spinner.

### Example: status colour visible through a transparent ornament

Place an image with a transparent opening over literal padding belonging to
the existing Status field. Do not change the artwork's opaque pixels, invent
a coloured spacer field or duplicate Status.

Agent example:

```yaml
extensions:
  rune-agent:
    config:
      status_bar:
        layout: '{{ .Image | src "file:///absolute/path/ornament.png?v=2" | width 6 | height 3 | reserve 0 | x_offset 3 | z_index -1 | alt "" }}      {{ .Status | bold }}{{ .ShiftRight }}{{ .Model }}'
```

Editor example:

```yaml
editor:
  status_bar:
    layout: '{{ .Image | src "file:///absolute/path/ornament.png?v=2" | width 6 | height 3 | reserve 0 | x_offset 3 | z_index -1 | alt "" }}      {{ .Status | bold }}{{ .ShiftRight }}{{ .Filepath }}'
```

Replace the placeholder URI with the real asset path. There are **six spaces**
between the Image action and the Status action. On the left side of the pivot,
all six become Status's prefix. The image reserves no cells, so it overlaps
this coloured prefix rather than adding a separate blank segment. The status
label begins after the padding. `z_index -1` leaves text readable while showing
the ornament above the background.

The phase/mode colour fills only the status-bar row. A hole above or below that
row reveals editor/chat content, not the status colour. Align the opening with
that row or change the artwork; there is no dynamic multirow fill-rectangle
directive. For an existing design, adapt this pattern around its single Status
field and preserve its other fields rather than replacing the entire layout.

### Screenshot checks

- Verify the edit is active before interpreting a screenshot: a config that
  does not parse installs no new layout, and a notification says why.
- Inspect status text, optional fields and the transparent opening separately.
  Check whether padding belongs to the intended field before changing colours.
- Test available idle and active states: a task description can be wider than
  a phase label. Do not claim to have checked states that were not visible.
- Check narrow widths when possible. Large image reservations can displace
  text, and `max_cut` can hide an image when its element is clipped.
- A missing image may be a bad URI or clipped placement. Inspect
  notifications and the configured `log_path`.
- When the result differs from the brief, adjust the layout or asset and
  inspect another screenshot. No manual cell measurement is required.

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
Run `runectl screenshot` yourself and inspect the result after each revision.
For the keys under Checking your work, this also applies the change; do not
ask the user to reload them. Do not restart Rune or reload the agent's own
workspace automatically: that can interrupt the conversation and running work.

| Change | Takes effect |
|---|---|
| A key listed under Checking your work, or an image it names, even overwritten at the same path | `runectl screenshot`, in every open workspace |
| Any other `browser.*`, `command.*` or `extensions.*` key | `:workspacereload` in the focused workspace |
| Any other `gui.*` or `program_status.*` key, `animations.*` | Restart Rune. `:guiwindownew` opens a fresh process to preview them |
| A sound file overwritten at the same path | The next time it plays |
| The wallpaper file overwritten at the same path | `runectl screenshot` or `:workspacereload` |
| Any other image overwritten at the same URI | Never: give it a new URI |

If a requested change needs a workspace reload or restart, prepare it and state
that specific activation requirement once. After activation, capture and inspect
the result yourself. Do not ask for a screenshot unless capture is genuinely
unavailable. Do not claim unactivated changes have been verified.

**Errors:**
- Config errors appear as notifications after the reload, so the screenshot
  shows them. If the configuration file does not parse, no new styles are
  installed. Correct the edit and capture again.
- An invalid URI rejects its whole `graphics` table. Correct the URI instead
  of assuming the previous images will remain.
- An unreadable wallpaper is a config error.
- Other missing or unreadable images use their slot fallback; separator
  junctions try the vertical side image before the rune, as described above.
  Load failures log a warning to the file named by `log_path`.

## Loop
1. Discover the active config and read it, the manifest and the existing assets.
   Back up the config and preserve a rollback copy of any assets being replaced.
2. Run `runectl screenshot` and inspect the baseline image.
3. Use the user's brief and current theme to choose the palette, material and
   light direction. Keep changes within the requested scope.
4. Work one surface at a time without asking for approval at every step. Use
   the surface table for configuration units and the screenshot for visual fit.
5. Generate or edit the assets, or adjust the layout using the status-bar
   reference. Inspect the asset files and test repeating tiles for seams.
6. Patch only the relevant configuration keys. Use absolute image URIs and
   follow the cache rules for the surface being edited.
7. Run `runectl screenshot` again and open the returned PNG. Check readability,
   clipping, rail continuity, background matching, transparent areas and whether
   ornaments obscure text. 
8. Fix any config errors shown in the capture or in the logs at $RUNE_DATADIR/debug.log.
9. Adjust the assets, spacing, dimensions, offsets or colours and repeat steps
   6–8 until the rendered result meets the brief. A successful file write or
   screenshot command alone is not verification: inspect the image.
10. Update the manifest and report the final screenshot path, changed keys and
   rollback location. Note any state you could not inspect or change that still
   needs activation. Do not end with routine measurement or screenshot chores
   for the user.
