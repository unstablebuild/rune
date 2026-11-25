---
name: rune-hud
description: >
  Update Rune's configuration and generate sprites to refine its UI:
  window frames, floating-only frames and focused headers, window backgrounds
  and focus styles (including marking the focused window by its background
  instead of a frame, for a smoother or more modern look), scroll bars,
  command prompts, editor
  and agent status-bar layouts, the quick menu, prompt buttons, wallpaper, the
  app icon and sounds. Discover the configuration, edit it, run runectl
  screenshot and inspect the result yourself. This is configuration and asset
  editing, not Rune source development. Use when the user wants to create,
  restyle or iterate on Rune's appearance or sounds.
---

# Rune HUD images and sounds

Own the edit-and-verify loop. Read the user's configuration and existing assets,
make the requested changes, run `runectl screenshot`, inspect the PNG and fix
what looks wrong. Do not ask the user to measure cells, run diagnostic commands
or provide screenshots you can capture yourself.

## Scope: configuration and assets only
- Edit the theme's config file, the include line that loads it, theme assets
  and the manifest. Generate or edit the requested sprites and wallpaper;
  generate sounds only when requested. Keep unrelated configuration and
  artwork unchanged.
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
- Floating-only shadows and distinct focused headers are supported config
  surfaces. Use the optional charsets below, not core changes or shared tiled
  frame edits, when the request concerns only floating windows.
- Marking the focused window by its background instead of its frame is a
  supported config surface: blank frame charsets for padding plus
  `focus_background_attr`, or the theme's `focus_background`. Follow
  "Frameless focus" below, also when the user only asks for a smoother,
  cleaner or more modern design.

## Checking your work
Take a baseline screenshot before editing. Run `runectl screenshot` yourself,
then open the PNG at the printed path with your image-reading tool. Use the
rendered result to judge proportions, spacing, legibility and seams. Repeat
after each revision; run captures sequentially.

Before each capture, Rune reads the active configuration file again and applies
these keys to every open workspace:

- `browser.window_manager`: `frame_attr`, `focus_frame_attr`, `frame_charset`,
  `focus_frame_charset`, `floating_frame_charset`, `focus_floating_frame_charset`,
  `focus_window_bar_charset`, `scroll_bar_attr`, `scroll_bar_charset`,
  `focus_scroll_bar_charset`, `scroll_bar_char`, `scroll_bar_hover_char`,
  `background_attr`, `focus_background_attr`, `background_graphics`,
  `focus_background_graphics`, `window_bar`, `window_bar_charset`,
  `close_icon` and `close_icon_attr`;
- `browser.frameunion_charset`;
- `browser`: `focus_tab_attr`, `non_focus_tab_attr`, `focus_tab_icon_attr`,
  `non_focus_tab_icon_attr`, `focus_tab_highlight_attr`, `hover_tab_attr`,
  `focus_tab_highlight_char`, `tab_images`, `focus_tab_highlight_graphics` and
  `tab_sounds`;
- `browser.prompt`: `text_attr`, `highlight_attr`, `hover_attr`,
  `clicked_attr`, `background_attr` and `images`;
- `command.frame_charset`, `command.separator_charset` and
  `command.show_manual`;
- `editor.status_bar`: `layout`, `background_attr`, `foreground_error_attr`
  and `focus`;
- `extensions.rune-agent.config.status_bar`, every key,
  `extensions.rune-agent.config.input_box_frame_charset`, the compose box
  colours and `extensions.rune-agent.config.focus`;
- `gui`: `quick_menu`, `quick_menu_sounds`, `themes`, `default_theme`,
  `sound_volume` and `app_icon`;
- `notifications`: `attr`, `background_attr`, `frame_charset`, `padding`,
  `progress_bar` and `progress_format`;
- `program_status`, every key;
- `terminal.bell`;
- `workspace`: `wallpaper_image`, `wallpaper_image_ascii`, `wallpaper_ascii`,
  `wallpaper`, `wallpaper_attr`, `wallpaper_background_attr`,
  `wallpaper_density_characters`, the `focus_` version of each of those
  wallpaper keys, `focus_tab_highlight_char`, `tab_images`,
  `focus_tab_highlight_graphics` and `tab_sounds`;
- `sounds`.

The capture cannot show `gui.app_icon`: the desktop draws the icon outside
Rune's output. Inspect the icon file itself, downscaled to 16, 32 and 256 px
(`magick icon.png -resize 16x16 icon-16.png`), then tell the user where to
look: the Dock and the Command-Tab switcher on macOS, the taskbar or window
title on Linux.

Rune also reads again every image these keys name, including a file you
overwrote at the same path. Existing prompts retain their style until reopened;
open notifications and an open program status inbox take the new style at once.
`gui.default_theme` and `gui.sound_volume` apply only when they differ from the
values Rune read last, so a theme or volume the user picked since stays. The
agent redraws its own status bar and compose box a moment after Rune reloads;
if a capture still shows the old ones, capture again.
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
  - Write the theme's keys to a file the theme owns,
    `$RUNE_DATADIR/hud/<theme>/config.yaml`, and make sure the user's config
    includes it: a YAML config lists it under the top-level `include:` key, a
    Starlark config calls `include("$RUNE_DATADIR/hud/<theme>/config.yaml")`.
    Add the line once, as text, and leave the rest of the user's file alone;
    the user's own settings override the theme's, so a key the user set stays
    in effect until they remove it.
  - Patch only the keys you change, as text. Never round-trip the user's file
    through a YAML library: it drops their comments.
  - Rune reads the config again when an included file changes, so a save is
    enough to apply the theme's live keys; `runectl screenshot` applies them
    before each capture as well.

Layout of a theme:
```
hud/<theme>/
  manifest.yaml
  wallpaper.png
  sources/<name>.png, <name>.prompt.txt
  sounds/<cue>.wav
  v<N>/icon.png
  v<N>/frame/{normal,focused,prompt}/<slot>.png
  v<N>/panel/{normal,focused}/<slot>.png      (the 8 frame slots and center)
  v<N>/scroll/{top,middle,bottom,hover-top,hover-middle,hover-bottom}.png
  v<N>/scroll/focused-{top,middle,bottom,hover-top,hover-middle,hover-bottom}.png
  v<N>/window-bar/{left,middle,right}.png
  v<N>/separator/{left,horizontal,right}.png
  v<N>/status/{normal,focused}/<name>.png
  v<N>/inbox/{normal,focused}/<name>.png
  v<N>/quickmenu/{idle,hover,pressed}/<button>.png
  v<N>/button/{idle,selected,hover,pressed}/{left,middle,right}.png
```
Use lowercase kebab-case file names, with no spaces, `?`, `#` or `%`: they
would need percent-encoding in a `file://` URI.

