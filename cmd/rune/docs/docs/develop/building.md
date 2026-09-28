---
sidebar_position: 9
description: Build Rune from source on macOS and Linux, run it with `go run ./cmd/rune`, and make your first change to the editor.
---

# Rune

Rune is a single Go module. A plain checkout builds with the standard Go
tooling: no submodules, no vendored trees, no code generation step. The one
thing that is not pure Go is the renderer, which links OpenGL and the
platform's window and input libraries through cgo. That is what most of the
prerequisites below are for.

This page takes you from a clean machine to a Rune you compiled yourself,
then through a first change to the editor's source.

## What you need

- **Go**, at the version pinned in the repository's `go.mod`. If your
  distribution ships an older Go, the default `GOTOOLCHAIN=auto` downloads the
  right toolchain on the first build, so any Go 1.21 or newer usually works as
  a bootstrap.
- **Git**.
- **A C toolchain** (`cc`, `c++`) and `pkg-config`, because the renderer uses
  cgo. Do not set `CGO_ENABLED=0`.
- **On Linux, development headers** for X11, OpenGL, ALSA, Wayland, and
  xkbcommon. The runtime libraries alone are not enough: you need the `-dev`
  or `-devel` packages.
- **A graphical session**, to run the windowed editor. Rune renders through
  OpenGL on X11, so a Wayland desktop needs XWayland (present by default on
  GNOME, KDE, and Sway). `rune --tui` and `rune --headless` need none.

## macOS

Apple Silicon and Intel are both supported, on macOS 13.3 (Ventura) or newer.
You need the Xcode command line tools for `clang`, and Go:

```bash
xcode-select --install
brew install go
```

There are no other dependencies. The window, graphics, and audio frameworks
the renderer links against ship with the OS. Check your toolchain with:

```bash
go version
cc --version
```

## Linux

Pick your distribution below. Each list is verified by building `./cmd/rune`
in a clean container image of that distribution, so it is known to be
sufficient.

If your distribution is not listed, the packages you are looking for are the
development files for `libX11`, `libXrandr`, `libXcursor`, `libXinerama`,
`libXi`, `libXxf86vm`, `libGL` (Mesa), `libasound` (ALSA), `wayland`, and
`libxkbcommon`, plus a C and C++ compiler and `pkg-config`.

### Debian and Ubuntu

```bash
sudo apt-get update
sudo apt-get install -y --no-install-recommends \
  git golang-go gcc g++ pkg-config \
  libgl1-mesa-dev libx11-dev libxrandr-dev libxcursor-dev \
  libxinerama-dev libxi-dev libxxf86vm-dev \
  libasound2-dev libwayland-dev libxkbcommon-dev
```

### Fedora, RHEL, Rocky, and AlmaLinux

```bash
sudo dnf install -y \
  git golang gcc gcc-c++ pkgconf-pkg-config \
  mesa-libGL-devel libX11-devel libXrandr-devel libXcursor-devel \
  libXinerama-devel libXi-devel libXxf86vm-devel \
  alsa-lib-devel wayland-devel libxkbcommon-devel
```

On RHEL, Rocky, and AlmaLinux, `libXxf86vm-devel` ships in CodeReady Builder
rather than AppStream, so enable that repository first:

```bash
sudo dnf install -y dnf-plugins-core
sudo dnf config-manager --set-enabled crb   # `powertools` on RHEL 8
```

### Arch and Manjaro

```bash
sudo pacman -S --needed \
  git go base-devel mesa libx11 libxrandr libxcursor \
  libxinerama libxi libxxf86vm alsa-lib wayland libxkbcommon
```

Arch does not split development headers into separate packages, so the
ordinary library packages are all you need.

### openSUSE

```bash
sudo zypper install -y \
  git go gcc gcc-c++ pkgconf-pkg-config \
  Mesa-libGL-devel libX11-devel libXrandr-devel libXcursor-devel \
  libXinerama-devel libXi-devel libXxf86vm-devel \
  alsa-devel wayland-devel libxkbcommon-devel
```

Note that the pkg-config package is `pkgconf-pkg-config`; there is no
`pkg-config` package on openSUSE.

### Alpine

```bash
sudo apk add \
  git go build-base pkgconf mesa-dev libx11-dev libxrandr-dev \
  libxcursor-dev libxinerama-dev libxi-dev libxxf86vm-dev \
  alsa-lib-dev wayland-dev libxkbcommon-dev
```

Alpine needs a `go` package that is already at or above the version in
`go.mod`. The automatic toolchain download that rescues other distributions
does not work here, because the toolchains Go fetches are linked against
glibc. If `apk` gives you an older Go, use `edge` or the `community`
repository of a newer release.

Alpine is a supported *build* host but not a supported *release* target: the
binaries published on the [Getting Started](../intro.md) page are glibc
builds.

### Void

```bash
sudo xbps-install -y \
  git go base-devel pkg-config MesaLib-devel libX11-devel libXrandr-devel \
  libXcursor-devel libXinerama-devel libXi-devel libXxf86vm-devel \
  alsa-lib-devel wayland-devel libxkbcommon-devel
```

