
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

## development

```bash
git clone git@github.com:unstablebuild/rune.git
```

Then run:
```bash
go run ./cmd/rune
```

To create a macOS application bundle:
```bash
make rune-dmg
```

To compile all executables in `./bin`:
```bash
make -j16
```

## headless

Run Rune in headless mode to serve a workspace to all of your development machines,
over the [Rune network](https://docs.rune.build/learn/network):

```bash
docker run -d --name rune \
  --hostname devpod \
  -v /src/projects:/home/rune/projects \
  unstablebuild/rune

$ docker logs -f rune  # prints the sign-in code
```

`--hostname` is the name the node joins the network under, so from any of your machines
`workspaceopen rune://devpod/home/rune/projects` opens the mounted projects.
Language packages install on demand: when a workspace on the node needs one it lacks,
Rune asks you on your machine and installs it on the node.

The [headless guide](https://docs.rune.build/learn/headless) covers running the node as a
service under systemd, launchd, OpenRC, or runit, and adding system tools to the Docker image.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

To report a security issue, see [SECURITY.md](SECURITY.md).

## Sponsorship

Rune is developed by Unstable Build, LLC, a self-funded organization. If you'd like to
financially support us, you can do so via GitHub Sponsors; we might even send you some swag.

## License

Rune is licensed under the [GNU General Public License, version 3](LICENSE)
or, at your option, any later version.