## Rules that apply to every image
1. **Use absolute URIs**, except for the wallpaper and the app icon. Image
   values are `file:///absolute/path.png` or `https://...`, with no `~` and no
   `$VARS`, except `$RUNE_DATADIR` in a `file://` URI, as in
   `file://$RUNE_DATADIR/hud/<theme>/v1/frame/normal/topleft.png`.
   `workspace.wallpaper_image` and `gui.app_icon` are the exceptions: they
   take a plain path, which may use `~` and environment variables.
2. **Overwrite an image in place only if a key under Checking your work names
   it.** Rune caches images by URI for 24 hours by default, but
   `runectl screenshot` reads those images again, so you can overwrite them
   while you iterate. Save each version you ship in a new `v<N>` directory
   and keep the previous one for rollback.
   - The wallpaper and the app icon are not cached: overwrite them in place.
3. **Formats:** PNG with alpha is preferred. JPEG, GIF and WebP also load, except
   for the wallpaper, which must be a PNG. Each side is at most 8192 px.
4. **Images are drawn as they are.** Attrs, dimming, b&w and the theme do not
   tint them, so bake the colours in.
   - A theme is two matched states, normal and focused. Each has one
     background, a colour and optionally a panel image, and every sprite set
     of that state is painted against it: frames, scroll bar, status bars,
     inbox and compose box. Ship a normal and a focused set of each sprite
     whose surroundings differ between the states. When a theme gains a
     focus background, run the audit in "Adding a focus background to a
     theme" first.
   - Frame sprites are lines on transparent pixels. Window frames, floating
     frames, the command prompt frame, the compose box frame and
     notification frames all work the same way: Rune paints the transparent
     pixels on the content's side of the line in the content's colour, and
     the ones on the other side show what is under the frame. Never paint
     either side in: a baked side is right in one state only. See "One
     colour on each side of a line".
   - Every other image's transparent pixels show the background of the cell
     it's drawn in, and a cell has exactly one background. "Which colour a
     transparent pixel shows" lists it for each surface.
   - On a floating window's bar, transparent pixels show whatever is under
     the window, including its text: make bar sprites opaque inside the bar
     and transparent only outside it, such as beyond a rounded end.
     The close icon and title over bar sprites keep that background too, so
     `close_icon_attr`'s background only applies to a bar drawn with
     characters; pick the icon and title colours to read over the sprites.
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
A HUD reads as one object only when every surface follows one set of cohesive guiding
principles.

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
   The focused window's background is the canvas of the focused state.
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
   - App icon: one bold emblem on the theme's background, readable at 16 px,
     with no text.
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
9. **Balanced padding.** Centre a tab, inbox entry or button plate on its
   label, with the same visible padding above, below, left and right. A dark
   base or shadow counts against the bottom padding. Judge it in a zoomed crop
   of the live screenshot, not in the source art. If a plate gets clipped, fix
   the clipping without shrinking the padding.
10. **One family of controls.** Prompt buttons, quick menu buttons, file tabs,
    workspace tabs and inbox entries are one set: give them the same rim
    thickness, base depth, corner radius, padding and gaps. Take the control
    the user approved as the reference, record its measurements in the
    manifest, and compare the others with it side by side in a crop.
11. **One selection cue.** When the focused or selected image already sets a
    tab or entry apart, don't add a highlight or marker on top of it. For file
    and workspace tabs, set `focus_tab_highlight_char: ""` and remove
    `focus_tab_highlight_graphics` (its images draw even when the character is
    empty). For the inbox, set `program_status.marker: " "` and remove
    `marker_graphics`. Add a cue only when the user asks for one.

## Colour continuity
Match background fills to the active theme. Transparent image pixels reveal
whatever is underneath: usually the theme background, but also status colours,
text or other images. Use that intentionally for status-bar ornaments.
- Read `gui.default_theme`, then its `background`: from `gui.themes.<name>` in
  the user's config when it's defined there, otherwise from the built-in list
  below. Read its `focus_background` too, the focused canvas while
  `focus_background_attr` and `focus_background_graphics` are unset. Record
  both in the manifest.
- Built-in backgrounds: furman `#202020`, romero `#000000`, carmack `#080808`,
  reynolds `#121212`, brevik `#080808`, thompson `#0f1114`, pike `#ffffea`,
  hopper `#000000`, kernighan `#0c0c0c`, wozniak `#120F1D`, auge `#270d06`,
  ritchie `#f8f8f8`, kelleher `#000000`, sanfilippo `#000000`, frost
  `#000000`, mullen `#0d0d1b`.
- Built-in focus backgrounds: pike `#fffff3`, ritchie `#ffffff`. The other
  built-in themes set none, so their focused window keeps the theme
  background unless the config sets one.
- The wallpaper's own background must be that colour, not a nearby one, and
  its edges must fade to it exactly. With `contain` the image's edges sit
  inside the window, so any step shows as a rectangle.
- Leave `workspace.wallpaper_background_attr` unset, or give it the same `bg`.
- Opaque fills in frames, bars and buttons follow the same rule.
- The focused state has its own canvas: the `bg` of
  `browser.window_manager.focus_background_attr`, or else the theme's
  `focus_background`, and the panel of `focus_background_graphics` drawn over
  it. Opaque fills in focused sprites,
  the focused wallpaper and the `focus` blocks' backgrounds must match it
  exactly, and the panel's own fill must be that `bg`, so a slice that hasn't
  loaded leaves no step.
- A pixel painted in a canvas colour is right only where that canvas is
  behind it in every state it's drawn in. A sprite shared by both states
  can't bake either canvas into a pixel the pane shows around.
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
- Wallpaper: sample a corner and confirm the background matches.

## Surfaces
Abbreviations:
- **L**: the scroll bar outset `left`.
- **T, B, S, I, H**: the window bar outsets `top`, `bottom`, `sides`, `inward`
  and `horizontal_top`. Prompt buttons use T, B and S too, from their own outset.
  Command separator junctions use only T, B and I from their own outset. File
  tabs use only S, from theirs. Program status inbox entries use T, B and S,
  from theirs.