### Headless machines

The build itself does not need a display, so a container or CI runner can
compile Rune with the packages above and nothing else. Only the windowed
editor needs a display at runtime: Rune loads the X11 and OpenGL client
libraries with `dlopen` when it opens a window, so `rune --tui` and
`rune --headless` run on a machine with none of them installed.

Parts of the test suite do drive the GUI. Rune's own Linux CI runs them
under `xvfb-run`, and you can do the same:

```bash
sudo apt-get install -y --no-install-recommends xauth xvfb
xvfb-run -a make test
```

## Get the source

```bash
git clone https://github.com/unstablebuild/rune.git
cd rune
```

Clone with the full history. The `make` targets stamp `git describe --tags`
into the binary, which a shallow clone cannot produce.

## Your first `go run`

```bash
go run ./cmd/rune
```

That is the whole build. The first invocation downloads the module graph and
compiles the cgo renderer, so give it a few minutes; every run after that is
served from Go's build cache and takes seconds.

Rune opens on the directory you ran it from, so you are now editing Rune's
source inside Rune. The first launch asks which key-binding preset you want
and then offers a guided tutorial. If you want the details of that flow, see
[Getting Started](../intro.md).

### Keep your development state out of your real config

If you already use Rune day to day, point the development build at a separate
data directory so an experiment cannot corrupt your working setup:

```bash
go run ./cmd/rune -d ~/.rune-dev
```

`-d` relocates the whole data directory: config, installed packages, logs,
and crash reports. The config file is resolved inside it, so there is no need
to pass `-c` as well. Delete `~/.rune-dev` whenever you want a clean
first-run experience again.

## Make your first change

Rune's commands are what the [Command Prompt](../learn/command-prompt.md)
dispatches, and adding one touches exactly two files. It is the smallest
useful change you can make to the editor, so it is a good first one.

Every command is an entry in the `exCommands` table in
`internal/ide/commands.go`. Add one at the top of the table:

```go
var (
	exCommands = map[string]commandAll{
		"hello": {
			man: textapi.CommandManual{
				Summary:  "Say hello from a locally built Rune.",
				Synopsis: "[<name>]",
			},
			handler: (*ex).hello,
		},
		"keydump": {
```

`man` is not decoration. It is what the command prompt shows while you are
typing, and what the fuzzy finder searches, so every command carries one.

The `handler` field names a method on `*ex`, the type that implements the
editor's commands. Add it at the end of `internal/ide/ex.go`:

```go
func (e *ex) hello(_ context.Context, args ...string) error {
	who := "world"
	if len(args) > 0 {
		who = strings.Join(args, " ")
	}
	_, err := e.notifications.Notify(browserapi.LevelSuccess, "hello, "+who)
	return err
}
```

Every handler has the same shape: it takes a `context.Context` and the
arguments the user typed, and returns an error. Returning an error is how a
command reports failure to the user, so handlers do not render their own
error popups.

Now run it again:

```bash
go run ./cmd/rune
```

Only the packages you touched are recompiled, so this is fast. Open the
command prompt with <CommandPromptKey /> and run:

```
hello
hello Rune
```

A success notification appears in the corner. Type `hel` and press `<tab>`
and you will see the summary you wrote, because the prompt reads the same
table you edited.

## Before you send a patch

```bash
make test
make lint
```

`make test` runs the suite with the race detector and must stay hermetic, so
anything that spawns a real external process lives behind the `e2e` build tag
and runs with `make test-e2e` instead. `make lint` needs
[golangci-lint](https://golangci-lint.run/) on your `PATH`.

`make generate` regenerates protobuf stubs and mocks. You only need it when
you change a `.proto` file or a `go:generate` directive.

See [CONTRIBUTING.md](https://github.com/unstablebuild/rune/blob/main/CONTRIBUTING.md)
for the sign-off requirement and the rest of the review process.

## Troubleshooting

**`fatal error: X11/Xlib.h: No such file or directory`**

The C compiler cannot find the X11 headers. You have the runtime libraries
but not the development packages; install the list for your distribution
above. The same cause produces `GL/gl.h`, `X11/extensions/Xrandr.h`, and
`alsa/asoundlib.h` variants of the error.

**`undefined: Window` in `internal/glfw`, or `undefined: tree_sitter.Parser`**

cgo is disabled, so the compiler is seeing only the stub files that remain
when the `cgo` build constraint is off. Unset `CGO_ENABLED` or set it to `1`.

**`go: go.mod requires go >= 1.x`**

Your Go is older than the module. Leave `GOTOOLCHAIN` at its default `auto`
so Go fetches the pinned toolchain, or install a newer Go. On musl systems
such as Alpine the download does not help; install a new enough Go package
instead.

**The window fails to open on a Wayland session**

Install XWayland. Rune renders through OpenGL on X11 and does not have a
native Wayland backend.
