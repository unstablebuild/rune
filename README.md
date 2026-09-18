
<p align="center">
  <img src="/extra/icon.iconset/icon_512x512.png" alt="rune" width="100" />
</p>

<p align="center">
  <a href="https://rune.build">rune.build</a> · <a href="https://docs.rune.build/#prerequisites">quick start</a> · <a href="https://docs.rune.build/develop/building">hack on rune</a>
</p>

<p align="center">
  <a href="https://github.com/unstablebuild/rune/releases/latest"><img src="https://img.shields.io/github/v/release/unstablebuild/rune?label=release&amp;labelColor=333333&amp;color=666666" alt="latest stable release" /></a>
  <a href="https://github.com/unstablebuild/rune/releases"><img src="https://img.shields.io/github/downloads/unstablebuild/rune/total?labelColor=333333&amp;color=666666" alt="total GitHub release downloads" /></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-GPLv3-blue.svg" alt="GPLv3 license" /></a>
  <a href="https://github.com/unstablebuild/rune/actions/workflows/test-linux.yml"><img src="https://github.com/unstablebuild/rune/actions/workflows/test-linux.yml/badge.svg?branch=main" alt="Linux tests" /></a>
  <a href="https://github.com/unstablebuild/rune/actions/workflows/test-macos.yml"><img src="https://github.com/unstablebuild/rune/actions/workflows/test-macos.yml/badge.svg?branch=main" alt="macOS tests" /></a>
  <a href="https://discord.gg/xzte9J8f8N"><img src="https://img.shields.io/badge/discord-join-5865F2?logo=discord&amp;logoColor=white" alt="Join us on Discord" /></a>
  <a href="https://www.reddit.com/r/UnstableBuild/"><img src="https://img.shields.io/badge/reddit-r%2FUnstableBuild-FF4500?logo=reddit&amp;logoColor=white" alt="Rune subreddit" /></a>
    <a href="https://x.com/unstablebuild"><img src="https://img.shields.io/badge/follow-%40unstablebuild-000000?logo=x&logoColor=white" alt="follow @unstablebuild on X" /></a>
</p>

---

https://github.com/user-attachments/assets/4ab84f7f-47c8-47af-9d32-7c69afd02669

**Rune is a fast, GPU-accelerated, full-featured IDE and terminal multiplexer, suitable both for automatic and manual programming.**

- **continue working from anywhere**: All your Rune instances form an e2e-encrypted network of peers, powered by our [headscale](https://github.com/juanfont/headscale) network. Connect to your workstation from your laptop, and to your laptop from your workstation.
- **batteries included**: Production-grade language intelligence, out-of-the-box. Check the list of [supported languages](https://docs.rune.build/languages/supported).
- **a new organizing model**: Stay in the flow for longer. Rune's UI is a screen multiplexer that allows you to organize it freely. Nine workspace slots. Infinite terminals, tabs and windows. A built-in agent or IDE-grade skills for yours.
- **plugins and extensions**: Extend Rune's functionality with [official packages →](https://rune.build/packages) or install community ones straight from a Git repository `pkg install github.com/unstablebuild/rune-extension-themebuilder`.
- **native application**: A native graphics pipeline: OpenGL on Linux, Metal on macOS. No Electron.

## install

```bash
curl -fsSL https://rune.build/install.sh | sh
```

### Nix

Run Rune directly without installation:
```bash
nix run github:unstablebuild/rune
```

Install Rune into your user profile:
```bash
nix profile install github:unstablebuild/rune
```

Or add Rune to NixOS (`configuration.nix`) or Home Manager (`home.nix`) via flake inputs:
```nix
# In your system flake.nix: `inputs.rune-ide.url = "github:unstablebuild/rune";`
environment.systemPackages = [
  inputs.rune-ide.packages.${pkgs.system}.rune-ide
];
```

Or build the binary locally into `./result/bin/rune`:
```bash
nix build
```

## development

```bash
git clone git@github.com:unstablebuild/rune.git
```

Then run:
```bash
go run ./cmd/rune
```

### Nix and direnv

Start a development shell with the Go toolchain and C libraries (`libGL`, `X11`, `wayland`, `alsa-lib`):
```bash
nix develop
```

If you use `direnv`, allow `.envrc` to load the shell environment automatically:
```bash
direnv allow
```

To create a macOS application bundle:
```bash
make rune-dmg
```

See [prerequisites](https://docs.rune.build/#prerequisites) for more details.

## Makefile

The Makefile has a few rules for common operations needed during development.

```bash
make                 # build all binaries into bin/
make debug           # build with the race detector and debug-only commands enabled
make clean           # remove bin/ and target/

# individual binaries
make rune            # the editor (bin/rune)
make rune-agent      # the agent extension binary (bin/rune-agent)

# testing and code quality
make test            # run the test suite with the race detector
make test-e2e        # also run the e2e suites (requires docker)
make test-no-race    # run the test suite without the race detector
make coverage        # generate a coverage report
make lint            # run golangci-lint
make format          # run go fmt
make generate        # regenerate generated files (protobufs, mocks, docs)

# license headers
make license         # add the license header to files that are missing one
make assert_license  # fail if any file is missing the canonical header
```

The remaining targets (`dist`, `release`, `rune-dmg*`, `*-docker-*`,
`*-notarize`, `*-dist*`) drive Unstable Build's internal
release, packaging, and cloud deployment pipelines and are not expected to
work outside that environment.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

To report a security issue, see [SECURITY.md](SECURITY.md).

## Sponsorship

Rune is developed by Unstable Build, LLC, a self-funded organization. If you'd like to
financially support us, you can do so via GitHub Sponsors; we might even send you some swag.

## License

Rune is licensed under the [GNU General Public License, version 3](LICENSE)
or, at your option, any later version.