- **R**: the rows of the tab bar, 2 in the GUI.
- **N**: the rows of a program status inbox entry, 2 to 4: the workspace name
  and the progress bar each add a row only when the entry has one.
- **Tiles →** / **tiles ↓**: one copy is drawn per cell, so the image must tile
  seamlessly in that direction.

| Surface | Key | Images | Cells (w × h) | Fit |
|---|---|---|---|---|
| Window frame | `browser.window_manager.frame_charset.graphics` | `topleft` `topright` `bottomleft` `bottomright` | 1 × 1 | fill |
| | | `horizontaltop` `horizontalbottom` (tiles →), `verticalleft` `verticalright` (tiles ↓) | 1 × 1 | fill |
| Focused frame | `browser.window_manager.focus_frame_charset.graphics` | same 8 | 1 × 1 | fill |
| Window background | `browser.window_manager.background_graphics` | `topleft` `topright` `bottomleft` `bottomright` | 1 × 1 | `fit`: fill (default) or tile |
| | | `horizontaltop` `horizontalbottom` | (w−2) × 1 | `fit` |
| | | `verticalleft` `verticalright` | 1 × (h−2) | `fit` |
| | | `center` | (w−2) × (h−2) | `fit` |
| Focused window background | `browser.window_manager.focus_background_graphics` | same 9 | same as above | `fit` |
| Floating-only frame | `browser.window_manager.floating_frame_charset.graphics` | same 8 | 1 × 1 | fill |
| Focused floating-only frame | `browser.window_manager.focus_floating_frame_charset.graphics` | same 8 | 1 × 1 | fill |
| Command prompt frame | `command.frame_charset.graphics` | same 8 | 1 × 1 | fill |
| Command separator | `command.separator_charset.graphics` | `left`, `right` | (1+I) × (T+1+B) | fill |
| | | `horizontal` (tiles →) | 1 × 1 | fill |
| Notification frame | `notifications.frame_charset.graphics` | same 8 | 1 × 1 | fill |
| Notification progress bar | `notifications.progress_format.graphics` | `start`, `current_tip`, `end` | 1 × 1 | fill |
| | | `current` `remain` (tiles →) | 1 × 1 | fill |
| Agent compose box frame | `extensions.rune-agent.config.input_box_frame_charset.graphics` | same 8 | 1 × 1 | fill |
| Focused agent compose box frame | `extensions.rune-agent.config.focus.input_box_frame_charset.graphics` | same 8 | 1 × 1 | fill |
| Scroll bar | `browser.window_manager.scroll_bar_charset.graphics` | `top_char`, `middle_char` (tiles ↓), `bottom_char`, and the `hover_` version of each | (1+L) × 1 | fill |
| Focused scroll bar | `browser.window_manager.focus_scroll_bar_charset.graphics` | same as the scroll bar | (1+L) × 1 | fill |
| Floating window bar | `browser.window_manager.window_bar_charset.graphics` | `left` | (S+1+I) × (T+1+B) | fill |
| | | `right` | (I+1+S) × (T+1+B) | fill |
| | | `horizontal` (tiles →) | 1 × (H+1) | fill |
| Focused floating window bar | `browser.window_manager.focus_window_bar_charset.graphics` | same 3 and outsets as the normal window bar | same as above | fill |
| Prompt buttons | `browser.prompt.images.<idle\|selected\|hover\|pressed>` | `left` | (S+1) × (T+1+B) | fill |
| | | `middle` (tiles →) | 1 × (T+1+B) | fill |
| | | `right` | (1+S) × (T+1+B) | fill |
| File tab | `browser.tab_images.<focus\|focus_window\|non_focus\|hover\|pressed>` | `left` | (S+1) × R | fill |
| | | `middle` (tiles →) | 1 × R | fill |
| | | `right` | (1+S) × R | fill |
| File tab highlight | `browser.focus_tab_highlight_graphics` | `left`, `right` | 1 × 1 | fill |
| | | `horizontal` (tiles →) | 1 × 1 | fill |
| Workspace tab | `workspace.tab_images.<focus\|focus_window\|non_focus\|hover\|pressed>` | `left` | (S+1) × R | fill |
| | | `middle` (tiles →) | 1 × R | fill |
| | | `right` | (1+S) × R | fill |
| Workspace tab highlight | `workspace.focus_tab_highlight_graphics` | `left`, `right` | 1 × 1 | fill |
| | | `horizontal` (tiles →) | 1 × 1 | fill |
| Inbox entry | `program_status.images.<idle\|selected\|hover>` | `left` | (S+1) × (T+N+B) | fill |
| | | `middle` (tiles →) | 1 × (T+N+B) | fill |
| | | `right` | (1+S) × (T+N+B) | fill |
| Inbox marker | `program_status.marker_graphics` | `top`, `bottom` | 1 × 1 | fill |
| | | `vertical` (tiles ↓) | 1 × 1 | fill |
| Inbox progress bar | `program_status.progress_bar_graphics` | `left`, `right` | 1 × 1 | fill |
| | | `filled`, `empty` (tiles →) | 1 × 1 | fill |
| Focused inbox | `program_status.focus.<images\|marker_graphics\|progress_bar_graphics>` | same as the inbox rows above | same | fill |
| Editor status bar | `editor.status_bar.layout` | `.Image` elements | `width` × `height` | contain, or `fit "fill"` |
| Focused editor status bar | `editor.status_bar.focus.layout` | `.Image` elements | `width` × `height` | contain, or `fit "fill"` |
| Agent status bar | `extensions.rune-agent.config.status_bar.layout` | `.Image` elements | `width` × `height` | contain, or `fit "fill"` |
| Focused agent status bar | `extensions.rune-agent.config.focus.status_bar.layout` | `.Image` elements | `width` × `height` | contain, or `fit "fill"` |
| Quick menu | `gui.quick_menu[i].sprite` | `idle` `hover` `pressed` | square, each side as wide as 4 text columns | contain |
| Wallpaper | `workspace.wallpaper_image` | a path, or `{path, width, height, fit}` | `width`/`height` in cells or `N%`, default 60% | `contain` (default), `cover` or `fill` |
| Focused wallpaper | `workspace.focus_wallpaper_image` | same as the wallpaper | same | same |
| App icon | `gui.app_icon` | a path | none: a square image, 256 px or larger | `contain`, centered on a transparent square |

### Floating-only styling

