---
sidebar_position: 1
slug: /
title: Getting Started
description: Install and set up Rune, the fast, keyboard-driven IDE for power users. Code, terminals, CLI tools, language intelligence, debugging, and AI agents in one composable environment.
---

import Head from '@docusaurus/Head';
import Install from '@site/src/components/Install';

<Head>
  <title>Rune: The development environment for pros</title>
  <meta property="og:title" content="Rune: The development environment for pros" />
</Head>

# Getting Started

Rune is a fast, keyboard-driven IDE for power users. The Unix way, finished as a product: code, terminals, CLI tools, language intelligence, debugging, and AI agents, all in one composable, multi-workspace environment.

<div className="intro-screenshot" role="img" aria-label="Rune screenshot" />

------------

## Prerequisites

Rune runs on macOS and Linux. Find your platform below for the specifics.

### macOS

- **Architecture:** Apple Silicon or Intel.
- **Version:** macOS 13.3 (Ventura) or newer.

### Linux

- **Architecture:** `x86_64` or `arm64`.
- **glibc:** version 2.28 or newer (Debian 10+, Ubuntu 20.04+, Fedora 33+,
  or RHEL/Rocky/AlmaLinux 8+). The requirement is a floor, not a ceiling, so
  newer versions are fine. musl-based distributions such as Alpine are not
  supported.
- **Graphics:** a graphical environment (X11, or Wayland via XWayland) with
  the system OpenGL and X11 libraries installed and an OpenGL-capable driver.
  On a minimal or headless install you may need to add your distribution's
  OpenGL (Mesa) and X11 client library packages. Most desktop installs
  already include them.

## Install

<Install />

## Your first steps

Open Rune. The first launch asks one question, then hands you to a guided
tutorial that runs inside the IDE.

### 1. Pick your key bindings

Rune ships with three built-in editors, so pick the one that feels like home:

- **standard**, if you come from VS Code, Cursor, Sublime, or a plain text editor.
- **vim**, if you come from Vim or Neovim and want modal editing everywhere.
- **emacs**, if you come from GNU Emacs and want an Emacs-style keymap everywhere.

The choice applies everywhere, not just in editor buffers: terminals, input
boxes, and the file explorer all follow it. Rune writes the matching preset
into your configuration, so you can change it later from
[Configuration](./config.md) and [Key Mapping](./learn/key-mapping.md).

:::info[Set the same preset here]
The **Preset** button at the top of this page decides which key bindings these
docs show. Click it until it matches the editor you picked, and every key on
every page will match your setup.
:::

If the text is too small, press `<meta>` and `=` to make the font bigger, or
`<meta>` and `-` to make it smaller.

Whichever you pick, the rest is already wired: language intelligence,
debugging, terminals, and tasks ship in the box.

<video
  className="docs-figure"
  autoPlay
  loop
  muted
  playsInline
  preload="metadata"
  poster="https://assets.rune.build/videos/www-rune-batteries-included-v2.jpg"
  aria-label="Rune running language intelligence, a debugger, and terminals side by side">
  <source src="https://assets.rune.build/videos/www-rune-batteries-included-v2.webm#t=0.1" type="video/webm" />
  <source src="https://assets.rune.build/videos/www-rune-batteries-included-v2.mp4#t=0.1" type="video/mp4" />
</video>

### 2. Run the basics tutorial

Rune starts the **basics** tutorial for you right after the first-run setup.
It renders as an overlay on top of the real IDE, using your bindings and your
theme, and it advances when you actually run the command it asks for, not when
you click through a slideshow.

Have a project directory handy. The first step asks you to open it as a
workspace.

Basics walks you through:

- **Workspaces:** the home workspace, the nine workspace slots, and
  `workspaceopen`.
- **Files:** `edit`, the file explorer, and tabs.
- **Windows:** split, focus, move, resize, maximize, and close.
- **Terminals:** terminal splits, plus one-shot programs such as `! git log`.
- **Commands:** how they are named, and why that makes the fuzzy finder fast.
- **Your configuration:** switch themes, then make the change permanent.

:::info[Nothing to memorize]
Press <KeyBinding command="cheatsheet" /> at any point for a cheat sheet of
every key and command the tutorial teaches.
:::

Go at your own pace. From the command prompt (<CommandPromptKey />), run
`tutorial stop` to leave the tutorial, and `tutorial start basics` to take it
again from the top.

### 3. Keep going

When basics finishes, Rune offers the next tutorial. You can also start either
one yourself from the command prompt:

- `tutorial start navigation`: structural navigation and Rune's code
  intelligence tools.
- `tutorial start agent`: install Rune Agent, connect a model provider, and
  start a conversation.

Finish the agent tutorial and you get a `help` command. It opens this
documentation as a workspace inside Rune and starts an agent with those pages
in scope, so you can ask it to troubleshoot a problem or walk you through a
configuration change instead of searching a site.

<div className="docs-figure help-command-figure" role="img" aria-label="Running help in the command prompt to open the docs and ask the agent a question" />

After that, the [Command Prompt](./learn/command-prompt.md) guide covers the
prompt in depth, [Configuration](./config.md) covers making Rune yours, and the
[Docs Workspace](./learn/docs-workspace.md) guide covers reading the
documentation offline. You can also
[write your own tutorials](./develop/tutorials.md) to onboard your team into a
repository.
