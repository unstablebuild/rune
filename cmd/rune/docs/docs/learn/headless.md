---
sidebar_position: 34
---

# Headless

A machine serves its workspaces on the [Rune network](./network.md) only
while Rune runs on it. On a build box or a server nobody sits at, that
means `rune --headless`, and the way to keep it running is to hand it to
the machine's service manager: it starts at boot, restarts if it fails,
and its output lands in the system log. This page is a recipe per
service manager, and a ready-made [Docker image](#docker) for hosts that
run containers. Read [Running a machine without the
editor](./network.md#running-a-machine-without-the-editor) first if you
have not.

## Before you install the service

Every recipe below assumes the same setup. The exception is
[Docker](#docker): the image installs Rune and its user itself, so skip
ahead.

**Install Rune on the host.** The one-line installer puts the binary at
`~/.local/bin/rune` on both Linux and macOS:

```
curl -fsSL https://rune.build/install.sh | sh
```

A headless node needs no display and no graphical libraries, so a
minimal server install is enough. See the
[prerequisites](/#prerequisites) for the platform floor.

**Run it as yourself, not as root.** The node serves files as the user
it runs as, and the terminals and language servers behind a `rune://`
workspace run as that user too. Run it under the account whose projects
you want to open. The recipes use a user `alice` with home
`/home/alice`; substitute your own.

**Sign in once.** On the node run `rune --headless`. It prints a code
to enter in a browser on any machine you have to hand, and waits:

```
To sign this machine in, open

    https://auth.rune.build/activate

in any browser and enter the code ABCD-EFGH (expires 14:32).

Signed in as alice@example.com (Rune Pro)
Network node buildbox is Running
  address: 100.64.0.7
```

Once the account and node lines appear, press `Ctrl-C`. The sign-in and
the machine's network identity are now cached in `~/.rune`, so the
service never asks again. Nothing needs to reach the node: it only polls
out, so this works over SSH, in a container, or under a service manager.
You can skip the foreground run and let the service do it on its first
start instead: the code shows up in its log, and the node joins once you
have entered it.

The sign-in is a serve-only one: the node can serve your other machines
but not reach them (see [Headless machines only
serve](./network.md#headless-machines-only-serve)). A sign-in with full
account access is never kept. A node that finds one in its data
directory, from a node set up before headless machines were serve-only
or a data directory copied from a desktop install, says so, discards it,
and prints a code to sign in again.

**Give it a `PATH`.** A service manager starts Rune without your login
shell, so on startup Rune asks your login shell for the `PATH` your
startup files set up, such as `.profile` or `.zshrc`, and keeps the
directories the service definition adds as well. Tools that a `rune://`
workspace should find on this machine, that Rune does not install and
that your startup files do not add, such as `node`, `mise`, or Homebrew,
must be on the service's `PATH`. Every recipe sets `PATH` explicitly;
extend it to match the host. Rune puts `~/.rune/bin`, where the packages
it installs live, first on its own, and terminals keep it first even when
a startup file such as `/etc/profile` resets `PATH`.

Language packages need no setup. When a workspace you open on this
machine needs one it does not have, Rune asks you in your own window and
installs it here (see [Installing packages on another
machine](./network.md#installing-packages-on-another-machine)). The
environment a package sets up, such as `GOROOT`, applies to the
terminals and tools started after the install, with no restart.

**Leave `network.auto_join` on.** It is the default. A headless node has
no console to run `network up` in, so with it off Rune says so and exits.
A stable name is worth setting when the host's own name is not one you
would type, which is always the case in a container:

```yaml tab
network:
  hostname: buildbox
```

```python tab
config["network"] = {
    "hostname": "buildbox",
}
```

That goes in the host's own [Rune config](../config.md), at
`~/.rune/config.yaml` or `~/.rune/config.star`.

## systemd, as a user service

The service runs under your account, needs no root to install, and
starts at boot once lingering is on. This is the recipe for most Linux
hosts.

Create `~/.config/systemd/user/rune.service`:

```ini
[Unit]
Description=Rune network node

[Service]
ExecStart=%h/.local/bin/rune --headless
Environment=PATH=%h/.local/bin:%h/go/bin:%h/.cargo/bin:/usr/local/bin:/usr/bin:/bin
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=default.target
```

Then enable it and let it outlive your login sessions:

```bash
systemctl --user daemon-reload
systemctl --user enable --now rune
sudo loginctl enable-linger "$USER"
```

User services normally start when you log in and stop when your last
session ends. `enable-linger` starts your user's service manager at boot
and keeps it running, so the node is up whether or not anyone is logged
in. Follow it with:

```bash
journalctl --user -u rune -f
```

A user service cannot wait for the system's network to come up. If Rune
starts before the machine is online it fails to join and exits, and
`Restart=on-failure` starts it again five seconds later, which is the
whole point of that line.

## systemd, as a system service

When the unit belongs in `/etc`, for a host an administrator manages or
one where lingering is not available, the same service runs as a system
unit with the user named in it. Create `/etc/systemd/system/rune.service`:

```ini
[Unit]
Description=Rune network node
After=network-online.target
Wants=network-online.target

[Service]
User=alice
ExecStart=/home/alice/.local/bin/rune --headless
Environment=PATH=/home/alice/.local/bin:/home/alice/go/bin:/usr/local/bin:/usr/bin:/bin
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

`%h` means the service manager's own home in a system unit, which is
root's, so paths are spelled out here. Enable and follow it with:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now rune
journalctl -u rune -f
```

## launchd (macOS)

A launch agent runs the node under your account whenever you are logged
in. Create `~/Library/LaunchAgents/build.rune.headless.plist`, with your
own home directory in place of `/Users/alice`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>build.rune.headless</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/alice/.local/bin/rune</string>
    <string>--headless</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/Users/alice/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>StandardOutPath</key>
  <string>/Users/alice/Library/Logs/rune-headless.log</string>
  <key>StandardErrorPath</key>
  <string>/Users/alice/Library/Logs/rune-headless.log</string>
</dict>
</plist>
```

Load it, and follow the log:

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/build.rune.headless.plist
tail -f ~/Library/Logs/rune-headless.log
```

`launchctl bootout gui/$(id -u)/build.rune.headless` stops and unloads
it. `KeepAlive` with `SuccessfulExit` false restarts the node after a
failure but not after a clean stop.

A launch agent needs a logged-in user. On a Mac nobody logs into, turn on
automatic login for the account in System Settings, or install the same
plist under `/Library/LaunchDaemons` with a `UserName` key and `HOME` in
`EnvironmentVariables`, which launchd starts at boot without a session.

## OpenRC

Create `/etc/init.d/rune` and make it executable. `supervise-daemon`
keeps the node in the foreground under its own supervision and restarts
it when it fails:

```sh
#!/sbin/openrc-run
description="Rune network node"

supervisor=supervise-daemon
command="/home/alice/.local/bin/rune"
command_args="--headless"
command_user="alice:alice"
directory="/home/alice"
respawn_delay=5
output_log="/var/log/rune.log"
error_log="/var/log/rune.log"

export HOME="/home/alice"
export PATH="/home/alice/.local/bin:/usr/local/bin:/usr/bin:/bin"

depend() {
	need net
}
```

```bash
sudo chmod +x /etc/init.d/rune
sudo rc-update add rune default
sudo rc-service rune start
tail -f /var/log/rune.log
```

## runit

Create the service directory `/etc/sv/rune` with a `run` script and a
`log/run` script, both executable. runit supervises the process and
restarts it whenever it exits.

`/etc/sv/rune/run`:

```sh
#!/bin/sh
exec 2>&1
export HOME=/home/alice
export PATH=/home/alice/.local/bin:/usr/local/bin:/usr/bin:/bin
cd /home/alice
exec chpst -u alice /home/alice/.local/bin/rune --headless
```

`/etc/sv/rune/log/run`:

```sh
#!/bin/sh
exec svlogd -tt /var/log/rune
```

```bash
sudo mkdir -p /var/log/rune
sudo chmod +x /etc/sv/rune/run /etc/sv/rune/log/run
sudo ln -s /etc/sv/rune /var/service/
sudo sv status rune
tail -f /var/log/rune/current
```

The service directory is `/var/service` on Void Linux and `/etc/service`
on most other runit setups.

## Docker

A container makes a fine node: the network is userspace WireGuard, so it
needs no capabilities, no published ports, and no host networking. Rune
publishes an image that runs `rune --headless` as an unprivileged user,
`rune`, for both amd64 and arm64:

```bash
docker run -d --name rune --restart unless-stopped \
  --hostname buildbox \
  -v rune-data:/home/rune/.rune \
  -v /srv/projects:/home/rune/projects \
  unstablebuild/rune
docker logs -f rune
```

The first start has no account signed in, so the log shows a code and
the page to enter it on:

```
To sign this machine in, open

    https://auth.rune.build/activate

in any browser and enter the code ABCD-EFGH (expires 14:32).
```

Open the page on any machine, enter the code, and wait for the account
and `Network node` lines to follow it in the log. Then press `Ctrl-C` to
stop following the log; the node keeps running. From your laptop,
`workspaceopen rune://buildbox/home/rune/projects/app` opens a project
in it.

Three things matter here. `--hostname` is the name the machine joins
the network under; without it the node is named after the container's
ID, which changes whenever the container is recreated. The `rune-data`
volume holds the sign-in, the network identity, and the node's
[config](../config.md) (`/home/rune/.rune/config.yaml`), so the
container can be recreated, or upgraded to a new image, without signing
in again. And a mounted project directory must be readable and writable
by uid 1000, which is what `rune` is inside the image.

**Adding tools.** A `rune://` workspace uses what the container has: its
terminals, language servers, and tasks run inside it. Language packages
install on demand, as on any other machine, and are kept in the
`rune-data` volume. For the system tools your projects need beyond
them, build your own image on top of it:

```dockerfile
FROM unstablebuild/rune
USER root
RUN apt-get update && apt-get install -y --no-install-recommends git make
USER rune
```

Build it with `docker build -t rune-node .` and run `rune-node` in place
of `unstablebuild/rune` above. The image is Debian, so `apt-get` and
prebuilt binaries linked against glibc both work in it.

## Operating the node

**Logs.** Everything goes to standard output, so `journalctl`, the
launchd log file, or `docker logs` is the place to look. Rune writes the
same log to `log_path` as well, `~/.rune/debug.log` by default. Set
`log_path: ""` in the host's config to keep only the service manager's
copy.

**Stopping.** The node exits cleanly on `SIGTERM`, which is what every
service manager above sends on stop.

**Upgrading.** A headless node has no upgrade prompt and does not
upgrade itself. Run the installer again on the host, then restart the
service. For Docker, `docker pull unstablebuild/rune` (and rebuild any
image of your own on top of it), then remove the container and run it
again; the volume carries the sign-in over. `rune --version` shows what
is installed.

**A removed machine.** If the machine was unregistered with
`network remove` from another machine on your account, restart the
service to register it again, which takes a slot back.

**Signing in again.** The cached sign-in renews itself, so this is rare:
it is needed after the account's access was revoked, or to move the
machine to another account. Stop the service, run `rune --tui` on the
host, and in the [console](./console.md) run `logout`. Quit, then run
`rune --headless` in the foreground and enter the code it prints, as in
[Before you install the service](#before-you-install-the-service).
Start the service again. Signing in with `login` from `rune --tui`
instead gives the host full account access, which a headless node
discards on its next start.

For Docker, stop the container and run the editor on the same volume:

```bash
docker stop rune
docker run -it --rm -v rune-data:/home/rune/.rune unstablebuild/rune --tui
```

Run `logout` in its console and quit, then `docker start rune` and enter
the code from `docker logs -f rune`.

**Starting over.** Everything the node has accumulated lives in its data
directory, `~/.rune` unless the service passes `-d`: the sign-in, the
machine's network identity, the config, the log, and the packages Rune
installed. Stop the service and remove the directory to wipe all of it:

```bash
rm -rf ~/.rune
```

For Docker, remove the `rune-data` volume instead. The next start signs
in from scratch, as in [Before you install the
service](#before-you-install-the-service) or [Docker](#docker), and
joins as a new machine:
the old one stays in `network machines` as offline until you
`network remove` it, and on a free plan holds its slot until then.

## Troubleshooting

**`network.auto_join is off in ~/.rune/config.yaml`.** Set it to `true`
in the host's config; a headless node has no other way to join.

**`could not join the network` right after boot.** The machine was not
online yet. The recipes restart the node on failure, so it joins on the
next try; check `network peers` from another machine a minute later.

**`could not join the network` on every restart.** Read the rest of the
line. `the network links the machines of one Rune account` means the
cached sign-in no longer works: sign in again as described above. `the
free Rune plan includes 2 machines` means the account is full: free a
slot with `network remove` from another machine, or upgrade.

**Tools are missing in a `rune://` terminal.** A terminal is a login
shell: it builds `PATH` from your startup files, then puts the tools Rune
installs and the `PATH` entries from [`gui.env`](../config.md#gui) in the
host's config in front. A tool that is still missing is in neither place. Add its
directory in your shell's startup files, or to `gui.env.PATH` in
`~/.rune/config.yaml` on the host and restart the service.

**`enter the code` in the log.** No account is signed in on the host.
Open the page named in the log in any browser, enter the code, and the
node joins once the sign-in completes. A code expires after a few
minutes; if it has, restart the service for a fresh one.

**`the API server does not offer sign-in by code`.** The node is talking
to a Rune API server too old to sign machines in by code, and a headless
node has no other way to sign in. Point it at a current API server.

**`the API server is too old to sign machines in as serve-only`** or
**`did not issue a serve-only sign-in`.** The node is talking to a Rune
API server older than serve-only machines. A headless node never runs
with full account access, so it cannot start until it is pointed at a
current API server.

**`This machine holds a sign-in with full account access`.** The data
directory came from a desktop install or from before headless machines
were serve-only. The node has discarded that sign-in; enter the code it
prints next.