Under `browser.window_manager`, optional `floating_frame_charset` inherits
missing slots from `frame_charset`; `focus_floating_frame_charset` inherits
from `focus_frame_charset`, never from the normal floating override.
`focus_window_bar_charset` inherits runes, graphics, mode and outsets from
`window_bar_charset`. Omitted keys preserve the shared legacy styles. A present
`{}` resolves inherited defaults but stays an explicit override. A graphics URI
of `""` clears that image; no empty-field merging restores it at draw time.

For a per-float shadow with tiled frames untouched, leave both shared frame
charsets alone and put shadow sprites in both floating overrides' right and
bottom graphics slots. Bake the shadow within the frame cells; frame charsets
have no outset. For normal black versus focused lavender headers, put black
`left`, `horizontal`, `right` sprites in `window_bar_charset.graphics` and
lavender versions in `focus_window_bar_charset.graphics`. Colours belong in
the sprites, not invented per-floating attribute keys. For example:

```yaml
browser:
  window_manager:
    floating_frame_charset:
      graphics: &float_shadow
        verticalright: file://$RUNE_DATADIR/hud/custom/v1/shadow-right.png
        horizontalbottom: file://$RUNE_DATADIR/hud/custom/v1/shadow-bottom.png
        bottomright: file://$RUNE_DATADIR/hud/custom/v1/shadow-corner.png
    focus_floating_frame_charset:
      graphics: *float_shadow
    window_bar_charset:
      graphics:
        left: file://$RUNE_DATADIR/hud/custom/v1/black-left.png
        horizontal: file://$RUNE_DATADIR/hud/custom/v1/black-middle.png
        right: file://$RUNE_DATADIR/hud/custom/v1/black-right.png
    focus_window_bar_charset:
      graphics:
        left: file://$RUNE_DATADIR/hud/custom/v1/lavender-left.png
        horizontal: file://$RUNE_DATADIR/hud/custom/v1/lavender-middle.png
        right: file://$RUNE_DATADIR/hud/custom/v1/lavender-right.png
```

Create the assets and merge only the requested keys into the existing config.
`runectl screenshot` and `themereload` refresh every optional image URI, even
when overwritten at the same path. Removing an optional key restores its
shared fallback on existing floats on the next capture; removing just a slot
restores inheritance, while `""` explicitly clears it. Inspect focused and
unfocused floats, check shadows and header joins, and confirm tiled frames
remain unchanged. Do not claim a focus state was verified unless visible.

### Focus styles

The focused window can stand out by its background rather than its frame: a
background colour, optionally a panel image, and focused sprites. It applies
only with more than one window, and only window focus counts: a single window,
or Rune out of focus, shows the normal style.

#### Frameless focus: remove the frame lines, mark focus with the background

Use this technique when the user asks for it outright:

- "Remove the window lines and use the background to show what's in focus."
- "No borders", "borderless windows", "get rid of the frames".
- "Highlight the active pane", "make the focused window stand out" without
  asking for a frame colour.

Also use it when the request is vague about the look: "smoother", "more
modern", "cleaner", "minimal", "less busy", "flatter", or "like VS Code" or
"like Zed". Make it the first change, before new colours or sprites. Frame
lines are the heaviest structure on screen, and box-drawing outlines read as
a classic terminal UI. Users who ask for a modern look generally expect panes
split by spacing and surface tone, with the focused pane a shade apart, as in
current editors. In your reply, say that focus is now shown by the background
and that the frame lines can come back.

Don't use it, unless asked, when the user wants frames, or a framed, retro or
pixel-art theme, or when the theme's frame sprites are its identity. Style
the frames' focus with `focus_frame_attr` and `focus_frame_charset` instead.

To apply it, do all of the following:

1. **Blank the frames; don't turn them off.** Keep
   `browser.window_manager.frame: true` and set `frame_charset` and
   `focus_frame_charset` to a space in all eight slots. The frame stays as one
   cell of padding around each window, which takes the window's background,
   and keeps the scroll bar, which draws in its right edge, and edge dragging.
   `frame: false` loses all three and puts neighbouring windows' text side by
   side. Use it only when the user asks for no padding.
2. **Drop the frame images.** Spaces don't hide `graphics`, and slots left
   out inherit. Remove the `graphics` tables from both charsets, or set their
   slots to `""`. Do the same for `floating_frame_charset` and
   `focus_floating_frame_charset` unless the user wants to keep float shadows.
3. **Keep the padding transparent.** Set the `bg` of `frame_attr` and
   `focus_frame_attr` to `default`, or leave it out. An explicit `bg` paints a
   ring of that colour instead of the window's background.
4. **Set the background attrs.** `focus_background_attr.bg` fills every cell
   of the focused window drawn in the default background: the padding, the
   text area and the empty space. `background_attr.bg` does the same for every
   window, a single window included; left out, every window keeps the theme
   background until one takes the focus style. Both work in the terminal UI
   too.
   - **Check the theme first.** When the active theme has a
     `focus_background` (see Colour continuity), it already colours the
     focused window: leave `focus_background_attr` unset, so the colour
     follows the theme when the user switches with `guitheme`. Set
     `focus_background_attr` only to override it; the theme's is then
     ignored, and `{}` turns it off. When the theme has none and is defined
     in the user's config (`gui.themes.<name>`), add `focus_background` to
     the theme instead of `focus_background_attr`, for the same reason. Use
     `focus_background_attr` for built-in themes without one, and when the
     terminal UI must show the focus too: it ignores the theme's.
   - Derive the focused colour from the theme background (see Colour
     continuity). It must look like the theme background highlighted, not
     a different colour: a small step brighter, on dark and light themes
     alike. Never darker: the focused pane is the lit one. On a dark theme,
     mix it about 6-8% toward white: furman `#202020` becomes `#2e2e2e`. On a
     light theme, raise each channel by up to about 10 toward 255, leaving
     channels already at 255, as the built-in light themes do: ritchie
     `#f8f8f8` becomes `#ffffff`, pike `#ffffea` becomes `#fffff3`. Don't jump
     a tinted background straight to white (pike to `#ffffff`): that drops
     its tint and reads as another colour. A background already at `#ffffff`
     has no room: give every window a slightly darker `background_attr` and
     set `focus_background_attr` to the theme background. A tint of the
     accent is fine only when the user asks for colour.
   - The step must read at a glance in a screenshot, yet stay small: syntax
     and terminal colours are tuned for the theme background and lose
     contrast on a far-off one. If the focused tile doesn't stand out, raise
     the step a little rather than adding an outline back.
