# Key combination syntax

Wherever Rune accepts a key combination (`bindings` in `rune.star`, the `goto`
template for the [exoeditor](./exoeditor.md), key sequences
sent into a vte, or error messages from a misconfigured binding) the same
grammar is used. Use this page as the single reference for what you can write.

## At a glance

- Plain printable characters stand for themselves: `a`, `A`, `1`, `?`.
- Named keys, modifier-decorated keys, and mouse events go inside angle brackets: `<enter>`, `<ctrl-space>`, `<shift-up>`, `<mouse-left>`.
- Modifier names are lowercase and may be spelled out (`ctrl`, `shift`, `alt`, `meta`) or written as a single-letter alias (`c`, `s`, `a`, `m`).
- When combining modifiers, **the order is fixed**; see [Modifier order](#modifier-order). `<shift-ctrl-x>` is rejected; `<ctrl-shift-x>` is accepted.
- Multiple keystrokes are concatenated: `<esc>:wq<enter>` is four keys.
- The literal characters `<`, `>`, and `\` must be escaped with a backslash: `\<`, `\>`, `\\`.
- A literal space is `<space>`; a bare ` ` inside a key sequence is rejected.

## Plain characters

Any single printable character (other than `<`, `>`, `\`, and space) is itself a
key combination. Case matters:

```
a       # the letter a
A       # shift-a, as a single key
1       # the digit 1
?       # the question mark
```

Concatenate characters to describe a sequence:

```
gg      # press g, then g again
:wq     # colon, w, q
```

## Named keys

Named keys are bracketed, lowercase, and have no modifier prefix:

| Name | Key |
| --- | --- |
| `<enter>` | Return / Enter |
| `<esc>` | Escape |
| `<tab>` | Tab |
| `<space>` | Space bar |
| `<backspace>` | Backspace |
| `<insert>`, `<delete>` | Insert, Delete |
| `<home>`, `<end>` | Home, End |
| `<pgup>`, `<pgdn>` | Page Up, Page Down |
| `<up>`, `<down>`, `<left>`, `<right>` | Arrow keys |
| `<f1>` … `<f12>` | Function keys |

## Mouse events

Mouse events use the same bracket-and-modifier grammar:

| Name | Event |
| --- | --- |
| `<mouse-left>` | Primary button press |
| `<mouse-middle>` | Middle button press |
| `<mouse-right>` | Secondary button press |
| `<mouse-release>` | Button release |
| `<mouse-wheel-up>` | Wheel scroll up |
| `<mouse-wheel-down>` | Wheel scroll down |

Modifiers attach the same way as for keys: `<ctrl-mouse-left>`,
`<shift-mouse-wheel-up>`, `<alt-mouse-right>`.

## Modifiers

Prefix a bracketed key with one or more modifier names:

```
<ctrl-...>
<shift-...>
<alt-...>
<meta-...>
```

Notes on individual modifiers:

- **`ctrl`** combines with letters, digits, function keys, arrows, mouse events, and most named keys (e.g. `<ctrl-a>`, `<ctrl-_>`, `<ctrl-space>`, `<ctrl-pgdn>`).
- **`shift`** is meaningful on non-letter keys where Rune can detect it (`<shift-tab>`, `<shift-up>`, `<shift-f5>`). For plain letters, prefer the uppercase character (`A`) over `<shift-a>`.
<Platform when="darwin">

- **`alt`** is Option.
- **`meta`** is Command.

</Platform>
<Platform when="linux">

- **`alt`** is Alt.
- **`meta`** is Super. The shipped presets bind nothing to it, because Super
  belongs to the desktop; see
  [Why Alt on Linux](./key-mapping.md#why-alt-on-linux).

Rune recognizes exactly four modifiers: `ctrl`, `shift`, `alt`, and
`meta`. `meta` is the Super key, which is the key most keyboards label with the
Windows logo, so pressing Super registers as `meta`. This is expected: Rune has
no separate Super modifier, and a Super combination such as Super+P is written
`<meta-p>`.

If your keyboard layout defines the historical Meta or Hyper modifiers as
distinct keys (for example through xkb, with Hyper bound to Caps Lock), Rune
does not see them as their own modifiers today. It reads the four modifiers
above and nothing else, so a Hyper or dedicated-Meta press does not register as
a modifier and does not show up when you inspect keys with the `keydump`
command (see [Troubleshooting](../troubleshoot.md)). Tools that read the
compositor directly, such as `wev`, can still show those keys because they
observe a layer below the one Rune reads. To repurpose such a key inside Rune
in the meantime, remap it with [`gui.key_mapping`](./key-mapping.md).

</Platform>

### Short-form modifiers

Each modifier has a single-letter alias that is interchangeable with the
spelled-out form: `c` for `ctrl`, `s` for `shift`, `a` for `alt`, `m` for `meta`.
The alias rules apply in combinations too, so all of the following describe the
same key:

```
<ctrl-shift-x>
<c-shift-x>
<ctrl-s-x>
<c-s-x>
```

The spelled-out form is the canonical one and is what Rune emits when it
renders a key combination back to a string; pick that form in configuration
you write by hand so what you type matches what Rune prints in error messages
and log output.

### Modifier order

Modifiers must appear in the order Rune expects, not in the order you'd say
them out loud. The accepted combinations are exactly:

| Modifiers | Canonical form |
| --- | --- |
| ctrl + shift | `<ctrl-shift-…>` |
| ctrl + alt | `<ctrl-alt-…>` |
| ctrl + meta | `<ctrl-meta-…>` |
| ctrl + shift + alt | `<ctrl-shift-alt-…>` |
| ctrl + shift + meta | `<ctrl-shift-meta-…>` |
| ctrl + alt + meta | `<ctrl-alt-meta-…>` |
| shift + meta | `<shift-meta-…>` |
| alt + shift | `<alt-shift-…>` |
| alt + meta | `<alt-meta-…>` |
| alt + shift + meta | `<alt-shift-meta-…>` |

Anything else (`<shift-ctrl-x>`, `<meta-alt-up>`, `<alt-ctrl-f1>`) is rejected
at parse time, even though the meaning is unambiguous. There is no combination
that mixes all four modifiers (`ctrl + shift + alt + meta`).

## Sequences

A binding value is a sequence of key combinations, concatenated with no
separator. Each character or `<…>` block is one keystroke:

```
<ctrl-x><ctrl-s>           # press Ctrl-X, then Ctrl-S
<esc>:goto<space>42<enter> # esc, colon, g, o, t, o, space, 4, 2, enter
gg                         # press g twice
```

## Escaping

`<`, `>`, and `\` are the three reserved characters. Escape them with a
leading backslash to use them as literal keystrokes:

| Escape | Literal key |
| --- | --- |
| `\<` | `<` |
| `\>` | `>` |
| `\\` | `\` |

A bare space inside a sequence is rejected; write `<space>` instead. This
makes whitespace in a binding value purely cosmetic and prevents accidental
truncation when a sequence is copied across formats.

## Common mistakes

| Wrong | Correct | Why |
| --- | --- | --- |
| `Ctrl-Space` | `<ctrl-space>` | Bracketed, lowercase, no `+` |
| `Ctrl+S` | `<ctrl-s>` | Hyphen, not `+` |
| `<C-s>` | `<c-s>` or `<ctrl-s>` | Modifier names are lowercase (see [Short-form modifiers](#short-form-modifiers)) |
| `<A-x>` | `<a-x>` or `<alt-x>` | Modifier names are lowercase |
| `<Shift-Tab>` | `<shift-tab>` | All-lowercase inside brackets |
| `<ctrl- >` | `<ctrl-space>` | A literal space is not allowed |
| `<meta-cmd-k>` | `<meta-k>` | `meta` already covers Command/Super |
| `<shift-ctrl-x>` | `<ctrl-shift-x>` | Modifier order is fixed (see [Modifier order](#modifier-order)) |
| `<meta-alt-up>` | `<alt-meta-up>` | Modifier order is fixed |
| `<ctrl-shift-alt-meta-x>` |  | Four-modifier combinations are not accepted |

## See also

- [Key remapping](./key-mapping.md): use `gui.key_mapping` to rewrite one key
  combination into another (for example, CapsLock to Escape) using this same
  grammar.
