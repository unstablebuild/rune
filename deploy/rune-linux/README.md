# Rune Linux builds

The Rune GUI binary is built for `linux/amd64` or `linux/arm64` in one of
two ways:

- **Cross** (default): `make rune-release-linux-<arch>` /
  `make rune-prod-dist-linux-<arch>` cross-compile inside Docker via
  `deploy/rune-linux/Dockerfile` (pinned to `debian:buster-slim`,
  glibc 2.28). This is the published path: the artifact's glibc floor is
  fixed at 2.28 regardless of the build machine, so it runs on any
  glibc >= 2.28 host, and it works from any host arch without forcing the
  Go toolchain under target-arch emulation. `*-cross` aliases are kept for
  backward compatibility.
- **Native** (`*-native` targets): `make rune-release-linux-<arch>-native`
  / `make rune-prod-dist-linux-<arch>-native` build with the host's own
  toolchain. This requires a Linux host whose architecture matches the
  target (`uname -m` mapped to `amd64`/`arm64`); cross-arch native builds
  are not supported (no emulation). The glibc floor becomes the build
  host's glibc, so prefer the default cross build for anything you ship.

## Supported hosts

The published cross build runs on any Linux distribution with glibc 2.28
or newer, which covers Debian 10+, Ubuntu 20.04+, Fedora 33+, and
RHEL / Rocky / AlmaLinux 8+. An X11 and OpenGL capable environment is
required for the GUI only; `rune --tui` and `rune --headless` load no
graphical library and run on a server that has none installed.

## Supported targets

| Target | Cross compiler | pkg-config path |
|---|---|---|
| `linux/amd64` | `x86_64-linux-gnu-gcc` | `/usr/lib/x86_64-linux-gnu/pkgconfig` |
| `linux/arm64` | `aarch64-linux-gnu-gcc` | `/usr/lib/aarch64-linux-gnu/pkgconfig` |

## How it works

### Native build (`*-native`)

`make rune-release-linux-<arch>-native` runs `cmd/rune`'s
`make-release-linux` recipe, which mirrors the Docker bundle layout using
the host toolchain:

1. building `./cmd/rune` with `CGO_ENABLED=1` and
   `rpath=$ORIGIN/../lib`, applying the same `RUNE_ENV`-driven ldflags as
   every other build
2. copying each NEEDED shared library (and transitive deps) into
   `rune.app/lib/` via `objdump`, skipping glibc core libraries
3. copying the `.desktop` entry, icons, and zsh dot files into
   `rune.app/share/`
4. packaging `rune.app/` into a `ustar` `.tar.gz`

It refuses to run unless the host is Linux and its arch matches the
requested target arch; use the default cross build otherwise.

### Cross-compile build (default)

The `make rune-linux-cross-compile` rule (and the `*-cross` targets that
wrap it) works by:

1. building `deploy/rune-linux/Dockerfile` with `docker buildx`
2. running the Go toolchain on `$BUILDPLATFORM`
3. passing `GIT_SSH_KEY` as a build arg so the private Go modules resolve
4. installing the target-arch Linux cross compiler and development headers
5. cross-compiling `./cmd/rune` to `linux/$TARGETARCH` with
   `rpath=$ORIGIN/../lib` so the binary finds its bundled libraries
6. copying each NEEDED shared library (and their transitive deps) into
   `rune.app/lib/`, following symlinks so every file is a real ELF object
7. packaging `rune.app/` into a `ustar` `.tar.gz` inside the Linux container
   so host-specific metadata such as macOS xattrs cannot enter the archive
8. exporting both the `rune.app/` directory and `.tar.gz` from the final
   scratch stage

## Build commands

Cross-compile for `linux/amd64` (default):

```bash
make rune-linux-cross-compile
# or explicitly:
make rune-linux-cross-compile RUNE_LINUX_TARGET_ARCH=amd64
```

Cross-compile for `linux/arm64`:

```bash
make rune-linux-cross-compile RUNE_LINUX_TARGET_ARCH=arm64
```

## Release tarballs

To build and package a release tarball via the default Docker cross-compile
path (any host, glibc 2.28 floor):

```bash
make rune-release-linux-amd64   # -> target/rune_linux_amd64/rune-release-linux-amd64-<tag>.tar.gz
make rune-release-linux-arm64   # -> target/rune_linux_arm64/rune-release-linux-arm64-<tag>.tar.gz
```

To build the same tarball natively with the host toolchain (host arch must
match the target arch; floor becomes the host's glibc):

```bash
make rune-release-linux-amd64-native
make rune-release-linux-arm64-native
```

To build, package, and publish as a GitHub release asset:

```bash
# Production (unstablebuild/rune, prod API endpoints baked in)
# default cross-compile, glibc 2.28 floor
make rune-prod-dist-linux-amd64
make rune-prod-dist-linux-arm64

# Staging (unstablebuild/rune-staging, staging API endpoints baked in)
# default cross-compile, glibc 2.28 floor
make rune-staging-dist-linux-amd64
make rune-staging-dist-linux-arm64

# Native host-toolchain variants (host arch must match target arch)
make rune-prod-dist-linux-amd64-native
make rune-prod-dist-linux-arm64-native
make rune-staging-dist-linux-amd64-native
make rune-staging-dist-linux-arm64-native
```

## Release layout

The tarball and cross-compile output follow the same `.app` directory
convention used by Zed:

```
rune.app/
  bin/
    rune            # the main binary (rpath = $ORIGIN/../lib)
  lib/
                    # bundled shared libraries, if any are NEEDED
  share/
    applications/
      rune.desktop  # freedesktop .desktop entry
    icons/
      hicolor/
        512x512/apps/rune.png
        1024x1024/apps/rune.png
    zdot/
      .zlogin         # zsh dot files for integrated terminal
      .zprofile
      .zshenv
      .zshrc
      inputrc         # readline bindings for bash in the integrated terminal
```

## System library requirements

The binary bundles its direct NEEDED shared libraries and their
transitive dependencies in `rune.app/lib/`. glibc core libraries
(`libc`, `libm`, `libpthread`, `libdl`, `librt`, `ld-linux`) are
**not** bundled and must be provided by the host system.

X11, OpenGL, and Wayland client libraries are not bundled either: Rune
loads them with `dlopen` when it opens a window, so the host provides
them, and a host that has none can still run `rune --tui` and
`rune --headless`.