5. **Let content follow.** Cells with a background of their own keep it.
   Leave `terminal.default_attr.bg` and `workspace.wallpaper_background_attr.bg`
   at `default`, and leave the agent's `background_attr.bg` at `default` or
   give it a focused value in `extensions.rune-agent.config.focus`. Status bars
   keep their own background unless the user wants them to follow too: then
   set it in `editor.status_bar.focus` and the agent's `focus.status_bar`.
6. **Turn off union frames.** Set `browser.union_frames: false`, or its
   junction glyphs draw lines where the tab bar meets the blank frames.
7. **Verify with two windows.** Split, so two tiles show: the focused one's
   colour fills its padding and text area, and the other stays on the normal
   background. A single-window screenshot can't show the focus style.

For the furman theme (`#202020`):

```yaml
browser:
  union_frames: false
  window_manager:
    frame: true
    frame_attr: {fg: gray, bg: default}
    focus_frame_attr: {fg: silver, bg: default}
    frame_charset: &blank {topleft: " ", topright: " ", bottomleft: " ", bottomright: " ",
                           horizontaltop: " ", horizontalbottom: " ", verticalleft: " ", verticalright: " "}
    focus_frame_charset: *blank
    focus_background_attr: {bg: "#2e2e2e"}    # the focus cue: 6-8% lighter
    # background_attr: {bg: "#1a1a1a"}        # optional: every window, even a single one
editor:
  status_bar:
    focus: {background_attr: {bg: "#3a3a3a"}} # optional: focused bar follows
extensions:
  rune-agent:
    config:
      focus: {background_attr: {bg: "#2e2e2e"}} # only if background_attr.bg is set
```

#### Panels, sprites and inheritance

- `focus_` keys inherit like the floating ones: an absent key keeps the
  normal style, a present key takes the slots it leaves out from the normal
  key, and `""` clears an image slot.
- `background_attr.bg` fills the cells drawn in the default background, and
  `fg` replaces the default foreground, for light panels. Cells with a
  background of their own keep it: selections, the cursor line, status bar
  backgrounds, `terminal.default_attr`, and the agent's `background_attr`.
- The panel goes under every cell background and character. Where a slice
  doesn't draw (the terminal UI, loading, failed) the colour shows instead.
- Leave `focus_frame_attr.bg` unset so the frame takes the focused colour and
  panel. That's all blank frames need. Over frame sprites, Rune paints the
  focused colour inside each line and leaves the theme background outside
  it: see "One colour on each side of a line".
- `editor.status_bar.focus`, `program_status.focus` and
  `extensions.rune-agent.config.focus` are laid over their section key by
  key. Put only the keys that change in them: the focused layout and
  background, the focused inbox images, or the focused compose box frame,
  colours, `background_attr` and `status_bar`.
- The tab bar's joins to the frames cut the panel. Set
  `browser.union_frames: false` for themes with a panel.

A panel on top of the borderless setup, with a focused scroll bar sprite:

```yaml
browser:
  window_manager:
    focus_background_graphics:
      fit: fill
      center: file://$RUNE_DATADIR/hud/custom/v1/panel/focused/center.png
      topleft: file://$RUNE_DATADIR/hud/custom/v1/panel/focused/topleft.png
      # ...the other corners and edges
    focus_scroll_bar_charset:
      graphics: {middle_char: file://$RUNE_DATADIR/hud/custom/v1/scroll/focused-middle.png}
```

#### Adding a focus background to a theme

A theme's sprites and colours were painted for one canvas. A focus background
adds a second, and whatever assumed the first shows on it: dark bands around
the compose box, a halo and square corners outside the frame line, a status
bar that disappears, gray text that fades. Reusing the existing sprites
unchanged causes all of these, so never report the reuse as a saving.
A GUI theme with a `focus_background`, such as pike or ritchie, adds that
second canvas by itself: run this audit when a HUD is used with one, too.

Before the first screenshot, audit the theme and fix every finding:

1. **Record both canvases** in the manifest: the normal one (the theme
   background, or `background_attr.bg`) and the focused one
   (`focus_background_attr.bg`, or else the theme's `focus_background`).
2. **Find pixels painted in the normal canvas colour.** Check every sprite
   drawn inside or on the edge of a window: tiled frames, the compose box
   frame, status bar and inbox images, the scroll bar. Count each PNG's
   pixels by colour and alpha with a short script. Opaque pixels in exactly
   the normal canvas colour are masks painted for the unfocused pane. On the
   focused pane they show as bands, notches or rectangles.
3. **Find frame sprites with a painted side.** Frame, prompt, compose box and
   notification sprites must be lines on transparent pixels: Rune paints
   each side for the state it's drawn in. Opaque fills on either side of the
   line, such as an inside fill or an outside painted in the canvas colour,
   are right in one state only. Also find lines with gaps: Rune can't tell
   the sides of a broken line apart, and its cell takes one colour.
4. **Find explicit backgrounds in the normal canvas colour** on surfaces
   inside windows: `scroll_bar_attr`, the agent's `background_attr`,
   `input_box_attr`, `input_box_frame_attr`, `terminal.default_attr`,
   `workspace.wallpaper_background_attr`, `browser.prompt.background_attr`
   and the `program_status` attrs. Set them to `default` so they follow the
   pane, or give the focused value in the section's `focus` block. Prompts
   such as the close-dirty-tabs prompt are floating windows with no `focus`
   block, so theirs must be `default`. Tabs, the command prompt and
   notifications sit outside windows and keep theirs.
5. **Find backgrounds already in the focused colour.** A status bar, compose
   fill or rail sprite in that colour merges into the focused pane and stands
   out on the others. Pick another focused colour, or give those surfaces a
   focused shade in their `focus` block.
6. **Check secondary text.** Gray accents and placeholders were picked for
   the normal canvas, and `dim` text is drawn translucent, so it takes on the
   background under it. On the focused colour, keep secondary text at 3:1
   contrast or more and body text at 4.5:1 or more. Set brighter focused
   colours, or make the step smaller.

Then fix the outlines: drop them ("Frameless focus"), or keep them as line
sprites ("One colour on each side of a line").

#### Which colour a transparent pixel shows

| Surface | Inside the line | Elsewhere |
| --- | --- | --- |
| Tiled frame | The window's colour in its state: the focused canvas (`focus_background_attr.bg`, else the theme's `focus_background`) or `background_attr.bg`. | The frame's `bg`; at `default`, the theme background. A panel slice drawn on the cell shows on both sides. |
| Floating frame, prompts included | The window's colour in its state: the focused canvas or `background_attr.bg`, else the theme background. Never the content's own background, so content with one of its own shows a strip of the window's colour along the line. | Whatever is under the window. A panel slice drawn on the cell shows on both sides. |
| Window bar | No inside. | Whatever is under the window. |
| Command prompt frame | `command.element_attr.bg`, else the focused canvas, else the theme background. | Whatever is under the prompt. |
| Compose box frame | `input_background_color`, when set. | `input_box_frame_attr.bg`, else the agent's `background_attr.bg`, else the pane. |
| Notification frame | `notifications.background_attr.bg`, else `notifications.attr.bg`. | Whatever is under the notification. |
| Status bar images | No inside. | The bar's `background_attr.bg`; at `default`, the pane. |
| Scroll bar | No inside. | `scroll_bar_attr.bg`; at `default`, the pane. |

