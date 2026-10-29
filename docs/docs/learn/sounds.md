---
sidebar_position: 48
sidebar_label: Sounds
---

# Sounds

Rune plays a short sound for events you might not be looking at, such as a
program in a terminal ringing the bell. Sounds have names: Rune ships four
built-in ones, and you and the packages you install can add more. Settings
that make a sound, such as `terminal.bell`, refer to a sound by its name.

## Built-in sounds

| Name | Plays when |
| --- | --- |
| `error` | something fails or is refused, such as a program reporting an error or a vi-mode edit above a terminal's prompt |
| `warning` | something needs caution, such as a program blocked waiting on you or, by default, ringing the bell |
| `info` | something informational happens |
| `success` | something you were waiting on succeeded, such as a program reporting it is done |

Rune plays them for the events above. Nothing plays `info` by default yet.

## Defining sounds

The `sounds` section maps names to sounds. Each value is one of:

- the path of a WAV, MP3 or Ogg Vorbis file;
- the name of another sound;
- an empty string, which silences the sound.

```yaml tab
sounds:
  ding: "$HOME/sounds/ding.wav"
  warning: ding
  info: ""
```

```python tab
config["sounds"] = {
    "ding": "$HOME/sounds/ding.wav",
    "warning": "ding",
    "info": "",
}
```

This defines a new sound, `ding`, makes the built-in `warning` sound play
it, and silences `info`.

- Use absolute paths or `file://` URIs. A path can reference environment
  variables such as `$HOME`.
- Rune plays up to the first 10 seconds of a file.
- If a file is missing or cannot be played, Rune logs a warning. A built-in
  sound pointed at it plays its built-in version instead.
- A name cannot contain `/`, since a value with `/` is read as a path.
- Rune reports a name that refers to an unknown sound, or to itself through
  other names, as a config error and leaves that entry out.

Sounds come from your config file and the packages you install; a
workspace's `.rune/config.yaml` cannot define them. Changes apply after
`workspacereload`, and sounds added by a package install are available right
away. To share sounds, publish them in a
[package](../develop/packages.md#registering-sounds).

To hear a sound, run `guisound` with its name:

```
guisound ding
```

## Sounds pack

The [sounds pack](https://github.com/unstablebuild/rune-sounds-pack) adds the
936 interface sounds of [UI SFX](https://uisfx.com): twelve packs, such as
`minimal`, `glass` and `arcade`, each with the same 78 cues. Each sound is
named `uisfx-<pack>-<cue>`, such as `uisfx-minimal-success`. To install it,
run this in the [console](./console.md):

```text
pkg install github.com/unstablebuild/rune-sounds-pack
```

The install makes each built-in sound play its minimal counterpart, such as
`uisfx-minimal-warning` for `warning`, and sets `terminal.bell` to
`uisfx-minimal-blocked`, unless your config already sets them: Rune keeps the
values you set. The pack's README lists every pack and cue.

## Terminal bell

`terminal.bell` names the sound a program in a terminal plays when it rings
the bell. It is `warning` by default. Set it to an empty string to keep
programs from making a sound; the tab of an unfocused terminal is still
marked when its program rings the bell.

```yaml tab
terminal:
  bell: ding
```

```python tab
config["terminal"]["bell"] = "ding"
```

Like other `terminal.*` settings, a change to `terminal.bell` applies after
`workspacereload`.

## Program status

Programs that report their status, such as Rune Agent, agent CLIs and
commands run in the IDE console, play a sound when they finish, fail or wait
on you. `program_status.sounds` maps each state to a sound; see
[Program status](./terminal/program-status.md#sounds).

## Quick menu

`gui.quick_menu_sounds` names the sounds the quick menu plays: `hover` when
the pointer moves onto a button and `click` when you click one. Each is the
name of a sound, built-in or your own, or an empty string for none, which is
the default.

```yaml tab
gui:
  quick_menu_sounds:
    hover: ding
    click: success
```

```python tab
config["gui"]["quick_menu_sounds"] = {"hover": "ding", "click": "success"}
```

- The quick menu plays sounds only when its buttons set a `sprite`. The
  native macOS quick menu plays none.
- Rune reports an unknown sound name as a config error, and that event plays
  nothing.
- Like other `gui.*` settings, a change applies after restarting Rune.

## Play a sound from an alias

To play a sound from an [alias](./aliases.md), add a `guisound` step. An
alias stops at the first step that fails, so a sound at the end plays only
when the steps before it succeed:

```yaml tab
command:
  aliases:
    gitpush:
      - "!! git push"
      - guisound success
```

```python tab
config["command"]["aliases"]["gitpush"] = ["!! git push", "guisound success"]
```

To make a command you already type play a sound, redefine its alias under the
same name. Rune's default aliases, such as `w` and `save` for `write`, are
redefined like your own:

```yaml tab
command:
  aliases:
    w:
      - write
      - guisound success
```

```python tab
config["command"]["aliases"]["w"] = ["write", "guisound success"]
```

An alias named after a command replaces that command and cannot run it: Rune
reports an alias `write` with a `write` step as an alias cycle. To add a sound
to a command you type by its own name, give the alias a new name.

## Volume

`gui.sound_volume` sets the volume of every sound, from `0` (mute) to `1`
(the default):

```yaml tab
gui:
  sound_volume: 0.5
```

```python tab
config["gui"]["sound_volume"] = 0.5
```

To change the volume for the current session, use `guivolume`:

| Command | Effect |
| --- | --- |
| `guivolume increase` | raises the volume by 10% and unmutes |
| `guivolume decrease` | lowers the volume by 10% and unmutes |
| `guivolume mute` | mutes, or unmutes at the previous volume |

Sounds play in the Rune window. With `rune --tui`, every sound rings the
bell of the terminal Rune runs in instead, and that terminal's settings
control how it sounds.
