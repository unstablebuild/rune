---
sidebar_position: 0
sidebar_label: Make Rune Yours
title: Make Rune Yours
---

export const Shot = ({name, alt, link, children}) => {
  const src = `https://assets.rune.build/images/docs-rune/styling/${name}.webp`;
  const img = (
    <img
      className="docs-figure"
      style={{marginTop: 0, marginBottom: 0}}
      src={src}
      alt={alt}
      loading="lazy"
    />
  );
  return (
  <figure style={{margin: '2rem 0'}}>
    {link ? <a href={src} target="_blank" rel="noreferrer">{img}</a> : img}
    {children ? (
      <figcaption style={{
        textAlign: 'center',
        marginTop: '0.9rem',
        fontSize: '0.9em',
        color: 'var(--ifm-color-emphasis-700)',
      }}>{children}</figcaption>
    ) : null}
  </figure>
  );
};

export const Gallery = ({children}) => (
  <div style={{
    display: 'grid',
    gridTemplateColumns: 'repeat(auto-fit, minmax(300px, 1fr))',
    columnGap: '1.5rem',
  }}>{children}</div>
);

# Make Rune Yours

You can change how Rune looks at three levels. Start with the first, and go
further only when it doesn't give you the look you want:

1. **[Pick a theme](#themes).** Rune ships 16 color schemes, and one command
   switches between them. A theme recolors everything at once: the editor,
   prompts, and even the CLIs and TUIs running on your terminal.
2. **[Draw the interface with images](#graphical-themes).** In the GUI, a
   graphical theme draws frames, tabs, buttons and bars with images instead
   of characters. Describe the look you want to Rune Agent, and its
   `rune-hud` skill draws the images and writes the config for you.
3. **[Fine-tune each element](#fine-tune-each-element).** Rune's built-in
   config gives every part of the interface a default: its colors, the
   characters it is drawn with, the layout of its bars and the animations
   that play over it. Change any of them in your own config.

Unless a caption says otherwise, every screenshot was taken with the default
`romero` theme and the [Vim editor](../learn/vim-editor.md), changing only the keys
shown above it.

## Themes

A theme is a palette. It decides what each color name, such as `yellow` or
`gray`, looks like, along with the text, background and cursor colors. Every
part of Rune draws with those names, so switching the theme recolors all of
it. Here is the same window under each of the 16 built-in themes: code on the
left, a chat with Rune Agent at the top right, and `git log` in a terminal
below it. The theme recolors the output of the programs in your terminal
too. [Dimming](#dimming) is off, so that every window shows its colors. Click
one to see it at full size:

<Gallery>
<Shot link name="theme-romero" alt="Go code, an agent chat and a colored git log graph in a terminal, under the romero theme">`romero`, the default</Shot>
<Shot link name="theme-furman" alt="Go code, an agent chat and a colored git log graph in a terminal, under the furman theme">`furman`</Shot>
<Shot link name="theme-carmack" alt="Go code, an agent chat and a colored git log graph in a terminal, under the carmack theme">`carmack`</Shot>
<Shot link name="theme-reynolds" alt="Go code, an agent chat and a colored git log graph in a terminal, under the reynolds theme">`reynolds`</Shot>
<Shot link name="theme-brevik" alt="Go code, an agent chat and a colored git log graph in a terminal, under the brevik theme">`brevik`</Shot>
<Shot link name="theme-thompson" alt="Go code, an agent chat and a colored git log graph in a terminal, under the thompson theme">`thompson`</Shot>
<Shot link name="theme-pike" alt="Go code, an agent chat and a colored git log graph in a terminal, under the light pike theme">`pike`, a light theme</Shot>
<Shot link name="theme-hopper" alt="Go code, an agent chat and a colored git log graph in a terminal, under the hopper theme">`hopper`</Shot>
<Shot link name="theme-kernighan" alt="Go code, an agent chat and a colored git log graph in a terminal, under the kernighan theme">`kernighan`</Shot>
<Shot link name="theme-wozniak" alt="Go code, an agent chat and a colored git log graph in a terminal, under the wozniak theme">`wozniak`</Shot>
<Shot link name="theme-auge" alt="Go code, an agent chat and a colored git log graph in a terminal, under the auge theme">`auge`</Shot>
<Shot link name="theme-ritchie" alt="Go code, an agent chat and a colored git log graph in a terminal, under the light ritchie theme">`ritchie`, a light theme</Shot>
<Shot link name="theme-kelleher" alt="Go code, an agent chat and a colored git log graph in a terminal, under the kelleher theme">`kelleher`</Shot>
<Shot link name="theme-sanfilippo" alt="Go code, an agent chat and a colored git log graph in a terminal, under the sanfilippo theme">`sanfilippo`</Shot>
<Shot link name="theme-frost" alt="Go code, an agent chat and a colored git log graph in a terminal, under the frost theme">`frost`</Shot>
<Shot link name="theme-mullen" alt="Go code, an agent chat and a colored git log graph in a terminal, under the mullen theme">`mullen`</Shot>
</Gallery>

`guitheme <name>` switches the theme for the current session, and `guitheme`
with no name returns to the default. To start Rune with one, set
`gui.default_theme`:

```yaml tab
gui:
  default_theme: carmack
```

```python tab
config["gui"]["default_theme"] = "carmack"
```

[Themes](./themes.md) shows how to write a palette of your own.

:::tip[Use named colors]
Themes are why this guide writes every color by its name, such as `yellow`,
`teal` or `gray`, rather than as a hex value such as `"#ecbe7b"`. A style
written with names follows every theme you switch to. A hex color stays the
same under every theme, and can end up unreadable under the next one. Keep
hex values for the one place that decides what the names look like: a
theme's palette, under `gui.themes`.
:::

## Graphical themes

When a palette isn't enough, a graphical theme changes the shapes too. In the
GUI, it draws the interface with images: window frames, tabs, buttons, bars,
the command prompt and the quick menu. It pairs them with a palette, a
wallpaper, an app icon and sounds, so that all of Rune shares one look. This
is **Violet Ledger**, a graphical theme of dark plum cards, violet buttons and
chalk text, which every screenshot in this section shows:

<Shot name="vl-overview" alt="Rune under Violet Ledger: rounded violet and plum tabs, an editor beside an agent chat framed in lavender, flat plum status bars with badges, a recessed composer, a column of keycap buttons on the right and rounded workspace tabs">Violet Ledger: rounded tabs, a lavender frame around the focused chat, flat status bars with badges, a recessed composer and a quick menu of keycaps.</Shot>

### Ask Rune Agent

The easiest way to get a graphical theme is to ask for one.
[Rune Agent](../agent/intro.md) ships a `rune-hud` skill for this work. Describe
the look you want in a chat, and the agent loads the skill. To load it by
name, type `/rune-hud`.

:::tip[Use an OpenAI or Codex model]
Ask with a model from the `openai` or `codex`
[provider](../agent/providers.md). They are the ones that give the agent an
`imagegen` tool, which generates and edits images from a description. Codex
signs in with your ChatGPT account, and OpenAI takes an API key. Switch the
chat's model with `chatmodel`, for example `chatmodel codex/gpt-6-astra`.

With any other model, the agent has no image generation, so it builds every
image with scripts, out of shapes, lines and gradients. That can be enough
for flat frames and simple icons, but it may not achieve the look you
describe.
:::

For example:

> Give Rune a graphical theme of dark plum cards, violet buttons and chalk
> text, with rounded tabs and a quick menu of keycaps.

From there, the agent:

- Finds your config and backs it up before its first change. It then edits
  only the keys it needs, and leaves a comment next to each value it replaces
  that names the one before.
- Draws the images, starting with the window frames, and keeps every surface
  in one style: one accent color, light from one side, and buttons and tabs
  of the same shape. It saves each revision to a new folder under
  `$RUNE_DATADIR/hud/<theme>/` and keeps the earlier ones, so you can roll
  back.
- Takes a screenshot with
  [`runectl screenshot`](../develop/runectl.md#screenshots) after each change,
  looks at it, and fixes what doesn't line up, such as the seams between
  tiles, the joins between surfaces and the padding around labels.

Once you have a theme, ask for one change at a time: a brighter frame around
the focused window, a badge behind the mode in the status bar, or a new icon
for a quick menu button.

### Build one by hand

The rest of this section goes through what the agent writes, surface by
surface, in Violet Ledger's config. Read it to adjust a graphical theme
yourself, or to build one from images of your own.
[Seeing your changes](#seeing-your-changes) shows how to apply each key.

<Shot name="vl-sprites" alt="Violet Ledger's dialog buttons and tabs in each of their four states, assembled from their left, middle and right slices, and its seventeen quick menu icons">A few of its 151 images, assembled from their slices.</Shot>

### How images replace characters

Most surfaces are drawn from a **charset**: a character for each slot, such as
the eight slots of a frame. A charset also takes a `graphics` table that names
an image for each slot:

```yaml
frame_charset:
  topleft: "╭"          # what a terminal draws, and the GUI when the image fails
  # ...
  graphics:
    mode: replace       # or overlay
    topleft: file://$RUNE_DATADIR/hud/violet-ledger/frame/normal/topleft.png
    # ...
```

- `mode: replace` draws the image instead of the character. `mode: overlay`
  draws it over the character, so the character shows through its
  transparent pixels.
- Each image fills one cell and is stretched to it. Slots along an edge repeat
  once per cell, so their images must tile without a seam.
- An `outset` lets images reach past their cells, into the cells around them,
  in whole cells. Which sides it takes depends on the surface.
- Buttons, tabs and inbox entries are not charsets: they take `left`, `middle`
  and `right` images for each state, stretched across the label.
- Images are drawn as they are. Attributes, the theme and
  [dimming](#dimming) don't tint them, so ship separate images for each state,
  such as focused and unfocused frames. Transparent pixels show the cell's
  background. On a floating window's frame and window bar, they show whatever
  is under the window instead, so a rounded corner stays round over a coloured
  pane. Make those images opaque inside the window and transparent outside it.
  The close icon and the title over a window bar's images sit on that same
  background, so the background of `close_icon_attr` applies only where the
  bar draws characters.
- Image URIs are absolute `file://` or `https://` URIs. In a `file://` URI,
  Rune expands `$RUNE_DATADIR` and no other variable. The wallpaper and the
  app icon take plain paths instead. A URI Rune cannot parse rejects its whole
  `graphics` table.
- Rune caches images by URI. `themereload` and `runectl screenshot` read them
  again, so you can edit an image in place while you iterate.
- In a terminal, and wherever an image fails to load, Rune draws the
  characters. Violet Ledger keeps rounded box-drawing characters under its
  rounded images for that reason.

The snippets below are Violet Ledger's config, with the folders it keeps for
each revision left out of the paths. Its images live in
`$RUNE_DATADIR/hud/violet-ledger/`.

### Start with the palette

A graphical theme starts as an ordinary [theme](./themes.md). Its palette is
where its hex values belong, and the rest of your config can keep using
names:

```yaml tab
gui:
  default_theme: violet-ledger
  themes:
    violet-ledger:
      background: "#211d2b"
      foreground: "#eeeaf2"
      cursor: "#b8a4ff"
      black: "#14111c"
      gray: "#484051"
      silver: "#b4acbf"
      white: "#f8f5fc"
      navy: "#4c386d"
      purple: "#9672ff"
      blue: "#a89af9"
      maroon: "#9380a4"
      magenta: "#c6b3f2"
      cyan: "#bac6ed"
      teal: "#8eb7be"
      green: "#a8c9aa"
      lime: "#bcdbbc"
      olive: "#a9987d"
      yellow: "#dfca9f"
      red: "#e09bad"
```

```python tab
config["gui"]["default_theme"] = "violet-ledger"
config["gui"]["themes"]["violet-ledger"] = {
    "background": "#211d2b", "foreground": "#eeeaf2", "cursor": "#b8a4ff",
    "black": "#14111c", "gray": "#484051", "silver": "#b4acbf", "white": "#f8f5fc",
    "navy": "#4c386d", "purple": "#9672ff", "blue": "#a89af9", "maroon": "#9380a4",
    "magenta": "#c6b3f2", "cyan": "#bac6ed", "teal": "#8eb7be", "green": "#a8c9aa",
    "lime": "#bcdbbc", "olive": "#a9987d", "yellow": "#dfca9f", "red": "#e09bad",
}
```

Its syntax colors stay distinct, muted pastels, while violet and its lighter
and darker shades are kept for the interface. Violet Ledger also names a few
exact colors in [attributes](#attributes) such as `focus_frame_attr`, to match
the pixels of its images. That's the one exception to using named colors:
images don't follow another theme either, so a graphical theme is a single
look rather than a palette to swap.

### Window frames {/* #graphical-window-frames */}

```yaml tab
browser:
  window_manager:
    frame_charset:
      graphics:
        mode: replace
        topleft: file://$RUNE_DATADIR/hud/violet-ledger/frame/normal/topleft.png
        topright: file://$RUNE_DATADIR/hud/violet-ledger/frame/normal/topright.png
        bottomleft: file://$RUNE_DATADIR/hud/violet-ledger/frame/normal/bottomleft.png
        bottomright: file://$RUNE_DATADIR/hud/violet-ledger/frame/normal/bottomright.png
        horizontaltop: file://$RUNE_DATADIR/hud/violet-ledger/frame/normal/horizontaltop.png
        horizontalbottom: file://$RUNE_DATADIR/hud/violet-ledger/frame/normal/horizontalbottom.png
        verticalleft: file://$RUNE_DATADIR/hud/violet-ledger/frame/normal/verticalleft.png
        verticalright: file://$RUNE_DATADIR/hud/violet-ledger/frame/normal/verticalright.png
    focus_frame_charset:
      graphics:
        mode: replace
        topleft: file://$RUNE_DATADIR/hud/violet-ledger/frame/focused/topleft.png
        # ...and the other seven slots, from frame/focused/
```

```python tab
vl = "file://$RUNE_DATADIR/hud/violet-ledger"
slots = [
    "topleft", "topright", "bottomleft", "bottomright",
    "horizontaltop", "horizontalbottom", "verticalleft", "verticalright",
]

def images(folder):
    ret = {"mode": "replace"}
    for s in slots:
        ret[s] = vl + "/" + folder + "/" + s + ".png"
    return ret

wm = config["browser"]["window_manager"]
wm["frame_charset"] = {"graphics": images("frame/normal")}
wm["focus_frame_charset"] = {"graphics": images("frame/focused")}
```

<Shot name="vl-overview-editor" alt="The editor focused with a rounded lavender frame, next to the agent chat with a thin dark outline">With the editor in focus: a lavender outline around it, and a thin dark one around the chat. The interiors are transparent, so the frames never cover what is inside.</Shot>

The scroll bar works the same way, with `top_char`, `middle_char`,
`bottom_char` and their `hover_` versions under
`scroll_bar_charset.graphics`, and an `outset` with `left`, in cells.

A theme can mark the focused window with a background image instead of a
brighter frame: `focus_background_graphics` draws a panel behind it, and
`focus_scroll_bar_charset`, the status bars, the inbox and the agent chat
take images painted for that panel. See [Focus styles](#focus-styles).

### Tabs

`browser.tab_images` draws every tab with a `left`, `middle` and `right`
image, for each of `focus`, `focus_window`, `non_focus`, `hover` and
`pressed`. Tabs in focus always draw their `focus` images, or the `non_focus`
ones without them. The tab of the focused window draws `focus_window`
instead, falling back to `focus`, so it can stand out from the tabs shown in
other windows. The other tabs draw `hover` and `pressed` under the pointer,
falling back to `non_focus`. Violet Ledger lets its tabs reach one cell past
each side of the label, and turns off the line the tab bar draws over the
focused tab:

```yaml tab
browser:
  focus_tab_highlight_char: ""
  tab_images:
    outset: {sides: 1}
    focus:
      left: file://$RUNE_DATADIR/hud/violet-ledger/tab/focus/left.png
      middle: file://$RUNE_DATADIR/hud/violet-ledger/tab/focus/middle.png
      right: file://$RUNE_DATADIR/hud/violet-ledger/tab/focus/right.png
    non_focus:
      left: file://$RUNE_DATADIR/hud/violet-ledger/tab/non-focus/left.png
      middle: file://$RUNE_DATADIR/hud/violet-ledger/tab/non-focus/middle.png
      right: file://$RUNE_DATADIR/hud/violet-ledger/tab/non-focus/right.png
    # hover and pressed, the same way
```

```python tab
vl = "file://$RUNE_DATADIR/hud/violet-ledger"

def tab(state):
    return {p: vl + "/tab/" + state + "/" + p + ".png" for p in ["left", "middle", "right"]}

tabs = {"outset": {"sides": 1}}
for key, state in [("focus", "focus"), ("non_focus", "non-focus"), ("hover", "hover"), ("pressed", "pressed")]:
    tabs[key] = tab(state)
config["browser"]["focus_tab_highlight_char"] = ""
config["browser"]["tab_images"] = tabs
config["workspace"]["focus_tab_highlight_char"] = ""
config["workspace"]["tab_images"] = tabs
```

<Shot name="vl-tabs" alt="Four tabs: README.md and trajectory.go on dark plum plates, main.go and rocket-launch on bright violet ones" />

`workspace.tab_images` takes the same keys for the workspace bar. Violet
Ledger gives it the same images:

<Shot name="vl-wsbar" alt="The workspace bar with 1 ~/acme on a violet plate and 2 ~/launchpad on a plum one" />

`focus_tab_highlight_graphics` draws the highlight with `left`, `horizontal`
and `right` images instead, for themes that keep it.

### Status bars

`{{ .Image }}` places an image in a status bar layout. Violet Ledger stacks
two under the text: a rail across the whole bar, and a badge behind the mode.
`z_index` below `0` draws an image under the text, `reserve` is the number of
cells it takes in the row, and `width "100%"` with `x_offset "50%"` spans the
bar:

```yaml tab
editor:
  status_bar:
    layout: '{{ .Image | src "file://$RUNE_DATADIR/hud/violet-ledger/status/editor-rail.png" | width "100%" | height 1 | reserve 0 | x_offset "50%" | fit "fill" | z_index -2 | alt "" }}{{ .Image | src "file://$RUNE_DATADIR/hud/violet-ledger/status/mode-cap.png" | width 3 | height 1 | reserve 4 | x_offset 1 | fit "fill" | z_index -1 | alt "" }}{{ .Status | bold }}  {{ .Filepath }}  {{ .GitShortRef | fg "blue" }} +{{ .GitDiffAdd | fg "silver" }} −{{ .GitDiffDel | fg "silver" }}{{ .ShiftRight }}{{ .Language | fg "blue" }}  {{ .CursorLine }}:{{ .CursorColumn }} / {{ .TotalLines | fg "silver" }} {{ .Image | src "file://$RUNE_DATADIR/hud/violet-ledger/status/end-cap.png" | width 2 | height 1 | reserve 2 | z_index -1 | alt "" }}'
```

```python tab
vl = "file://$RUNE_DATADIR/hud/violet-ledger/status/"
config["editor"]["status_bar"]["layout"] = (
    '{{ .Image | src "' + vl + 'editor-rail.png" | width "100%" | height 1 | reserve 0 | x_offset "50%" | fit "fill" | z_index -2 | alt "" }}' +
    '{{ .Image | src "' + vl + 'mode-cap.png" | width 3 | height 1 | reserve 4 | x_offset 1 | fit "fill" | z_index -1 | alt "" }}' +
    '{{ .Status | bold }}  {{ .Filepath }}  {{ .GitShortRef | fg "blue" }}' +
    ' +{{ .GitDiffAdd | fg "silver" }} −{{ .GitDiffDel | fg "silver" }}' +
    '{{ .ShiftRight }}{{ .Language | fg "blue" }}' +
    '  {{ .CursorLine }}:{{ .CursorColumn }} / {{ .TotalLines | fg "silver" }} ' +
    '{{ .Image | src "' + vl + 'end-cap.png" | width 2 | height 1 | reserve 2 | z_index -1 | alt "" }}'
)
```

<Shot name="vl-editor-status" alt="A flat plum status bar with a small badge on the left, NORMAL, main.go, the branch and diff counts, and the language and position on the right" />

`alt ""` draws nothing in a terminal, where images can't show. The
[agent's status bar](../agent/config.md#images) lists every `{{ .Image }}`
directive.

### Command prompt {/* #graphical-command-prompt */}

`command.frame_charset.graphics` gives the prompt a frame of its own, and
`command.separator_charset.graphics` draws the line above the manual with
`left`, `horizontal` and `right` images. The separator's `outset` takes `top`,
`bottom` and `inward`: how far its ends reach into the prompt.

```yaml tab
command:
  frame_charset:
    topleft: "╭"
    # ...the other characters
    graphics:
      mode: replace
      topleft: file://$RUNE_DATADIR/hud/violet-ledger/frame/prompt/topleft.png
      # ...the other seven slots, from frame/prompt/
  separator_charset:
    left: "├"
    right: "┤"
    graphics:
      mode: replace
      left: file://$RUNE_DATADIR/hud/violet-ledger/separator/left.png
      horizontal: file://$RUNE_DATADIR/hud/violet-ledger/separator/horizontal.png
      right: file://$RUNE_DATADIR/hud/violet-ledger/separator/right.png
      outset: {top: 0, bottom: 0, inward: 1}
```

```python tab
vl = "file://$RUNE_DATADIR/hud/violet-ledger"
frame = {"mode": "replace"}
for s in ["topleft", "topright", "bottomleft", "bottomright",
          "horizontaltop", "horizontalbottom", "verticalleft", "verticalright"]:
    frame[s] = vl + "/frame/prompt/" + s + ".png"
config["command"]["frame_charset"] = {
    "topleft": "╭", "topright": "╮", "bottomleft": "╰", "bottomright": "╯",
    "horizontaltop": "─", "horizontalbottom": "─",
    "verticalleft": "│", "verticalright": "│",
    "graphics": frame,
}
config["command"]["separator_charset"] = {
    "left": "├",
    "right": "┤",
    "graphics": {
        "mode": "replace",
        "left": vl + "/separator/left.png",
        "horizontal": vl + "/separator/horizontal.png",
        "right": vl + "/separator/right.png",
        "outset": {"top": 0, "bottom": 0, "inward": 1},
    },
}
```

<Shot name="vl-prompt" alt="The command prompt in a rounded lavender frame, with matches in violet and the manual of windowconverttab under a joined separator" />

### Dialog buttons

`browser.prompt.images` draws each button with `left`, `middle` and `right`
images, for each of `idle`, `selected`, `hover` and `pressed`. `selected` and
`pressed` fall back to `hover`, and `hover` to `idle`, which is required once
any other state is set. The label stays live text on top, styled by
`text_attr`, `highlight_attr`, `hover_attr` and `clicked_attr`:

```yaml tab
browser:
  prompt:
    images:
      outset: {top: 1, bottom: 1, sides: 1}
      idle:
        left: file://$RUNE_DATADIR/hud/violet-ledger/button/idle/left.png
        middle: file://$RUNE_DATADIR/hud/violet-ledger/button/idle/middle.png
        right: file://$RUNE_DATADIR/hud/violet-ledger/button/idle/right.png
      selected:
        left: file://$RUNE_DATADIR/hud/violet-ledger/button/selected/left.png
        middle: file://$RUNE_DATADIR/hud/violet-ledger/button/selected/middle.png
        right: file://$RUNE_DATADIR/hud/violet-ledger/button/selected/right.png
      # hover and pressed, the same way
```

```python tab
vl = "file://$RUNE_DATADIR/hud/violet-ledger"
images = {"outset": {"top": 1, "bottom": 1, "sides": 1}}
for state in ["idle", "selected", "hover", "pressed"]:
    images[state] = {p: vl + "/button/" + state + "/" + p + ".png" for p in ["left", "middle", "right"]}
config["browser"]["prompt"]["images"] = images
```

<Shot name="vl-dialog" alt="A dialog in a lavender frame with a raised violet Yes button and a dark No button" />

Search boxes with buttons use the same images.

### Floating window bar {/* #graphical-floating-window-bar */}

`browser.window_manager.window_bar_charset.graphics` takes `left`,
`horizontal` and `right` images, and an `outset` with `top`, `bottom`,
`sides`, `inward` and `horizontal_top`. Violet Ledger keeps its bar flat and
inside its cells, and draws the close icon as a lavender `×`:

```yaml tab
browser:
  window_manager:
    window_bar_charset:
      graphics:
        mode: replace
        left: file://$RUNE_DATADIR/hud/violet-ledger/window-bar/left.png
        horizontal: file://$RUNE_DATADIR/hud/violet-ledger/window-bar/middle.png
        right: file://$RUNE_DATADIR/hud/violet-ledger/window-bar/right.png
        outset: {top: 0, bottom: 0, sides: 0, inward: 0, horizontal_top: 0}
    close_icon: "×"
```

```python tab
vl = "file://$RUNE_DATADIR/hud/violet-ledger/window-bar/"
wm = config["browser"]["window_manager"]
wm["window_bar_charset"] = {"graphics": {
    "mode": "replace",
    "left": vl + "left.png",
    "horizontal": vl + "middle.png",
    "right": vl + "right.png",
    "outset": {"top": 0, "bottom": 0, "sides": 0, "inward": 0, "horizontal_top": 0},
}}
wm["close_icon"] = "×"
```

<Shot name="vl-wbar" alt="A floating terminal window with a flat dark header, a lavender close cross and the command as its title, over the editor" />

#### Floating-only frames and focused headers

To leave tiled frames untouched, set `floating_frame_charset` and
`focus_floating_frame_charset` under `browser.window_manager`, rather than
changing the shared `frame_charset` and `focus_frame_charset`. They accept
the same eight rune and graphics slots. For a shadow on each float, bake it
into the floating frame's right and bottom sprites within their cells;
these frame keys do not add an outset or change window geometry.

`focus_window_bar_charset` accepts the same runes, graphics, mode and outsets
as `window_bar_charset`. For example, use black header sprites normally and
lavender sprites in focus, with shadow edges in both floating states:

```yaml
browser:
  window_manager:
    # Leave the shared frame_charset and focus_frame_charset unchanged.
    floating_frame_charset:
      graphics: &float_shadow
        verticalright: file://$RUNE_DATADIR/hud/custom/float-shadow-right.png
        horizontalbottom: file://$RUNE_DATADIR/hud/custom/float-shadow-bottom.png
        bottomright: file://$RUNE_DATADIR/hud/custom/float-shadow-corner.png
    focus_floating_frame_charset:
      graphics: *float_shadow
    window_bar_charset:
      graphics:
        mode: replace
        left: file://$RUNE_DATADIR/hud/custom/black-left.png
        horizontal: file://$RUNE_DATADIR/hud/custom/black-middle.png
        right: file://$RUNE_DATADIR/hud/custom/black-right.png
    focus_window_bar_charset:
      graphics:
        left: file://$RUNE_DATADIR/hud/custom/lavender-left.png
        horizontal: file://$RUNE_DATADIR/hud/custom/lavender-middle.png
        right: file://$RUNE_DATADIR/hud/custom/lavender-right.png
```

Create those assets before using the example. Missing fields in an explicit
table inherit from the shared normal frame, shared focused frame, or normal
window bar, respectively. An absent focused floating override uses the shared
focused frame, **not** `floating_frame_charset`. An empty table (`{}`) is a
present override resolved from those defaults. A graphics slot set to `""`
explicitly clears its inherited image; it is not merged back at draw time.
Invalid optional tables report config errors and fall back to the shared style.

Run `runectl screenshot` or `themereload` after edits. Both refresh these image
URIs, including files overwritten in place, and restyle existing floats.
Inspect a focused and an unfocused float and the unchanged tiled frames.
Remove the optional keys and capture again to restore shared styling; use
`graphics: {verticalright: ""}` instead when only that inherited image should
be cleared. Removing a field from a partial table restores inheritance.

### Notifications {/* #graphical-notifications */}

Violet Ledger draws notifications as rounded cards with
`notifications.frame_charset.graphics`, and draws the progress bar with
`notifications.progress_format.graphics` in `overlay` mode. Its images leave
a clear channel where the bar is, closed in by the card's face above and its
shadow below, and `overlay` fills that channel with the level's color.
Transparent pixels that reach the card's outer edge, such as the corners
outside the rounded cap or a translucent shadow, show what is under the
notification:

```yaml tab
notifications:
  frame_charset:
    graphics:
      mode: replace
      topleft: file://$RUNE_DATADIR/hud/violet-ledger/frame/notification/topleft.png
      # ...the other seven slots, from frame/notification/
  progress_format:
    current: "█"
    current_tip: "█"
    graphics:
      mode: overlay
      start: file://$RUNE_DATADIR/hud/violet-ledger/notification-progress/start.png
      current: file://$RUNE_DATADIR/hud/violet-ledger/notification-progress/current.png
      current_tip: file://$RUNE_DATADIR/hud/violet-ledger/notification-progress/current-tip.png
      remain: file://$RUNE_DATADIR/hud/violet-ledger/notification-progress/remain.png
      end: file://$RUNE_DATADIR/hud/violet-ledger/notification-progress/end.png
```

```python tab
vl = "file://$RUNE_DATADIR/hud/violet-ledger"
frame = {"mode": "replace"}
for s in ["topleft", "topright", "bottomleft", "bottomright",
          "horizontaltop", "horizontalbottom", "verticalleft", "verticalright"]:
    frame[s] = vl + "/frame/notification/" + s + ".png"
config["notifications"]["frame_charset"] = {"graphics": frame}
progress = {"mode": "overlay"}
for s in ["start", "current", "current_tip", "remain", "end"]:
    progress[s] = vl + "/notification-progress/" + s.replace("_", "-") + ".png"
config["notifications"]["progress_format"] = {"current": "█", "current_tip": "█", "graphics": progress}
```

<Shot name="vl-notify" alt="Four notifications as rounded plum cards, each with a thin progress line in its level's color along the bottom" />

### Program status inbox

The [program status](../learn/terminal/program-status.md) inbox, opened with
`programstatus`, takes images under `program_status`:

| Key | Images |
| --- | --- |
| `images` | Each entry: `left`, `middle` and `right` for `idle`, `hover` and `selected`, and an `outset` with `top`, `bottom` and `sides`. `selected` falls back to `hover`, and `hover` to `idle`. |
| `divider_charset.graphics` | The line between the list and the preview: `vertical`, and the `top` and `bottom` joins. |
| `progress_bar_graphics` | A working program's progress: `left`, `filled`, `empty` and `right`. |
| `marker_graphics` | The marker down the selected entry: `top`, `vertical` and `bottom`. |

Violet Ledger draws its entries as cards and its progress bars in `overlay`
mode, and sets `marker` to a space, since the selected card is marked by its
color:

<Shot name="vl-inbox" alt="The program status inbox with a selected violet card for a blocked fuel script, a card for a deploy at 50% with a violet progress bar, and cards for an agent chat and a finished test run" />

### Quick menu

`gui.quick_menu` lists the buttons down the right edge of the window, with
the command each one runs. Give every button a `sprite`, with `idle`, `hover`
and `pressed` images, and Rune draws the menu itself with them. Set sprites
on all buttons or on none. `gui.quick_menu_sounds` plays sounds on them:

```yaml tab
gui:
  quick_menu:
    - title: Command Prompt
      command: echo {prompt}
      sprite:
        idle: file://$RUNE_DATADIR/hud/violet-ledger/quickmenu/idle/command.png
        hover: file://$RUNE_DATADIR/hud/violet-ledger/quickmenu/hover/command.png
        pressed: file://$RUNE_DATADIR/hud/violet-ledger/quickmenu/pressed/command.png
    - title: Agent
      command: agent
      sprite:
        idle: file://$RUNE_DATADIR/hud/violet-ledger/quickmenu/idle/agent.png
        hover: file://$RUNE_DATADIR/hud/violet-ledger/quickmenu/hover/agent.png
        pressed: file://$RUNE_DATADIR/hud/violet-ledger/quickmenu/pressed/agent.png
    # ...up to 24 buttons
```

```python tab
vl = "file://$RUNE_DATADIR/hud/violet-ledger/quickmenu/"

def button(title, command, icon):
    return {
        "title": title,
        "command": command,
        "sprite": {s: vl + s + "/" + icon + ".png" for s in ["idle", "hover", "pressed"]},
    }

config["gui"]["quick_menu"] = [
    button("Command Prompt", "echo {prompt}", "command"),
    button("Agent", "agent", "agent"),
]
```

Violet Ledger's seventeen icons are in the [sheet above](#build-one-by-hand).

### Rune Agent {/* #graphical-rune-agent */}

Violet Ledger styles the agent's chat to match the editor: a recessed
composer drawn with `input_box_frame_charset.graphics` over
`input_background_color`, and a status bar laid out like the editor's, with a
bot badge in place of `{}`. Each phase in `status_bar.status` gets its own
shade of lavender and a braille spinner:

```yaml tab
extensions:
  rune-agent:
    config:
      input_background_color: "#252131"
      input_box_frame_charset:
        topleft: "╭"
        # ...the other characters
        graphics:
          mode: replace
          topleft: file://$RUNE_DATADIR/hud/violet-ledger/frame/compose/topleft.png
          # ...the other seven slots, from frame/compose/
      status_bar:
        layout: '{{ .Image | src "file://$RUNE_DATADIR/hud/violet-ledger/status/agent-rail.png" | width "100%" | height 1 | reserve 0 | x_offset "50%" | fit "fill" | z_index -2 | alt "" }}{{ .Image | src "file://$RUNE_DATADIR/hud/violet-ledger/status/agent-cap.png" | width 3 | height 1 | reserve 4 | x_offset 1 | fit "fill" | z_index -1 | alt "" }}{{ .Spinner }} {{ .Status | bold }}  {{ .Model }}  {{ .Effort | fg "silver" }}{{ .ShiftRight }}{{ .Elapsed | fg "blue" }}  {{ .Cache | fg "silver" }}  {{ .ContextGauge }} '
        status:
          REASONING:
            animation: {ch: "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"}
          # ...one entry per phase
```

```python tab
vl = "file://$RUNE_DATADIR/hud/violet-ledger"
agent = config.setdefault("extensions", {}).setdefault("rune-agent", {}).setdefault("config", {})
agent["input_background_color"] = "#252131"
frame = {"mode": "replace"}
for s in ["topleft", "topright", "bottomleft", "bottomright",
          "horizontaltop", "horizontalbottom", "verticalleft", "verticalright"]:
    frame[s] = vl + "/frame/compose/" + s + ".png"
agent["input_box_frame_charset"] = {
    "topleft": "╭", "topright": "╮", "bottomleft": "╰", "bottomright": "╯",
    "horizontaltop": "─", "horizontalbottom": "─",
    "verticalleft": "│", "verticalright": "│",
    "graphics": frame,
}
```

<Shot name="vl-agent" alt="The bottom of an agent chat: a dark recessed composer with rounded corners, and a flat plum status bar with a violet bot badge, IDLE, the model and a bracketed context gauge" />

### Wallpaper and app icon

`workspace.wallpaper_image` fills empty windows with a picture, and
`gui.app_icon` sets the icon your desktop shows for Rune. Both take plain
paths, which may use `~` and environment variables. Violet Ledger's
wallpaper is a violet keycap, with edges in the exact color of the theme's
background so it blends into the window:

```yaml tab
workspace:
  wallpaper_image:
    path: $RUNE_DATADIR/hud/violet-ledger/wallpaper.png
    width: 100%
    height: 100%
    fit: contain
gui:
  app_icon: $RUNE_DATADIR/hud/violet-ledger/icon.png
```

```python tab
config["workspace"]["wallpaper_image"] = {
    "path": "$RUNE_DATADIR/hud/violet-ledger/wallpaper.png",
    "width": "100%",
    "height": "100%",
    "fit": "contain",
}
config["gui"]["app_icon"] = "$RUNE_DATADIR/hud/violet-ledger/icon.png"
```

<Shot name="vl-wallpaper" alt="An empty window showing a glossy violet keycap with a lightning bolt, under a window with the agent chat" />

See [Wallpaper](./wallpaper.md) and [Sounds](./sounds.md) for the rest of a
theme's surroundings.

### Every surface at a glance

| Surface | Key | Images | `outset` |
| --- | --- | --- | --- |
| Window frames | `browser.window_manager.frame_charset.graphics`, `focus_frame_charset.graphics` | The eight frame slots. | |
| Window backgrounds | `browser.window_manager.background_graphics`, `focus_background_graphics` | The eight frame slots and `center`, with `fit`. | |
| Floating-only frames | `browser.window_manager.floating_frame_charset.graphics`, `focus_floating_frame_charset.graphics` | The eight frame slots; tiled frames stay unchanged. | |
| Scroll bar | `browser.window_manager.scroll_bar_charset.graphics`, `focus_scroll_bar_charset.graphics` | `top_char`, `middle_char`, `bottom_char` and their `hover_` versions. | `left` |
| Floating window bar | `browser.window_manager.window_bar_charset.graphics`, `focus_window_bar_charset.graphics` | `left`, `horizontal` and `right`. | `top`, `bottom`, `sides`, `inward`, `horizontal_top` |
| Tabs | `browser.tab_images`, `workspace.tab_images` | `left`, `middle` and `right` for `focus`, `focus_window`, `non_focus`, `hover` and `pressed`. | `top`, `bottom`, `sides` |
| Tab highlight | `browser.focus_tab_highlight_graphics`, `workspace.focus_tab_highlight_graphics` | `left`, `horizontal` and `right`. | |
| Command prompt | `command.frame_charset.graphics`, `command.separator_charset.graphics` | The eight frame slots; the separator's `left`, `horizontal` and `right`. | separator: `top`, `bottom`, `inward` |
| Dialog buttons | `browser.prompt.images` | `left`, `middle` and `right` for `idle`, `selected`, `hover` and `pressed`. | `top`, `bottom`, `sides` |
| Notifications | `notifications.frame_charset.graphics`, `notifications.progress_format.graphics` | The eight frame slots; the progress bar's five. | |
| Program status inbox | `program_status.images`, `divider_charset.graphics`, `progress_bar_graphics`, `marker_graphics`, and the same under `program_status.focus` | See [Program status inbox](#program-status-inbox). | entries: `top`, `bottom`, `sides` |
| Status bars | `{{ .Image }}` in `editor.status_bar.layout` and the agent's `status_bar.layout`, and in their `focus` blocks | An image of any size, placed in the layout. | |
| Agent composer | `extensions.rune-agent.config.input_box_frame_charset.graphics`, and the same under `focus` | The eight frame slots. | |
| Quick menu | `gui.quick_menu[].sprite` | `idle`, `hover` and `pressed`. | |
| Wallpaper, app icon | `workspace.wallpaper_image`, `focus_wallpaper_image`, `gui.app_icon` | One image each. | |

### Tips from Violet Ledger

- **Pick one accent.** Violet Ledger uses its violet for every selected
  thing: the selected button, the focused tab, the selected inbox entry and
  the agent's badge. Everything else stays in shades of plum.
- **Light everything from one side.** Its raised controls are all lit from
  the top left and stand on a dark base of the same depth, which shrinks when
  a button is pressed. Frames, status bars and inbox cards stay flat.
- **Draw at a larger size.** Its slices are 48 by 96 pixels for one cell, and
  Rune scales them down, so they stay sharp on any display.
- **Check the seams.** Compare the opposite edges of every repeating slice
  pixel for pixel, and the joins between each cap and its middle. A one-pixel
  step shows up once per cell.
- **Fill outside the curves.** Where a rounded corner would show the cell's
  own background, paint the pixels outside the curve in the theme's
  background color. Keep the inside of frames transparent, so they never
  cover what is in the window.
- **Version your images.** Violet Ledger writes each revision to a new folder
  and points the config at it, so going back is a change to the config.

### Share it

To share a graphical theme, publish it as a
[theme package](./themes.md#share-your-theme). Commit the images to the
repository, and name them in its `config.yaml` with URIs such as
`file://$RUNE_DATADIR/lib/$RUNE_PKG_ID/frame/normal/topleft.png`. Rune fills
in the package's name when it installs it.

## Fine-tune each element

Rune's [built-in config](https://github.com/unstablebuild/rune/tree/main/cmd/rune/rune.star)
gives every part of the interface a default style, and your config only needs
the keys you change: Rune merges them onto its defaults. The rest of this
guide goes through each part of the interface, the keys that style it, and
what a change looks like on screen:

<Shot name="anatomy" alt="A Rune window with numbered callouts on the tab bar, the focused window frame, the gutter, the scroll bar, the status bar, the workspace bar, a notification, highlighted code and the end-of-file hatch" />

1. [Tab bar](#tab-bar)
2. [Window frames](#window-frames), here around the focused window
3. [Gutter](#gutter)
4. [Scroll bar](#scroll-bar)
5. [Editor status bar](#editor-status-bar)
6. [Workspace bar](#workspace-bar)
7. [Notifications](#notifications)
8. [Code colors](#code-colors)
9. [End-of-file hatch](#end-of-file-hatch)

Further down are the [command prompt](#command-prompt), [dialogs](#dialogs),
the [terminal program bar](#terminal-program-bar), the chats of
[Rune Agent](#rune-agent) and the [animations](#animations) that play over
all of it.

## Attributes

Every key that ends in `_attr` takes an **attribute**: a foreground color, a
background color and text flags. Each of the three is optional:

```yaml tab
browser:
  window_manager:
    focus_frame_attr:
      fg: yellow
      bg: default
      flags: [bold]
```

```python tab
config["browser"]["window_manager"]["focus_frame_attr"] = {
    "fg": "yellow",
    "bg": "default",
    "flags": ["bold"],
}
```

| Key | Value |
| --- | --- |
| `fg` | Foreground color. |
| `bg` | Background color. |
| `flags` | One flag, or a list of them: `bold`, `dim`, `italic`, `underline`, `strikethrough`, `blink`, `reverse` or `none`. |

A color is one of:

- a palette name: `black`, `maroon`, `green`, `olive`, `navy`, `purple`,
  `teal`, `silver`, `gray`, `red`, `lime`, `yellow`, `blue`, `fuchsia` (or
  `magenta`), `aqua` (or `cyan`) and `white`. The active theme decides what
  each one looks like. Use these.
- `default`, the theme's own foreground or background.
- any other W3C color name, such as `darkorange`, or a hex value, such as
  `"#ecbe7b"`, quoted in YAML, where `#` starts a comment. Neither changes
  with the theme.

A color name Rune does not know falls back to `default`, so check the spelling
when a change seems to do nothing. `colorPalette` shows what each name looks
like under the active theme.

## Seeing your changes

Rune does not watch your config file. After saving it, how you apply a change
depends on the key:

| Keys | How they apply |
| --- | --- |
| Window frames, the scroll bar and window bar, tab bar colors and images, the command prompt frame, dialogs, the editor status bar, notifications, the program status inbox, the quick menu, the wallpaper, themes and sounds. Rune Agent's status bar and composer. | Run `themereload`, or [`runectl screenshot`](../develop/runectl.md#screenshots). Both update every open workspace in place. |
| Code colors, the gutter, the end-of-file hatch, search matches, the command prompt list, dirty-tab colors, tab icons, dimming, `frame`, the terminal program bar, and Rune Agent's messages. | Run `workspacereload`. |
| Fonts and cell spacing, the workspace bar, `notifications.auto_close`, and every animation. | Restart Rune, or open a fresh process with `guiwindownew`. |

Some GUI settings also have commands that change them for the current
session only, which is handy while you experiment. Write the value you settle
on to your config to keep it:

| Command | Changes |
| --- | --- |
| `guitheme <name>` | The theme. |
| `guifont <family>` | The font family. |
| `guifontsize increase\|decrease` | The font size. |
| `guicellwidth increase\|decrease` | The width of each cell. |
| `guilineheight increase\|decrease` | The height of each line. |
| `shaderrun <name>` | Plays a [cell shader](#cell-shaders) once. |

:::tip
Styling also differs between the GUI and Rune in a terminal. The defaults
shown here are the GUI's. In a terminal, Rune draws frames with box-drawing
characters, plays no cursor effect, and never draws images: everything in
[Graphical themes](#graphical-themes) falls back to characters.
:::

## Fonts and spacing

The font settings live under `gui` and need a restart, or the matching
`guifont*` command for the current session.

<Shot name="font-default" alt="Go code in the default font and size">The default: Rune's built-in JetBrains Mono Nerd Font, at a size picked for your display.</Shot>

`font_size` sets the size in points:

```yaml tab
gui:
  font_size: 17
```

```python tab
config["gui"]["font_size"] = 17
```

<Shot name="font-size" alt="The same code with a larger font" />

`line_height_offset` adds pixels to every line, or removes them with a
negative number:

```yaml tab
gui:
  line_height_offset: 8
```

```python tab
config["gui"]["line_height_offset"] = 8
```

<Shot name="font-lineheight" alt="The same code with taller lines" />

`column_width_offset` does the same for the width of every cell. The default
is `-1`:

```yaml tab
gui:
  column_width_offset: 3
```

```python tab
config["gui"]["column_width_offset"] = 3
```

<Shot name="font-colwidth" alt="The same code with wider letter spacing" />

`font_family` names any font family installed on your system. Run `guifont`
and press `<tab>` to list them, and try one there before writing it to your
config: Rune won't start with a family it can't find. A
[Nerd Font](https://www.nerdfonts.com) keeps the icons Rune uses in tabs and
bars:

```yaml tab
gui:
  font_family: 3270 Nerd Font Propo
```

```python tab
config["gui"]["font_family"] = "3270 Nerd Font Propo"
```

<Shot name="font-family" alt="The same code in the 3270 font" />

`ligatures` joins sequences such as `:=`, `!=` and `->` into a single glyph,
when the font provides one:

```yaml tab
gui:
  ligatures: true
```

```python tab
config["gui"]["ligatures"] = True
```

<Shot name="font-ligatures" alt="The same code with ligatures for := and !=" />

## Code colors

`editor.highlights` maps each syntax capture to an attribute. You only write
the captures you change, and adding `flags` alone keeps the capture's default
color:

<Shot name="attrs-default" alt="Go code with the default highlights">The default highlights.</Shot>

By default, Rune colors only a few captures, such as keywords, strings,
numbers and comments. Many more are tagged and left in the default
foreground: `type`, `function`, `property`, `operator` and
`constant.builtin`, among others. Give them colors and flags, and the code
reads at a glance:

```yaml tab
editor:
  highlights:
    keyword: {fg: magenta, flags: bold}
    type: {fg: yellow}
    function: {fg: teal, flags: bold}
    property: {fg: white, flags: italic}
    operator: {fg: maroon}
    constant.builtin: {fg: maroon, flags: [bold, italic]}
    number: {fg: yellow, flags: bold}
    string: {fg: green}
    comment: {fg: silver, flags: italic}
```

```python tab
config["editor"]["highlights"].update({
    "keyword": {"fg": "magenta", "flags": "bold"},
    "type": {"fg": "yellow"},
    "function": {"fg": "teal", "flags": "bold"},
    "property": {"fg": "white", "flags": "italic"},
    "operator": {"fg": "maroon"},
    "constant.builtin": {"fg": "maroon", "flags": ["bold", "italic"]},
    "number": {"fg": "yellow", "flags": "bold"},
    "string": {"fg": "green"},
    "comment": {"fg": "silver", "flags": "italic"},
})
```

<Shot name="attrs-custom" alt="The same code with bold pink keywords, yellow types, bold teal function names, italic fields, red operators, a bold italic nil, bold yellow numbers, green strings and gray italic comments">Types in `yellow`, function names in bold `teal`, fields in italics, and operators and `nil` in `maroon`, which `romero` draws bright red.</Shot>

Which captures a file gets depends on its language's highlighting query.
[Syntax Highlighting](./syntax-highlighting.md) lists every default and
explains how a name such as `function.method` falls back to `function`.

## Window frames

Each [window](../learn/layout-management.md) is drawn inside a frame. Its keys live
under `browser.window_manager`:

| Key | Styles |
| --- | --- |
| `frame_attr` | The frames of windows not in focus, and the frame of the only window when there is one. |
| `focus_frame_attr` | The frame of the window in focus, when there is more than one. |
| `frame_charset` | The characters of `frame_attr`'s frames. |
| `focus_frame_charset` | The characters of the focused window's frame. |
| `frame` | `false` removes frames, scroll bars and window bars. |

<Shot name="frames-default" alt="Two side-by-side windows with thin default frames">The default: thin lines on the outer edge of each cell, gray around the left window and silver around the focused right one.</Shot>

A charset names a character for each of eight slots. These box-drawing
characters give every window the same square frame:

```yaml tab
browser:
  window_manager:
    frame_charset: &box
      topleft: "┌"
      topright: "┐"
      bottomleft: "└"
      bottomright: "┘"
      horizontaltop: "─"
      horizontalbottom: "─"
      verticalleft: "│"
      verticalright: "│"
    focus_frame_charset: *box
```

```python tab
box = {
    "topleft": "┌",
    "topright": "┐",
    "bottomleft": "└",
    "bottomright": "┘",
    "horizontaltop": "─",
    "horizontalbottom": "─",
    "verticalleft": "│",
    "verticalright": "│",
}
config["browser"]["window_manager"]["frame_charset"] = box
config["browser"]["window_manager"]["focus_frame_charset"] = box
```

<Shot name="frames-box" alt="The two windows framed with square box-drawing lines" />

The two charsets don't have to match. Rounded corners for windows in the
background and a heavy frame for the one in focus:

```yaml tab
browser:
  window_manager:
    frame_charset:
      topleft: "╭"
      topright: "╮"
      bottomleft: "╰"
      bottomright: "╯"
      horizontaltop: "─"
      horizontalbottom: "─"
      verticalleft: "│"
      verticalright: "│"
    focus_frame_charset:
      topleft: "┏"
      topright: "┓"
      bottomleft: "┗"
      bottomright: "┛"
      horizontaltop: "━"
      horizontalbottom: "━"
      verticalleft: "┃"
      verticalright: "┃"
```

```python tab
config["browser"]["window_manager"]["frame_charset"] = {
    "topleft": "╭", "topright": "╮", "bottomleft": "╰", "bottomright": "╯",
    "horizontaltop": "─", "horizontalbottom": "─",
    "verticalleft": "│", "verticalright": "│",
}
config["browser"]["window_manager"]["focus_frame_charset"] = {
    "topleft": "┏", "topright": "┓", "bottomleft": "┗", "bottomright": "┛",
    "horizontaltop": "━", "horizontalbottom": "━",
    "verticalleft": "┃", "verticalright": "┃",
}
```

<Shot name="frames-mixed" alt="A rounded frame around the left window and a heavy frame around the focused right window" />

The attributes color the frames. A darker `frame_attr` makes the focused
window stand out:

```yaml tab
browser:
  window_manager:
    frame_attr: {fg: gray}
    focus_frame_attr: {fg: yellow, flags: bold}
```

```python tab
config["browser"]["window_manager"]["frame_attr"] = {"fg": "gray"}
config["browser"]["window_manager"]["focus_frame_attr"] = {"fg": "yellow", "flags": "bold"}
```

<Shot name="frames-attr" alt="A barely visible frame around the left window and a yellow frame around the focused one" />

`frame: false` removes the frames altogether, for the most compact layout:

```yaml tab
browser:
  window_manager:
    frame: false
```

```python tab
config["browser"]["window_manager"]["frame"] = False
```

<Shot name="frames-off" alt="The two windows without frames, separated only by the gap between them" />

### Dimming

`browser.window_manager.dim` fades the windows that are not in focus, so your
eye lands on the one that is. It has no effect with a single window, and the
images of a [graphical theme](#graphical-themes) are never dimmed.
To mark the focused window with a background of its own instead, see
[Focus styles](#focus-styles).

<Shot name="dim-true" alt="The left window dimmed next to the focused right window">`dim: true`, the default.</Shot>

```yaml tab
browser:
  window_manager:
    dim: false # or true, or "b&w"
```

```python tab
config["browser"]["window_manager"]["dim"] = False # or True, or "b&w"
```

<Shot name="dim-false" alt="Both windows at full brightness">`dim: false` keeps every window at full brightness.</Shot>

<Shot name="dim-bw" alt="The left window drawn in grayscale next to the colored focused window">`dim: b&w` (or `bw`) drains the color from unfocused windows instead.</Shot>

### Scroll bar

A window with more lines than fit shows a scroll bar on its right edge. Its
keys live under `browser.window_manager`:

- `scroll_bar_attr` colors it. The default is `blue`, which `romero` draws
  red.
- `scroll_bar_charset` names its characters: `top_char` for its first cell,
  `bottom_char` for its last and `middle_char` for every cell between. The
  `hover_top_char`, `hover_middle_char` and `hover_bottom_char` keys draw it
  while the pointer is on it. A top or bottom you leave out is the middle,
  and a hover key you leave out is the plain one.

```yaml tab
browser:
  window_manager:
    scroll_bar_attr: {fg: yellow}
    scroll_bar_charset:
      top_char: "╻"
      middle_char: "┃"
      bottom_char: "╹"
```

```python tab
config["browser"]["window_manager"]["scroll_bar_attr"] = {"fg": "yellow"}
config["browser"]["window_manager"]["scroll_bar_charset"] = {
    "top_char": "╻",
    "middle_char": "┃",
    "bottom_char": "╹",
}
```

<Shot name="scroll-compare" alt="Two scroll bars side by side: the default solid red bar and a thin yellow line made of characters">Left, the default. Right, the characters above.</Shot>

### Floating window bar

Floating windows, such as the output of a `!` command, carry a bar across
their top edge. Drag it to move the window, double-click it to maximize, or
click the close icon at its left. Its keys live under
`browser.window_manager`:

| Key | Styles |
| --- | --- |
| `window_bar` | `false` draws a plain frame instead. |
| `window_bar_charset` | The `left`, `horizontal` and `right` characters of the bar. |
| `close_icon` | The close icon. |
| `close_icon_attr` | The close icon's color. |

The bar takes the frame's color, so `focus_frame_attr` colors the bar of the
focused window. The title is drawn in the frame's background color.

<Shot name="wbar-default" alt="A floating terminal window with a gray bar across its top, a red dot at the left and the command as its title">The default: a solid bar in the frame's color, with a red dot to close it.</Shot>

```yaml tab
browser:
  window_manager:
    focus_frame_attr: {fg: teal, bg: black}
    window_bar_charset: {left: "▐", horizontal: "█", right: "▌"}
    close_icon: "✕"
    close_icon_attr: {fg: black}
```

```python tab
config["browser"]["window_manager"]["focus_frame_attr"] = {"fg": "teal", "bg": "black"}
config["browser"]["window_manager"]["window_bar_charset"] = {
    "left": "▐",
    "horizontal": "█",
    "right": "▌",
}
config["browser"]["window_manager"]["close_icon"] = "✕"
config["browser"]["window_manager"]["close_icon_attr"] = {"fg": "black"}
```

<Shot name="wbar-custom" alt="The same floating window with a teal bar, half-width ends, a black cross as the close icon and the title in black" />

```yaml tab
browser:
  window_manager:
    window_bar: false
```

```python tab
config["browser"]["window_manager"]["window_bar"] = False
```

<Shot name="wbar-off" alt="The same floating window with a plain frame and no bar" />

## Focus styles

The window in focus can stand out by more than its frame. It can have its own
background color, a background image, and its own scroll bar, status bars,
program status inbox, agent chat and wallpaper. With those, a frame can be
blank or turned off, and the background marks the focused window instead.

As with `focus_frame_attr`, the focus style applies only when there is more
than one window, and only the window focus counts: switching to another app
restyles nothing.

While the command prompt is open, it takes the colors of the focus
background, without the image, and every window under it draws the normal
style. Without a focus background, the prompt draws no window background.

Every `focus_` key follows the same rules:

- Without the key, the focused window draws the normal style.
- A key that is there takes what it leaves out from the normal key, slot by
  slot.
- `""` clears an image slot.

### Background

These keys live under `browser.window_manager`:

| Key | Styles |
| --- | --- |
| `background_attr` | Every window: `bg` fills the cells drawn in the default background, and `fg` replaces the default foreground, for light backgrounds. |
| `focus_background_attr` | The window in focus. |
| `background_graphics` | An image behind every window, in nine slices. |
| `focus_background_graphics` | The image behind the window in focus. |
| `focus_scroll_bar_charset` | The focused window's scroll bar, with the keys of `scroll_bar_charset`, `hover_` ones included. |

A cell with a background of its own keeps it: the selection, the cursor
line, status bars and `terminal.default_attr`. A frame whose `bg` is unset
takes the window's background, so leave `focus_frame_attr.bg` unset for the
frame to blend in.

A theme can name a focus background color of its own, its
[`focus_background`](./themes.md#what-a-theme-overrides), as `pike` and
`ritchie` do. The window in focus takes it, with the image and `fg` of
`background_graphics` and `background_attr`, while neither
`focus_background_attr` nor `focus_background_graphics` is set. It follows
`guitheme` as you switch themes. Set `focus_background_attr: {}` to turn it
off.

### Frame images over a background

A frame image draws its line through the middle of a cell, and a cell has a
single background, so one color would show on both sides of the line. Rune
paints each side in its own color instead:

- Inside the line, toward the content, the transparent pixels show the
  content's color.
- Outside the line, they show what is under the frame: the theme's
  background, or the window under a floating window or the command prompt.

For this, draw each frame image as a line with transparent pixels on both
sides, unbroken from one edge of the cell to the other. Rune finds the inside
from the line itself, so a gap in it, or an image with no line across the
cell, takes one color on both sides. So does an opaque image, which keeps the
colors painted into it.

Only images one cell in size get the inside color. An image that reaches
into the next cell through an outset, such as a command separator junction
with `inward: 1`, shows what is under the frame on both sides of its line.
Draw junctions one cell wide, with no outset, for their inside to take the
prompt's color.

The same goes for every framed surface:

| Surface | Inside the line |
| --- | --- |
| Window frames | The window's `background_attr` color, or in focus, the `focus_background_attr` color, or else the theme's `focus_background`. |
| Command prompt | The `bg` of `command.element_attr`, or else the focus background's. |
| Agent compose box | `input_background_color`. |
| Notifications | The `bg` of `notifications.background_attr`, or else of `notifications.attr`. |

The compose box frame keeps the chat's background under its images, unless
`input_box_frame_attr` sets a `bg` of its own. A frame of characters still
takes `input_background_color`.

This marks the focused window with a background alone. Frames of spaces stay
as a cell of padding around each window, in its background, and keep the
scroll bar and edge dragging:

```yaml tab
browser:
  union_frames: false
  window_manager:
    frame_charset: &blank
      topleft: " "
      topright: " "
      bottomleft: " "
      bottomright: " "
      horizontaltop: " "
      horizontalbottom: " "
      verticalleft: " "
      verticalright: " "
    focus_frame_charset: *blank
    focus_background_attr: {bg: "#1b1f2a"}
```

```python tab
blank = {s: " " for s in [
    "topleft", "topright", "bottomleft", "bottomright",
    "horizontaltop", "horizontalbottom", "verticalleft", "verticalright",
]}
config["browser"]["union_frames"] = False
wm = config["browser"]["window_manager"]
wm["frame_charset"] = blank
wm["focus_frame_charset"] = blank
wm["focus_background_attr"] = {"bg": "#1b1f2a"}
```

`union_frames: false` keeps the tab bar's junctions from drawing lines where
it meets the blank frames. A frame `bg` of its own paints a ring around the
window instead. With `frame: false` the background still marks the focused
window, but without the padding, scroll bar or edge dragging.

### Background image

`background_graphics` and `focus_background_graphics` draw an image behind a
window in the GUI, cut in nine slices: the eight frame slots, `topleft`
through `verticalright`, and `center`. Each corner takes one cell, each edge
is one cell thick, and `center` covers the rest. `fit: fill`, the default,
stretches each slice over its cells, and `fit: tile` repeats it one cell at a
time.

```yaml tab
browser:
  window_manager:
    focus_background_attr: {bg: "#1b1f2a"}
    focus_background_graphics:
      center: file://$RUNE_DATADIR/hud/x/panel/focused/center.png
      topleft: file://$RUNE_DATADIR/hud/x/panel/focused/topleft.png
      # ...the other corners and edges
```

```python tab
wm = config["browser"]["window_manager"]
wm["focus_background_attr"] = {"bg": "#1b1f2a"}
wm["focus_background_graphics"] = {
    "center": "file://$RUNE_DATADIR/hud/x/panel/focused/center.png",
    "topleft": "file://$RUNE_DATADIR/hud/x/panel/focused/topleft.png",
}
```

- The image goes under every cell background and character. Text,
  selections and frame images draw over it, and the transparent pixels of
  frame images show it.
- Where a slice doesn't draw, the color of `focus_background_attr` shows:
  in a terminal, while the image loads, and when it fails to.
- A floating window covers the image of the tile under it.
- The tab bar's joins to the window frames cut the image where they are
  drawn. Set `browser.union_frames: false` to keep it whole.

### Status bars, inbox and chat

The editor status bar, the program status inbox and Rune Agent's chats take
a `focus` block, which is laid over their section key by key. It changes any
style key the section has, without repeating the others:

| Block | Keys it changes |
| --- | --- |
| `editor.status_bar.focus` | `layout`, `background_attr` and `foreground_error_attr`. |
| `program_status.focus` | Any style key, such as `divider_charset`, `divider_attr`, `images`, `marker_graphics` and `progress_bar_graphics`. |
| `extensions.rune-agent.config.focus` | `status_bar`, `input_box_frame_charset`, `input_box_frame_attr`, `input_box_attr`, `input_background_color` and `background_attr`. ACP chats share it. |

```yaml tab
editor:
  status_bar:
    background_attr: {bg: "#20232b"}
    focus:
      background_attr: {bg: "#262b38"}
```

```python tab
config["editor"]["status_bar"]["background_attr"] = {"bg": "#20232b"}
config["editor"]["status_bar"]["focus"] = {"background_attr": {"bg": "#262b38"}}
```

A chat whose `background_attr` sets a color keeps it over the window's
background. Its `focus` block's `background_attr` replaces that color in
every cell the chat draws with it.

### Wallpaper

An empty window in focus shows the focused wallpaper. Its keys live under
`workspace` and take the same forms as the [wallpaper](./wallpaper.md) keys:
`focus_wallpaper_image`, `focus_wallpaper_image_ascii`,
`focus_wallpaper_ascii`, `focus_wallpaper_attr`,
`focus_wallpaper_background_attr` and `focus_wallpaper_density_characters`.
Without any of them, a focused window shows the normal wallpaper. The
focused wallpaper keeps the normal picture, art or text unless one of the
first three is set, and takes each attribute, and the density characters, the
focus keys leave out from the normal ones.

## Tab bar

The tab bar along the top lists the workspace's tabs. Its keys live directly
under `browser`:

| Key | Styles |
| --- | --- |
| `focus_tab_attr` | Tabs shown in a window. |
| `non_focus_tab_attr` | Tabs not shown in any window. |
| `dirty_tab_attr` | Tabs with unsaved changes, which also get a `*`. |
| `hover_tab_attr` | Layered over a tab not in focus while the pointer is on it. |
| `focus_tab_icon_attr` | The icon of the focused window's tab. |
| `non_focus_tab_icon_attr` | The icons of the other tabs. |
| `focus_tab_highlight_attr` | The highlight over the focused window's tab. |
| `focus_tab_highlight_char` | The highlight's character. The default draws a thin line along the top of the cell. |
| `icons` | Icons for files by extension (`".go"`), and the `default`, `terminal`, `console`, `directory` and `open_directory` icons. |
| `tab_override_icon` | One icon for every tab, in place of all of the above. |

<Shot name="tabs-default" alt="A tab bar with a red italic README.md*, a highlighted rocket.go, a gray trajectory.go and main.go">The default. `README.md*` has unsaved changes, `rocket.go` is in the focused window, `main.go` is in the other window and `trajectory.go` is hidden.</Shot>

```yaml tab
browser:
  focus_tab_attr: {fg: white, flags: bold}
  non_focus_tab_attr: {fg: silver, flags: italic}
  focus_tab_icon_attr: {fg: teal}
  focus_tab_highlight_attr: {fg: teal}
```

```python tab
config["browser"]["focus_tab_attr"] = {"fg": "white", "flags": "bold"}
config["browser"]["non_focus_tab_attr"] = {"fg": "silver", "flags": "italic"}
config["browser"]["focus_tab_icon_attr"] = {"fg": "teal"}
config["browser"]["focus_tab_highlight_attr"] = {"fg": "teal"}
```

<Shot name="tabs-attrs" alt="The same tab bar with bold white visible tabs, an italic silver hidden tab and a teal highlight" />

```yaml tab
browser:
  tab_override_icon: "◆"
  dirty_tab_attr: {fg: yellow, flags: [bold, underline]}
```

```python tab
config["browser"]["tab_override_icon"] = "◆"
config["browser"]["dirty_tab_attr"] = {"fg": "yellow", "flags": ["bold", "underline"]}
```

<Shot name="tabs-misc" alt="The same tab bar with a diamond icon on every tab and a bold, underlined, yellow README.md*" />

In the GUI, `tab_images` and `focus_tab_highlight_graphics` draw tabs with
images. See [Tabs](#tabs) in Graphical themes.

## Workspace bar

The workspace bar along the bottom of the window lists your open
[workspaces](../learn/layout-management.md). It appears when more than one is open.
`browser.workspace_bar` picks what it shows, and needs a restart:

```yaml tab
browser:
  workspace_bar: number # or path, the default, or false to hide it
```

```python tab
config["browser"]["workspace_bar"] = "number" # or "path", the default, or False to hide it
```

<Shot name="wsbar-path" alt="The workspace bar listing 1 ~/acme and 2 ~/launchpad">`path`, the default.</Shot>

<Shot name="wsbar-number" alt="The workspace bar listing only the numbers 1 and 2">`number`.</Shot>

The bar takes its colors from the tab bar's `browser.*_tab_attr` keys.
`workspace.focus_tab_highlight_char`, empty by default, marks the workspace in
focus. The character `"\U00100007"` draws a thin line along the bottom of the
cell:

```yaml tab
workspace:
  focus_tab_highlight_char: "\U00100007"
```

```python tab
config["workspace"]["focus_tab_highlight_char"] = "\U00100007"
```

<Shot name="wsbar-highlight" alt="The workspace bar with a red line under 1 ~/acme" />

`workspace.tab_images` and `workspace.focus_tab_highlight_graphics` work as
their `browser` counterparts do.

## Editor status bar

Each editor window ends in a status bar. `editor.status_bar.layout` is a
template of components and literal text:

<Shot name="sbar-default" alt="The default status bar: NORMAL on red, the file name, the branch, added and deleted lines, the cursor position, the line count and the language on red">The default layout.</Shot>

| Component | Shows |
| --- | --- |
| `{{ .Status }}` | The editor's mode, or a status message. |
| `{{ .Filepath }}` | The file's path in the workspace, with its unsaved or read-only marks. |
| `{{ .GitShortRef }}` | The Git branch or commit. |
| `{{ .GitDiffAdd }}`, `{{ .GitDiffDel }}` | Lines added and deleted. |
| `{{ .CursorLine }}`, `{{ .CursorColumn }}` | The cursor's line and column. |
| `{{ .TotalLines }}` | The number of lines. |
| `{{ .Language }}` | The file's language. |
| `{{ .ShiftRight }}` | Everything after it is aligned to the right. |
| `{{ .Image }}` | An image, in the GUI. See [Status bars](#status-bars) in Graphical themes. |

Pipe a component through `fg`, `bg`, `bold`, `italic`, `underline`,
`reverse`, `dim`, `blink` or `strikethrough` to style it. A minimal layout:

```yaml tab
editor:
  status_bar:
    layout: ' {{ .Status | bold }}  {{ .Filepath }}{{ .ShiftRight }}{{ .CursorLine }}:{{ .CursorColumn }} '
```

```python tab
config["editor"]["status_bar"]["layout"] = ' {{ .Status | bold }}  {{ .Filepath }}{{ .ShiftRight }}{{ .CursorLine }}:{{ .CursorColumn }} '
```

<Shot name="sbar-minimal" alt="A status bar with only NORMAL, main.go and 12:1" />

A few rules make richer layouts work:

- Literal text right after a component, up to the next two spaces, belongs to
  that component and takes its style. Separate components with two spaces.
- The shade characters `█▓▒░`, and the Powerline caps of a
  [Nerd Font](https://www.nerdfonts.com) such as `\uE0B0` and `\uE0B6`,
  attached to a component with a background are drawn in that background
  color. The shades make the fading edges of the default layout.
- `editor.status_bar.background_attr` sets the bar's background; only its
  `bg` is used. `foreground_error_attr` colors the language when its parser
  reports an error; only its `fg` is used.
- When the bar runs out of room, Rune drops the branch, the Git counts, the
  language and the line count, in that order.

Those rules are enough to draw pills. This layout wraps the mode and the
cursor position in rounded caps, `\uE0B6` and `\uE0B4` from the Nerd Font Rune
ships with, with a `█` inside each cap for a cell of padding. The cursor's
line and column share a background, so `:` joins them into one pill. Between
the pills, the file name stands out in bold `white`, the branch follows a
branch icon (`\uE0A0`), and `+` and `-` mark the Git counts:

```yaml tab
editor:
  status_bar:
    background_attr: {bg: gray}
    layout: "\
      \uE0B6█{{ .Status | fg \"black\" | bg \"yellow\" | bold }}█\uE0B4  \
      {{ .Filepath | fg \"white\" | bold }}  \
      \uE0A0 {{ .GitShortRef | fg \"teal\" | italic }}  \
      +{{ .GitDiffAdd | fg \"green\" }}  \
      -{{ .GitDiffDel | fg \"magenta\" }}\
      {{ .ShiftRight }}{{ .Language | fg \"teal\" }}  \
      \uE0B6█{{ .CursorLine | fg \"black\" | bg \"teal\" | bold }}\
      :{{ .CursorColumn | fg \"black\" | bg \"teal\" | bold }}█\uE0B4"
```

```python tab
config["editor"]["status_bar"]["background_attr"] = {"bg": "gray"}
config["editor"]["status_bar"]["layout"] = (
    '\uE0B6█{{ .Status | fg "black" | bg "yellow" | bold }}█\uE0B4  ' +
    '{{ .Filepath | fg "white" | bold }}  ' +
    '\uE0A0 {{ .GitShortRef | fg "teal" | italic }}  ' +
    '+{{ .GitDiffAdd | fg "green" }}  ' +
    '-{{ .GitDiffDel | fg "magenta" }}' +
    '{{ .ShiftRight }}{{ .Language | fg "teal" }}  ' +
    '\uE0B6█{{ .CursorLine | fg "black" | bg "teal" | bold }}' +
    ':{{ .CursorColumn | fg "black" | bg "teal" | bold }}█\uE0B4'
)
```

<Shot name="sbar-custom" alt="A gray status bar with NORMAL in a rounded yellow pill, main.go in bold, a branch icon and main in teal italics, +2 in green and -2 in pink, and on the right go in teal and 12:1 in a rounded teal pill" />

The [Vim](../learn/vim-editor.md) and [Helix](../learn/helix-editor.md) editors color
`{{ .Status }}` by mode, over the background in your layout: Vim's INSERT is
green, VISUAL blue, REPLACE yellow and DELETE red, for example. NORMAL keeps
your layout's colors.

<Shot name="sbar-insert" alt="The default status bar in insert mode, with INSERT on green">The default layout in insert mode.</Shot>

<Shot name="sbar-custom-insert" alt="The pill layout in insert mode, with INSERT in a rounded green pill">The pill layout in insert mode: the caps take the mode's color too.</Shot>

## Gutter

The gutter on the left of an editor shows line numbers, folds and Git
changes. Its keys live under `editor.aux_bar`:

| Key | Styles |
| --- | --- |
| `lines` | `absolute`, `relative` or `disabled`. |
| `git` | `inline` colors changed line numbers, `bar` marks them in a column of their own, `all` does both and `disabled` neither. |
| `line_number_attr` | Line numbers. |
| `highlight_cursor_attr` | The cursor's line number. |
| `git_add_inline_attr`, `git_del_inline_attr` | Added lines, and lines next to deleted ones. |

<Shot name="gutter-default" alt="Absolute line numbers with line 13 in green, line 15 in red and line 12 highlighted under the cursor">The default: line 13 was added and line 15 sits where a line was deleted.</Shot>

```yaml tab
editor:
  aux_bar:
    lines: relative
```

```python tab
config["editor"]["aux_bar"]["lines"] = "relative"
```

<Shot name="gutter-relative" alt="Line numbers counting up and down from the cursor's line" />

```yaml tab
editor:
  aux_bar:
    git: all
```

```python tab
config["editor"]["aux_bar"]["git"] = "all"
```

<Shot name="gutter-git" alt="The same gutter with a column of change marks left of the numbers" />

```yaml tab
editor:
  aux_bar:
    line_number_attr: {fg: silver}
    highlight_cursor_attr: {fg: black, bg: yellow, flags: bold}
    git_add_inline_attr: {fg: teal, flags: bold}
    git_del_inline_attr: {fg: magenta, flags: bold}
```

```python tab
config["editor"]["aux_bar"]["line_number_attr"] = {"fg": "silver"}
config["editor"]["aux_bar"]["highlight_cursor_attr"] = {"fg": "black", "bg": "yellow", "flags": "bold"}
config["editor"]["aux_bar"]["git_add_inline_attr"] = {"fg": "teal", "flags": "bold"}
config["editor"]["aux_bar"]["git_del_inline_attr"] = {"fg": "magenta", "flags": "bold"}
```

<Shot name="gutter-attrs" alt="Silver line numbers, the cursor line on yellow, an added line in teal and a deleted one in magenta" />

## End-of-file hatch

In the GUI, the rows past the end of a file are hatched with faint diagonal
stripes. `editor.eof_hatch.color` sets their color, drawn at low opacity, and
`period` the distance between stripes, in line heights:

<Shot name="hatch-default" alt="Faint gray diagonal stripes below the last line of a file">The default: `silver`, every `0.5` lines.</Shot>

```yaml tab
editor:
  eof_hatch:
    color: yellow
    period: 0.25
```

```python tab
config["editor"]["eof_hatch"] = {"color": "yellow", "period": 0.25}
```

<Shot name="hatch-custom" alt="Denser yellow diagonal stripes below the last line" />

## Search matches

Each editor colors search matches with its own `search_attr`:
`editor.vim.search_attr`, `editor.helix.search_attr`,
`editor.emacs.search_attr` or `editor.standard.search_attr`. The standard
editor's find bar has more keys under `editor.standard.search`.

<Shot name="search-default" alt="Every Stages in a file highlighted on yellow after searching for it">The default, after `/Stages` in the Vim editor.</Shot>

```yaml tab
editor:
  vim:
    search_attr: {fg: black, bg: teal, flags: bold}
```

```python tab
config["editor"]["vim"]["search_attr"] = {"fg": "black", "bg": "teal", "flags": "bold"}
```

<Shot name="search-custom" alt="The same matches in bold black on teal" />

## Command prompt

The [command prompt](../learn/command-prompt.md) floats over the workspace with a
list of suggestions and, below a separator, the manual of the selected
command. Its keys live under `command`:

| Key | Styles | Applies with |
| --- | --- | --- |
| `element_attr` | The suggestions. | `workspacereload` |
| `matched_text_attr` | The letters of each suggestion that match what you typed. | `workspacereload` |
| `focus_element_attr` | The suggestion you moved to in the list. | `workspacereload` |
| `frame_charset` | The prompt's frame, in place of the window frame. Slots you leave out keep the window's. | `themereload` |
| `separator_charset` | The `left` and `right` ends of the separator above the manual, and optionally `horizontal_left` and `horizontal_right`. | `themereload` |
| `show_manual` | `false` hides the manual. | `themereload` |

The prompt's frame takes its color from `browser.window_manager.frame_attr`.

<Shot name="cmd-default" alt="The command prompt with win typed, matching commands listed with win in red and the manual of windowconverttab below">The default, with the third suggestion selected.</Shot>

```yaml tab
command:
  element_attr: {fg: silver}
  matched_text_attr: {fg: yellow, flags: [bold, underline]}
  focus_element_attr: {fg: black, bg: yellow}
  frame_charset:
    topleft: "╭"
    topright: "╮"
    bottomleft: "╰"
    bottomright: "╯"
    horizontaltop: "─"
    horizontalbottom: "─"
    verticalleft: "│"
    verticalright: "│"
  separator_charset: {left: "├", right: "┤"}
```

```python tab
config["command"]["element_attr"] = {"fg": "silver"}
config["command"]["matched_text_attr"] = {"fg": "yellow", "flags": ["bold", "underline"]}
config["command"]["focus_element_attr"] = {"fg": "black", "bg": "yellow"}
config["command"]["frame_charset"] = {
    "topleft": "╭", "topright": "╮", "bottomleft": "╰", "bottomright": "╯",
    "horizontaltop": "─", "horizontalbottom": "─",
    "verticalleft": "│", "verticalright": "│",
}
config["command"]["separator_charset"] = {"left": "├", "right": "┤"}
```

<Shot name="cmd-custom" alt="The same prompt with a rounded frame, silver suggestions, underlined yellow matches and the selected suggestion on a yellow bar" />

```yaml tab
command:
  show_manual: false
```

```python tab
config["command"]["show_manual"] = False
```

<Shot name="cmd-nomanual" alt="A smaller command prompt listing only the suggestions" />

## Dialogs

When Rune asks you a question, such as whether to discard unsaved changes, it
opens a dialog with a row of buttons. Its keys live under `browser.prompt`:

| Key | Styles |
| --- | --- |
| `text_attr` | The buttons not selected. |
| `highlight_attr` | The selected button. |
| `hover_attr` | A button under the pointer. |
| `clicked_attr` | A button while you click it. |
| `background_attr` | The dialog around its message and buttons. |

The message itself is drawn on the theme's background, whatever
`background_attr` says, so a `background_attr` far from it leaves the
message on a band of its own. A dialog already open keeps its style until it
is opened again.

<Shot name="dialog-default" alt="A dialog asking to discard changes to README.md, with Yes on red and No on gray">The default, from `tabclose` on a file with unsaved changes.</Shot>

```yaml tab
browser:
  prompt:
    text_attr: {fg: silver, bg: gray}
    highlight_attr: {fg: black, bg: yellow, flags: bold}
```

```python tab
config["browser"]["prompt"]["text_attr"] = {"fg": "silver", "bg": "gray"}
config["browser"]["prompt"]["highlight_attr"] = {"fg": "black", "bg": "yellow", "flags": "bold"}
```

<Shot name="dialog-custom" alt="The same dialog with Yes in bold black on yellow and No in silver on gray" />

In the GUI, `browser.prompt.images` draws the buttons with images. See
[Dialog buttons](#dialog-buttons) in Graphical themes.

## Notifications

Notifications stack in the top-right corner. Their color follows their level
(info, success, warning or error), which you cannot change, but the rest of
their style lives under `notifications`:

| Key | Styles |
| --- | --- |
| `attr` | The message. |
| `background_attr` | The background. |
| `frame_charset` | The frame, as for [windows](#window-frames). |
| `padding` | The space, in cells, from the edges of the screen and between notifications. The default is `1`. |
| `progress_bar` | `false` removes the bar along the bottom that counts down to `auto_close`. |
| `progress_format` | The characters of the progress bar: `start`, `current`, `current_tip`, `remain` and `end`. |

Notifications already shown take a new style at once. You can try yours with
`notificationinfo`, `notificationsuccess`, `notificationwarning` and
`notificationerror`, each followed by a message.

<Shot name="notify-default" alt="Four notifications stacked in the top-right corner, with gray, green, yellow and red frames and progress bars">The default.</Shot>

```yaml tab
notifications:
  background_attr: {bg: gray}
  attr: {fg: white, flags: bold}
  padding: 2
```

```python tab
config["notifications"]["background_attr"] = {"bg": "gray"}
config["notifications"]["attr"] = {"fg": "white", "flags": "bold"}
config["notifications"]["padding"] = 2
```

<Shot name="notify-custom" alt="The same notifications on a gray background, in bold white, spaced further apart" />

The progress bar runs along the bottom edge of the frame, in the cells of
`horizontalbottom`. Box-drawing characters in a custom `frame_charset` don't
line up with it, so change the characters of the frame together with
`progress_bar: false`, or draw both with images, as
[Violet Ledger](#graphical-notifications) does.

```yaml tab
notifications:
  progress_bar: false
```

```python tab
config["notifications"]["progress_bar"] = False
```

<Shot name="notify-noprogress" alt="The same notifications with plain gray frames and no progress bars">Without the progress bar, the frames no longer show the level's color.</Shot>

## Terminal program bar

A program run with `!` gets a bar of its own. `terminal.plugin.bar_layout` is
a template like the [editor status bar](#editor-status-bar)'s, with these
components:

| Component | Shows |
| --- | --- |
| `{{ .StatusIcon }}` | A spinner while the program runs, then a success or error icon. |
| `{{ .Command }}` | The command. |
| `{{ .ExitStatus }}` | How the program exited. |
| `{{ .Elapsed }}` | How long it ran. |
| `{{ .AlignCenter }}`, `{{ .AlignRight }}` | Everything after it is centered, or aligned to the right. |

`status_success_icon`, `status_success_attr`, `status_error_icon` and
`status_error_attr` style the icon, `animation` lists the frames of its
spinner, and `bar_background_attr` styles the bar.

<Shot name="pbar-default" alt="A program bar with a green dot on the left and 3.042s on the right">The default, after `sh scripts/countdown.sh` finished.</Shot>

```yaml tab
terminal:
  plugin:
    bar_layout: ' {{ .StatusIcon }} {{ .Command | bold }}{{ .AlignRight }}{{ .ExitStatus | fg "green" }}  {{ .Elapsed | italic }} '
```

```python tab
config["terminal"]["plugin"]["bar_layout"] = ' {{ .StatusIcon }} {{ .Command | bold }}{{ .AlignRight }}{{ .ExitStatus | fg "green" }}  {{ .Elapsed | italic }} '
```

<Shot name="pbar-custom" alt="A plain program bar with the green dot on the left and ok 3.049s in green and italics on the right" />

## Rune Agent

[Rune Agent](../agent/intro.md)'s chats have a style of their own, set under
`extensions.rune-agent.config`.

<Shot name="agent-idle" alt="A chat with a question on a gray bar, a read_file tool call, an answer with a Go code block, an empty composer and a status bar reading IDLE">The defaults, after a turn: your message on `gray`, the tool call the agent made, its answer, the composer and the status bar.</Shot>

### Messages and composer

These keys also style the chats of [ACP agents](../learn/acp.md).

| Key | Styles |
| --- | --- |
| `background_attr` | The chat's background. |
| `user_msg_attr` | Your messages. Dim by default. |
| `user_msg_background_attr` | The background behind your messages. `gray` by default. |
| `user_msg_icon` | The character left of each of your messages. `""` hides it. |
| `assistant_msg_attr` | The agent's messages. |
| `transcript_horizontal_padding` | The columns beside the transcript. A negative number sets its width instead: the default, `-90`, keeps it 90 columns wide and centered. `0` fills the tab. |
| `input_box_attr` | The text you type in the composer. |
| `input_box_placeholder_attr` | The composer's placeholder. `red` by default. |
| `input_box_frame_attr` | The composer's frame. |
| `input_box_frame_charset` | The characters of the composer's frame, as for [windows](#window-frames). |
| `input_background_color` | The composer's background color. |
| `completion_element_attr`, `completion_matched_text_attr`, `completion_focus_element_attr` | The list of `#` completions, as the command prompt's keys do. |
| `completion_radar_color` | The sweep over the composer while `#` completions load. |
| `inline_attachment_attr` | A file or symbol inserted into your message from `#` completion. |

The reasoning, the tool calls and the markdown of the agent's answers have
no keys of their own: they follow the theme.

```yaml tab
extensions:
  rune-agent:
    config:
      user_msg_attr: {fg: white}
      user_msg_background_attr: {bg: navy}
      user_msg_icon: "❯"
      input_box_frame_attr: {fg: yellow}
      input_box_placeholder_attr: {fg: silver}
      input_box_frame_charset:
        topleft: "╭"
        topright: "╮"
        bottomleft: "╰"
        bottomright: "╯"
        horizontaltop: "─"
        horizontalbottom: "─"
        verticalleft: "│"
        verticalright: "│"
```

```python tab
agent = config.setdefault("extensions", {}).setdefault("rune-agent", {}).setdefault("config", {})
agent["user_msg_attr"] = {"fg": "white"}
agent["user_msg_background_attr"] = {"bg": "navy"}
agent["user_msg_icon"] = "❯"
agent["input_box_frame_attr"] = {"fg": "yellow"}
agent["input_box_placeholder_attr"] = {"fg": "silver"}
agent["input_box_frame_charset"] = {
    "topleft": "╭", "topright": "╮", "bottomleft": "╰", "bottomright": "╯",
    "horizontaltop": "─", "horizontalbottom": "─",
    "verticalleft": "│", "verticalright": "│",
}
```

<Shot name="agent-chat-idle" alt="The same chat with the question in white on dark red behind a chevron, and a rounded yellow composer">Your message in `white` on `navy`, which `romero` draws dark red, and a rounded `yellow` composer.</Shot>

### Status bar

Every chat ends in a status bar that follows the turn through its phases:
`SENDING`, `REASONING`, `RECEIVING`, `EXECUTING`, `COMPACTING`, `ASKING`,
`ERROR` and, between turns, `IDLE`. This is the default bar in each of them:

<Shot link name="agent-state-idle" alt="Rune Agent status bar at IDLE, showing gpt-6-sol, xhigh effort, 96% cache usage and 229k of 872k context tokens">`IDLE`: the conversation's model and context stay in view.</Shot>

<Shot link name="agent-state-sending" alt="Rune Agent status bar in cyan SENDING state, showing claude-fable-5-1, xhigh effort, conversation solid-platypus, and 690k of 1m context tokens">`SENDING`: the agent is submitting a request.</Shot>

<Shot link name="agent-state-reasoning" alt="Rune Agent status bar in red REASONING state with the model, effort, conversation and context gauge">`REASONING`: the model is thinking through its next step.</Shot>

<Shot link name="agent-state-receiving" alt="Rune Agent status bar in pink RECEIVING state with elapsed time and a context gauge">`RECEIVING`: the answer is streaming in.</Shot>

<Shot link name="agent-state-executing" alt="Rune Agent status bar in red EXECUTING state showing claude-fable-5-1, xhigh effort, 94% cache usage, 1h 55m elapsed, conversation solid-platypus, and 265k of 1m context tokens">`EXECUTING`: the agent is running tools.</Shot>

<Shot link name="agent-state-asking" alt="Rune Agent status bar in amber ASKING state, showing claude-opus-5-5, xhigh effort, 86% cache usage, conversation github-92, and 535k of 1m context tokens">`ASKING`: the agent is waiting for your answer.</Shot>

`COMPACTING` shows while the conversation is compacted, by `/compact` or on
its own during a turn, and `ERROR` while the provider rate-limits the turn.

Its keys live under `status_bar`:

| Key | Styles |
| --- | --- |
| `layout` | What the bar shows, as a template like the [editor's](#editor-status-bar): `{{ .Spinner }}`, `{{ .Status }}`, `{{ .Model }}`, `{{ .Effort }}`, `{{ .Speed }}`, `{{ .Conversation }}`, `{{ .Elapsed }}`, `{{ .Cache }}`, `{{ .ContextGauge }}`, `{{ .CacheGauge }}`, `{{ .Image }}` and more. |
| `background_attr` | The bar. Its `fg` is the color of every element that names none. |
| `status.<PHASE>.attr` | The spinner and the status in that phase. |
| `status.<PHASE>.animation` | The spinner in that phase: `ch` lists its frames, one character each, and `attr` colors it alone. |
| `gauge_width`, `gauge_start_rune`, `gauge_end_rune`, `gauge_cap_attr`, `gauge_empty_attr` | The gauges: their width, their brackets, and their empty track. |
| `context_gauge_fill_attrs`, `cache_gauge_fill_attrs` | The colors each gauge's fill ramps through, from left to right. |
| `enabled` | `false` removes the bar. |

Naming one phase, or one key of it, keeps the defaults of the rest.
[Rune Agent configuration](../agent/config.md#status-bar) lists every element
and its defaults.

```yaml tab
extensions:
  rune-agent:
    config:
      status_bar:
        background_attr: {fg: silver, bg: black}
        gauge_width: 14
        gauge_start_rune: "["
        gauge_end_rune: "]"
        gauge_cap_attr: {fg: silver}
        gauge_empty_attr: {fg: silver, bg: gray}
        context_gauge_fill_attrs:
          - {fg: black, bg: teal}
          - {fg: black, bg: yellow}
          - {fg: white, bg: red}
        status:
          IDLE: {attr: {fg: black, bg: green, flags: bold}, animation: {ch: "●"}}
          REASONING: {attr: {fg: black, bg: yellow, flags: bold}, animation: {ch: "◐◓◑◒"}}
          RECEIVING: {attr: {fg: black, bg: teal, flags: bold}}
        layout: '{{ .Spinner }} {{ .Status }} █▓▒░  {{ .Model | fg "teal" }}{{ .ShiftRight }}{{ .Elapsed | italic }}  {{ .ContextGauge }} '
```

```python tab
agent = config.setdefault("extensions", {}).setdefault("rune-agent", {}).setdefault("config", {})
agent["status_bar"] = {
    "background_attr": {"fg": "silver", "bg": "black"},
    "gauge_width": 14,
    "gauge_start_rune": "[",
    "gauge_end_rune": "]",
    "gauge_cap_attr": {"fg": "silver"},
    "gauge_empty_attr": {"fg": "silver", "bg": "gray"},
    "context_gauge_fill_attrs": [
        {"fg": "black", "bg": "teal"},
        {"fg": "black", "bg": "yellow"},
        {"fg": "white", "bg": "red"},
    ],
    "status": {
        "IDLE": {"attr": {"fg": "black", "bg": "green", "flags": "bold"}, "animation": {"ch": "●"}},
        "REASONING": {"attr": {"fg": "black", "bg": "yellow", "flags": "bold"}, "animation": {"ch": "◐◓◑◒"}},
        "RECEIVING": {"attr": {"fg": "black", "bg": "teal", "flags": "bold"}},
    },
    "layout": '{{ .Spinner }} {{ .Status }} █▓▒░  {{ .Model | fg "teal" }}{{ .ShiftRight }}{{ .Elapsed | italic }}  {{ .ContextGauge }} ',
}
```

<Shot name="agent-sbar-reasoning-bar" alt="A black bar with REASONING on yellow fading out, the model in teal, the elapsed time and a bracketed context gauge">`REASONING`, with a spinning half-moon.</Shot>

<Shot name="agent-sbar-receiving-bar" alt="The same bar with RECEIVING on teal and the context gauge filling with teal">`RECEIVING`. The gauge fills from `teal` towards `red`.</Shot>

<Shot name="agent-sbar-idle-bar" alt="The same bar with IDLE on green and the filled context gauge">`IDLE`.</Shot>

`status_bar.shader` animates the bar while a turn runs: `blaze`, `burn`,
`inferno` and `shine` recolor its text, and `pulse` brightens the whole bar.
`shader_fps` sets how often it redraws, `30` by default, and `shader_loop` how
long one loop lasts, `1200ms` by default:

```yaml tab
extensions:
  rune-agent:
    config:
      status_bar:
        shader: blaze
```

```python tab
agent = config.setdefault("extensions", {}).setdefault("rune-agent", {}).setdefault("config", {})
agent.setdefault("status_bar", {})["shader"] = "blaze"
```

<Shot name="anim-agent-shader" alt="The left half of the agent status bar with flames running through REASONING, the model and the effort">`blaze` while the model reasons.</Shot>

The composer's frame and the status bar also take images. See
[Rune Agent](#graphical-rune-agent) in Graphical themes.

### Applying agent styles

`themereload` and `runectl screenshot` apply `status_bar`,
`input_box_frame_charset` and its images, `input_box_attr`,
`input_box_frame_attr` and `input_background_color` to the chats already
open. The other keys apply after `workspacereload`.

## Animations

Rune animates two things:

- **Cell shaders** animate the characters and colors of the cells on screen.
  They play in the GUI and when Rune runs in a terminal.
- **The cursor effect** draws over the pixels of the window when the cursor
  jumps. It plays in the GUI only.

Both are set under `animations`, and Rune reads them when it starts, so
restart it after changing them. [Animations](./animations.md) has every key
and shader in detail.

### Cell shaders

`shaderrun <name> [<duration>] [<fps>]` plays any of Rune's 31 cell shaders
over the screen. Here is each one over the same two windows, with the
command that played it. Click one to open it on its own.

**Reveals** uncover the screen:

<Gallery>
<Shot link name="shader-beams" alt="Beams of light sweeping along the rows and columns, uncovering dimmed text, then a diagonal wipe restoring its colors">`shaderrun beams 3s`</Shot>
<Shot link name="shader-binaryPath" alt="The binary code of each character snaking in from outside the screen and collapsing into the character">`shaderrun binaryPath 3s`</Shot>
<Shot link name="shader-burn" alt="The screen catching fire character by character, the fire spreading from one cell">`shaderrun burn 3s`</Shot>
<Shot link name="shader-decrypt" alt="The text typed out as ciphertext, then decrypted character by character">`shaderrun decrypt 2s`</Shot>
<Shot link name="shader-expand" alt="The text bursting out of the center of the screen, each character traveling to its place">`shaderrun expand 2s`</Shot>
<Shot link name="shader-matrix" alt="Green streaks of symbols raining down the columns, then resolving cell by cell into the text">`shaderrun matrix 3s`</Shot>
<Shot link name="shader-middleOut" alt="The text spread along the middle row, then each character moving up or down to its place">`shaderrun middleOut 2s`</Shot>
<Shot link name="shader-pour" alt="The text poured in from one edge, line by line">`shaderrun pour 2s`</Shot>
<Shot link name="shader-print" alt="The text printed row by row like a line printer, scrolling up as it goes">`shaderrun print 2s`</Shot>
<Shot link name="shader-rain" alt="The characters raining down from the top, row by row">`shaderrun rain 2s`</Shot>
<Shot link name="shader-slide" alt="Each line sliding in from an edge, one character after another">`shaderrun slide 2s`</Shot>
</Gallery>

**Fires** burn over everything:

<Gallery>
<Shot link name="shader-blaze" alt="A fire rising over both windows">`shaderrun blaze 3s`</Shot>
<Shot link name="shader-blazeBlue" alt="The same fire with blue flames">`shaderrun blazeBlue 3s`</Shot>
<Shot link name="shader-embers" alt="Embers burning in ring-like blobs and turning to ash under glittering sparks">`shaderrun embers 3s`</Shot>
<Shot link name="shader-flames" alt="Flames crossing the screen from bottom to top">`shaderrun flames 3s`</Shot>
<Shot link name="shader-incendium" alt="Flames and embers together, the rising sparks dragging characters along">`shaderrun incendium 3s`</Shot>
<Shot link name="shader-inferno" alt="Another fire rising over both windows">`shaderrun inferno 3s`</Shot>
<Shot link name="shader-infernoBlue" alt="The inferno fire with blue flames">`shaderrun infernoBlue 3s`</Shot>
</Gallery>

**Over the text**, shaders play with the characters on screen:

<Gallery>
<Shot link name="shader-bomb" alt="A ring expanding from the center, pushing the characters under it outward like a magnifying glass">`shaderrun bomb 2s`</Shot>
<Shot link name="shader-crumble" alt="The characters crumbling to dust and falling, then dropping back into place">`shaderrun crumble 4s`</Shot>
<Shot link name="shader-rings" alt="The characters gathered into concentric rings that spin, scatter and condense, then return home">`shaderrun rings 4s`</Shot>
<Shot link name="shader-spotlights" alt="The screen dimmed with spotlights sweeping over it">`shaderrun spotlights 3s`</Shot>
<Shot link name="shader-thunderstorm" alt="The windows darkened under rain, with lightning lighting up the text">`shaderrun thunderstorm 4s`</Shot>
<Shot link name="shader-unstable" alt="The characters shuffled, rumbling and heating up until they blow apart, then reassembling">`shaderrun unstable 4s`</Shot>
<Shot link name="shader-vhsTape" alt="The screen played back like a worn tape, with tearing, glitches and snow">`shaderrun vhsTape 3s`</Shot>
</Gallery>

**Color and frames** shaders recolor the screen, or only the frames of
windows:

<Gallery>
<Shot link name="shader-grayFade" alt="The colors of the screen fading to gray">`shaderrun grayFade 2s`</Shot>
<Shot link name="shader-pulse" alt="Every cell pulsing toward a color and back">`shaderrun pulse 2s`</Shot>
<Shot link name="shader-shine" alt="A band of light sweeping diagonally across the screen">`shaderrun shine 2s`</Shot>
<Shot link name="shader-shineFrame" alt="A band of light sweeping across the frames of the windows only">`shaderrun shineFrame 2s`</Shot>
<Shot link name="shader-radarFrame" alt="A radar sweep circling the frames of the windows">`shaderrun radarFrame 2s`</Shot>
</Gallery>

The 31st, `nop`, draws nothing. `shaderrun intro` replays the intro of the
built-in logo. Rune plays the shaders in these places:

| Key | Plays |
| --- | --- |
| `animations.intro` | The intro of the built-in logo, when Rune starts. |
| `animations.loading_workspace`, `animations.open_workspace` | A gray fade while a workspace loads, then a reveal once it has. `open_workspace` takes a `shader` and a `duration`. |
| `animations.command_prompt` | A radar sweep round the command prompt's frame. |
| `animations.active_content_tab`, `animations.active_workspace_tab` | A shader over tabs whose program is [working](../learn/terminal/program-status.md). |
| `animations.screensaver` | Shaders that take turns once Rune has had no input for a while. |
| `extensions.rune-agent.config.status_bar.shader` | A shader over [Rune Agent's status bar](#status-bar) while a turn runs. |

All but the agent's are on by default. Set one to `false`, or set
`enabled: false` in its section, to turn it off.

#### Over the command prompt

By default, a red sweep covers a third of the frame and goes round once.
`color`, `angular_width` and `cycles` change that:

```yaml tab
animations:
  command_prompt:
    color: yellow
    angular_width: 0.5
    cycles: 2
```

```python tab
config["animations"]["command_prompt"]["color"] = "yellow"
config["animations"]["command_prompt"]["angular_width"] = 0.5
config["animations"]["command_prompt"]["cycles"] = 2
```

<Shot name="anim-prompt" alt="The command prompt opening, with a yellow sweep going round its frame twice" />

#### Over working tabs

While a program reports that it is working, such as a deploy script or an
agent, its tab plays a `shine` by default. The `shader` can also be `blaze`,
`burn`, `inferno` or `pulse`, and settings for one shader go under a key named
after it:

```yaml tab
animations:
  active_content_tab:
    shader: shine
    shine:
      color: yellow
      intensity: 0.9
      band_width: 0.4
```

```python tab
config["animations"]["active_content_tab"]["shader"] = "shine"
config["animations"]["active_content_tab"]["shine"] = {
    "color": "yellow",
    "intensity": 0.9,
    "band_width": 0.4,
}
```

<Shot name="anim-tab-shine" alt="A terminal tab whose label a yellow band of light sweeps across while its script runs" />

`loop` sets how long one loop lasts. The flames of `blaze` take a few dozen
loops to come round, so a short loop makes them faster:

```yaml tab
animations:
  active_content_tab:
    shader: blaze
    fps: 20
    loop: 300ms
```

```python tab
config["animations"]["active_content_tab"]["shader"] = "blaze"
config["animations"]["active_content_tab"]["fps"] = 20
config["animations"]["active_content_tab"]["loop"] = "300ms"
```

<Shot name="anim-tab-blaze" alt="The same terminal tab with flames running through its label" />

`animations.active_workspace_tab` takes the same keys for the workspace bar,
where a green `shine` plays by default.

#### Other things that move

| Key | Animates |
| --- | --- |
| `terminal.status.spinner`, `terminal.status.spinner_fps` | The spinner by the icon of a working tab: its frames, one character each, and how fast it turns. |
| `terminal.plugin.animation` | The spinner of the `!` [program bar](#terminal-program-bar). |
| `extensions.rune-agent.config.status_bar.status.<PHASE>.animation.ch` | The spinner of [Rune Agent's status bar](#status-bar). |
| `terminal.needs_attention_attr` | The tab of a terminal that rang its bell while Rune was in the background. Its default `blink` flag makes it blink. |

### Cursor effect

`animations.cursor` plays a shader over the window each time the cursor jumps,
such as to another window or another part of the file. Typing plays nothing.
It's off by default. `true` plays a flame that trails the cursor from where it
jumped:

```yaml tab
animations:
  cursor: true
```

```python tab
config["animations"]["cursor"] = True
```

<Shot name="cursor-fire-trail" alt="A flame stretching from where the cursor was to where it jumped, its tail burning down into the cursor">The built-in flame trail.</Shot>

The [cursor pack](https://github.com/unstablebuild/rune-cursor-pack) adds
four more. To install it, run this in the [console](../learn/console.md), then
restart Rune:

```text
pkg install github.com/unstablebuild/rune-cursor-pack
```

The install turns the effect on with the lightning color shader, unless your
config already sets `animations.cursor`. To switch, set `shader` to a path
below and `duration` to its suggested value, then restart Rune:

```yaml tab
animations:
  cursor:
    enabled: true
    shader: $RUNE_DATADIR/lib/github.com/unstablebuild/rune-cursor-pack/shaders/smear.kage
    duration: 300ms
```

```python tab
config["animations"]["cursor"] = {
    "enabled": True,
    "shader": "$RUNE_DATADIR/lib/github.com/unstablebuild/rune-cursor-pack/shaders/smear.kage",
    "duration": "300ms",
}
```

| Shader | Path under `$RUNE_DATADIR/lib/github.com/unstablebuild/rune-cursor-pack/shaders/` | Duration |
| --- | --- | --- |
| Lightning color, the one the install sets | `lightning_color.kage` | `350ms` |
| Lightning | `lightning.kage` | `350ms` |
| Fire trail, the built-in flame | `fire-trail.kage` | `250ms` |
| Fire | `fire.kage` | `800ms` |
| Smear | `smear.kage` | `300ms` |

<Gallery>
<Shot name="cursor-lightning" alt="A forked bolt of lightning striking from where the cursor was to where it jumped">Lightning. Lightning color takes the theme's cursor color for its glow.</Shot>
<Shot name="cursor-fire" alt="A fuse burning to the cursor, which bursts into flames that lick over the lines above">Fire.</Shot>
<Shot name="cursor-smear" alt="The cursor smearing between its old and new positions in its own color">Smear.</Shot>
</Gallery>

Cursor shaders are written in [Kage](https://ebitengine.org/en/documents/shader.html).
[Write your own](./animations.md#write-your-own) lists what Rune gives them
and how they decide what counts as a jump.

## More

- [Themes](./themes.md): the palette behind every color name.
- [Syntax Highlighting](./syntax-highlighting.md): every capture name and its
  default.
- [Animations](./animations.md): every shader, and how to write a cursor
  shader.
- [Wallpaper](./wallpaper.md): what empty windows show.
- [Sounds](./sounds.md): what the interface sounds like.
- [Rune Agent configuration](../agent/config.md): the agent's chat and status
  bar, in full.
- [Program status](../learn/terminal/program-status.md): the inbox and the status of
  tabs.