#### One colour on each side of a line

A frame sprite draws its line through the middle of a cell, which has one
background. Rune gives each side its own colour (table above): it finds the
side of the line that faces the content, from the transparent pixels the line
encloses, and paints them in the content's colour under the sprite. The
other side keeps the cell's background, which shows what is under the frame.
One set of sprites then works in both states, over any pane and under any
floating window. For that, every frame sprite (window frames, floating
frames, the command prompt, its separator junctions, the compose box and
notifications):

- Draws its line with transparent pixels on both sides. Paint neither side:
  no inside fill, no exterior in a canvas colour.
- Runs the line unbroken across its cell: an edge from one end to the
  other, a corner from one side to the adjacent one. Through a gap, both
  sides connect and the cell takes one colour on both. Pixels under about
  50% alpha count as transparent, so antialias freely.
- Keeps ornaments on the line or outside it. Transparent pixels a closed
  ornament encloses show the outside colour. A corner sprite also needs its
  innermost pixel, in the corner that faces the content, transparent.

The inside colour applies to images at one cell only: outsets and
multi-cell junctions keep the cell's background on both sides. A panel slice
that draws on a frame cell replaces the inside colour there.

Status bar rails aren't frames, and Rune paints no inside for them:

- **Status bar rails.** A rail's rounded ends sit on the bar's
  `background_attr.bg`. To use one rail in both states, set that `bg` to
  `default` and drop `bg` from the `status` attrs: the ends then show the
  pane, and the opaque rail colours the bar. The terminal UI then draws the
  bar without a fill. Otherwise, ship a focused rail with its ends painted in
  the focused colour, in `focus.status_bar.layout`.

### Requirements
- Frame and scroll bar images are drawn only while
  `browser.window_manager.frame` is true.
- Window backgrounds and their panels are drawn with frames on or off.
- Command separator images require `browser.window_manager.frame` and a
  visible manual/list separator: enable `command.show_manual` and open a
  command with manual content and enough prompt space to display it.
- Window bar images are drawn only while `browser.window_manager.window_bar`
  is true.
- While `notifications.progress_bar` is true the progress bar is the
  notification's bottom row: the frame's `bottomleft`, `horizontalbottom` and
  `bottomright` images are not drawn, and `progress_format.graphics` draws there
  instead, `start` and `end` at the corners, `current` up to `current_tip`, then
  `remain`. Images are not tinted by notification level; use `overlay` and leave
  a clear (alpha under 0x20) channel closed in by opaque or translucent pixels,
  such as the face above and a shadow below, and Rune fills it with the level
  colour. Clear pixels that reach the outer edge (the bottom, or the outer side
  of `start` and `end`) show what is under the notification, so a channel open
  to the edge stays uncoloured. Keep the exterior transparent and the shadow
  translucent rather than painting the canvas colour into them.
- Quick menu sprites move the bar from the native macOS bar to one Rune draws,
  on every platform. Set `sprite` on every button or on none; `symbol` isn't
  needed with one. Keep each button's `title` (its tooltip) and `command`.
- Prompt button images draw under the buttons of the prompts Rune opens through
  its standard prompt, such as the close-dirty-tabs prompt, and under the
  search box's buttons. Prompts built elsewhere keep their colours. Confirm on
  screen which prompts show them.
- File tab `focus` images are for every tab shown in a window, the tabs in
  `focus_tab_attr`, and fall back to `non_focus`. `focus_window` images are for
  the focused window's tab only and fall back to `focus`. Tabs have no hover
  state.
- The workspace tab bar shows only while more than one workspace is open. Its
  `focus_window` images, falling back to `focus`, are for the workspace in
  focus. Its colours come from `browser.focus_tab_attr`,
  `browser.non_focus_tab_attr` and
  `browser.focus_tab_highlight_attr`. Every workspace shares it, so only the
  user's config styles it, never a workspace's own.
- The program status inbox is `:programstatus`. Every workspace shares it, so
  only the user's config styles it. Inspect its images only when a capture
  shows it open with programs listed.
- Inbox sidebar: `program_status.sidebar_attr`, and
  `program_status.focus.sidebar_attr` while the inbox has the focus, colours
  the program list; the preview beside it keeps the window's background. The
  default is a shade of the theme's background, so set it only to restyle.
  Keep it a small step from the window background, as for the focused colour
  (see Colour continuity), and never gray, which the selected entry uses.
  Entry, marker and progress bar sprites sit on it: keep their corners
  transparent rather than painted in the canvas colour.
- Inbox margin: `program_status.sidebar_margin` (`top`, `left`, `bottom`,
  `right`, in cells) keeps the window background around the sidebar fill;
  `right` is the gap before the preview. The GUI draws the whole sidebar,
  entry sprites included, half a cell lower: at 0 the fill sits half a cell
  below the window bar and covers the top half of the frame's bottom row, and
  the frame's left column leaves about as much on the left. Cells are about
  twice as tall as wide, so add two columns for each row to keep the spacing
  even. `focus` can't change it. Entry sprites start inside it, so it shifts
  their x and y.
- Inbox entry images stand in for the backgrounds of
  `program_status.selected_attr` and `program_status.hover_attr`: an entry
  keeps their other colours, and gets the background back while its images
  don't draw.
- The app icon is the window icon on Linux, but GNOME and KDE Plasma show the
  icon of Rune's launcher instead; the user can set the `Icon=` line of
  `~/.local/share/applications/rune.desktop` (copied from
  `/usr/share/applications/` for the Debian package) to the image's absolute
  path. On macOS it replaces the Dock and Command-Tab icon while Rune runs;
  Finder and the Dock tile of a Rune that is not running keep the bundled icon.

### Where the anchor cell sits in each image
Line up rails and seams across slices at these positions.

**Window frames:**
- Edges are one cell thick.
- Keep the line at the cell's outer edge, where the default `▔ ▁ ▏ ▕`
  characters sit, so content keeps its spacing.
- Corners must meet both edges at the cell boundary.

**Window background panel:**
- The panel covers the whole window, frame cells included. Corners are one
  cell, edges one cell thick, and `center` covers the interior; a window
  narrower or shorter than 2 cells shows only `center`.
- Corners share the frame corner's shape: make them transparent where the
  frame corner is transparent, such as beyond a rounded curve, so the panel
  doesn't square the corner off.
- With `fit: tile` the edges and `center` repeat once per cell and must tile
  seamlessly; with `fit: fill` they stretch, so keep them flat along the
  edge.
- The panel is drawn as it is: keep its fill exactly the focused `bg`.

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
- Because they draw last, the `left` image's inward cells cover the close
  icon. Keep I at 0, or leave those cells transparent. Colour the icon from
  the palette with `close_icon_attr`, and check it in a capture with a
  floating window open.

**Agent compose box frame:**
- Centre each rail in its cell: halfway across the cell's width at the sides,
  halfway down its height at the top and bottom. Turn the corners on those
  same centre lines. The text then has equal padding inside the box and out.
  Unlike window frames, don't put the rail on the cell's outer edge.

**Prompt buttons:**
- In `left`, the label's first cell is at column S, row T.
- In `middle`, the label is row T.
- In `right`, the label's last cell is at column 0, row T.
- A label one cell wide is covered by both `left` and `right`.
- The images go under the label's text. The label keeps the foreground of
  `browser.prompt.{text,highlight,hover,clicked}_attr` by state, and its
  background is dropped. Keep row T calm so the label stays readable, and tune
  those attrs' `fg` if it isn't.
- In the search box, the label keeps the foreground of
  `editor.standard.search.button_attr` or `button_hover_attr`, over the
  background of the search box's `attr`.
- Prompts reserve room for the outsets, so a larger T+B makes prompts taller.
  The search box reserves room only for S, and cuts T and B off at its edges.
- Buttons are max(1, 2S) cells apart. With S ≥ 1, neighbouring buttons' outset
  columns touch with no gap, so keep the art off the outer edge of the outset.

**File tabs:**
- In `left`, the name's first cell is at column S; in `right`, its last cell is
  at column 0.
- Rune draws the name on row 1 lifted half a cell, so it sits in the middle of
  the R rows. Keep that band calm so the name stays readable.
- Tabs are as many cells apart as `browser.tab_name_separator` is wide; keep 2S
  within that, or neighbouring outsets overlap.
- The bar leaves S blank cells before the first tab and at least S after the
  last, so every tab's outset shows.
- The highlight covers row 0 of the focused window's tab only, cell by cell,
  over the tab images and under the name's lifted half. A highlight one cell
  wide uses `left`.
- A tab activates when the button is released over it, as a quick menu button
  does. `hover` and `pressed` apply only to tabs not in focus: a tab in focus
  keeps `focus` or `focus_window` under the pointer. Draw the states on the
  same canvas, so nothing jumps between them.
- `browser.hover_tab_attr` is layered over the name and icon of the tab under
  the pointer, under any colour the tab has of its own, such as
  `dirty_tab_attr`. Its `bg` fills the blanks of the name too.

**Workspace tabs:**
- The same as file tabs, upside down: Rune draws the name on row 0 lowered half
  a cell, and the highlight covers the last row, under the workspace in focus.
- Tabs are as many cells apart as `browser.tab_name_separator` is wide.
- The bar takes `browser.hover_tab_attr`, but its own `workspace.tab_images`
  and `workspace.tab_sounds`.

**Program status inbox:**
- An entry spans from its marker column to the right edge of the selection.
  In `left`, the marker column is at column S, row T; in `right`, the entry's
  last cell is at column 0, row T. The entry's N rows follow row T.
- N changes from entry to entry, so the images stretch vertically. Keep the
  corner radius small, so the corners still look right when stretched from 2
  to 4 rows, and check entries of different heights.
- The entry's text is drawn over the images; keep the band under it calm.
- Entries are 1 row apart, and the list has 1 cell of padding above and on its
  left: keep T+B and S within 1, or neighbouring outsets overlap.
- The marker covers the selected entry's marker column, row by row, over the
  entry images. A marker one cell tall uses `top`.
- Progress bar images cover the bar's cells, one per cell, over the entry
  images; the percentage label stays as text to the bar's right.

**Status bar `.Image`:**
- The image is `width` × `height` cells, centred on its anchor: the first
  reserved cell.
- The anchor is at column ⌊w/2⌋, row ⌊h/2⌋ of the image, before `x_offset`,
  `y_offset` and the pixel offsets move it.
- An image can extend above the bar into editor or chat content without
  `overflow`. `overflow` permits drawing outside that component's bounds.

**Quick menu:**
- Buttons can be square or any other shape that fits the design aesthetics and
contain-fitted, stacked half a cell.
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
  tab_images: {outset: {sides: 1}}
workspace:
  tab_images: {outset: {sides: 1}}
command:
  separator_charset: {graphics: {outset: {top: 1, bottom: 1, inward: 1}}}
program_status:
  images: {outset: {top: 1, bottom: 1, sides: 1}}
```
Outsets are whole cells, 0 or more. Separator outsets default to zero and
have no `sides` or `horizontal_top` field.

### `mode`
Every `graphics` table takes a `mode`:
- `replace` (the default) hides the character under each image.
- `overlay` keeps it, showing through the image's transparent pixels in its own
  colour, which dimming and the command prompt's focus shader still change.
  Leave the character's path through the image transparent to let them show;
  see Channel glyphs.
- For the window bar, `replace` sits the icon and title on the frame's
  background, and `overlay` keeps the bar's colours behind them.
- A separator's omitted mode inherits the effective command frame mode rather
  than independently defaulting to `replace`. A successful junction image in
  `replace` hides its glyph; `overlay` keeps the separator glyph, not the side
  sprite.
- For the file and workspace tab highlights, `replace` hides
  `focus_tab_highlight_char`, so the tab image shows through the highlight's
  transparent pixels; `overlay` draws the character over both. The highlight
  images draw even when `focus_tab_highlight_char` is `""`.
- For the inbox marker and progress bar, `replace` hides
  `program_status.marker` and `program_status.progress_bar`, so the entry
  images show through their transparent pixels; `overlay` keeps the characters
  in their colours.

`browser.prompt.images`, the `tab_images` tables and `program_status.images`
take no `mode`.

### Fallbacks
- **Tabs:** `focus_window` falls back to `focus`, and `focus` to `non_focus`,
  not to `hover`. `pressed` falls back to `hover`, and `hover` to `non_focus`.
- **Scroll bar:** a missing `top_char` or `bottom_char` uses the middle image,
  and a missing `hover_` image uses the plain one.
- **Command prompt frame:** slots `command.frame_charset.graphics` leaves out
  keep the normal window frame's images, forming the effective command frame.
- **Agent compose box frame:** slots `input_box_frame_charset` leaves out keep
  the default `┌─┐│└┘` runes and draw no image; they don't inherit the window
  frame. `input_box_frame_attr` colours the runes, which `overlay` images show.
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
- **Prompt buttons, tabs and inbox entries:** a missing `left` or `right`
  leaves its cells without an image; it doesn't fall back to `middle`.
- **File and workspace tab highlights:** a missing `left` or `right` uses
  `horizontal`.
- **Inbox entries:** `selected` falls back to `hover`, and `hover` to `idle`.
  No state is required: with only `selected`, the other entries keep their
  colours.
- **Inbox marker:** a missing `top` or `bottom` uses `vertical`.
- **Inbox progress bar:** a missing `left` or `right` uses the `filled` or
  `empty` image of the cell it stands in.

### Minimal command separator config
These absolute URIs are placeholders, not existing assets. Replace them with
newly written asset paths and add these keys to the theme's config:

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
- `.Image` in `terminal.plugin.bar_layout` or `editor.<mode>.message_bar.layout`:
  a config error.
- `browser.frameunion_charset` and `browser.window_manager.close_icon`:
  characters only.

### Channel glyphs
Rune's GUI font draws `\U00100008`–`\U00100011` itself, as a channel for
`overlay` frame images to frame. Set them as a frame charset's characters, use
`mode: overlay`, and leave the channel transparent in the art: the glyph then
shows through in the frame's colour, dims with the window, and carries the
command prompt's focus shader glow inside the images.
- `\U00100008`–`\U0010000F` are, in order, `topleft`, `topright`,
  `bottomleft`, `bottomright`, `horizontaltop`, `horizontalbottom`,
  `verticalleft` and `verticalright`. Corners turn from one groove into the
  other.
- Draw the rail flush with the cell's outer edge, half the cell's width thick
  at a side and a quarter of its height at the top or bottom. Leave its groove
  transparent: from 1/8 to 3/8 of the cell's width in from a side edge, or
  from 1/16 to 3/16 of its height in from the top or bottom edge.
- `\U00100010` and `\U00100011` branch the left and right channels into a
  bottom one. Set them as `command.separator_charset` `left` and `right`, with
  `\U0010000D` as the command frame's `horizontalbottom`, which the separator
  inherits, to run the channel along the separator too.

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

Add the examples to the theme's config; do not replace unrelated keys or
remove the user's useful fields. In Starlark, the equivalent paths are
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
| `src` | Quoted absolute `file:///`, `https://` or `http://` URI | No `~` or environment-variable expansion, except `$RUNE_DATADIR` in a `file://` URI. |
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
  - `browser.tab_sounds` and `workspace.tab_sounds` with `hover`, when the
    pointer moves onto a tab not in focus, and `click`, when a tab is clicked.
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
browser: {tab_sounds: {hover: hud-ember-hover, click: hud-ember-click}}
workspace: {tab_sounds: {hover: hud-ember-hover, click: hud-ember-click}}
```

## Apply and verify
Run `runectl screenshot` yourself and inspect the result after each revision.
For the keys under Checking your work, this also applies the change; do not
ask the user to reload them. Do not restart Rune or reload the agent's own
workspace automatically: that can interrupt the conversation and running work.
For focus styles, capture with two tiles and a floating window open, so the
focused and unfocused states, and a float over the panel, show in one
screenshot. Capture once more with the command prompt open, and once with one
of Rune's standard prompts open, such as the close-dirty-tabs prompt: ask the
user to open it if you can't, and never answer it yourself. Crop and enlarge
(4x or more) the seam between the two tiles, a frame corner, the compose box's
corners and bottom edge, the command prompt's corners and separator, the
standard prompt's message and edges, and both ends of each status bar, in
both states. On each side of every line, sample the pixel colour: outside
must be the theme background, inside the pane colour of that state. Behind
the standard prompt's text and along the inside of its frame, the colour must
be the same as the rest of its interior. A band, halo, notch, square corner
or lighter rectangle in any crop means the work isn't done.

| Change | Takes effect |
|---|---|
| A key listed under Checking your work, or an image it names, even overwritten at the same path | `runectl screenshot`, in every open workspace |
| Any other `browser.*`, `command.*` or `extensions.*` key | `workspacereload` in the focused workspace |
| Any other `gui.*` key, `animations.*` | Restart Rune. `:guiwindownew` opens a fresh process to preview them |
| A sound file overwritten at the same path | The next time it plays |
| The wallpaper file overwritten at the same path | `runectl screenshot` or `workspacereload` |
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
- An unreadable app icon shows a notification and restores the default icon.
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
   ornaments obscure text. When the change touches window backgrounds, the
   theme or the inbox, open `programstatus` with programs listed and check
   that its program list stands apart from the preview and the selected
   entry stands apart from the list.
8. Fix any config errors shown in the captured screenshot or in the logs at
$RUNE_DATADIR/debug.log.
9. Adjust the assets, spacing, dimensions, offsets or colours and repeat steps
   5–8 until the rendered result meets the brief. A successful file write or
   screenshot command alone is not verification: inspect the resulting image.
10. Update the manifest and report the final screenshot path, changed keys and
   rollback location. Note any state you could not inspect or change that still
   needs activation.
11. Ask the user if the new UI is to their liking.
   rollback location. Note any state you could not inspect or change that still
   needs activation.
