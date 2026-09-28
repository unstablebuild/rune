---
sidebar_position: 34
---

# Headless

A machine serves its workspaces on the [Rune network](./network.md) only
while Rune runs on it. On a build box or a server nobody sits at, that
means `rune --headless`, and the way to keep it running is to hand it to
the machine's service manager: it starts at boot, restarts if it fails,
and its output lands in the system log. This page is a recipe per
service manager. Read [Running a machine without the
editor](./network.md#running-a-machine-without-the-editor) first if you
have not.

## Before you install the service

Every recipe below assumes the same setup.

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

**Give it a `PATH`.** A service manager starts Rune without your login
shell, so `PATH` is whatever the service definition sets, not what your
`.zshrc` exports. Toolchains that a `rune://` workspace should find on
this machine, such as `go`, `node`, `cargo`, `mise`, or Homebrew, must be
on it. Every recipe sets `PATH` explicitly; extend it to match the host.
Rune adds `~/.rune/bin`, where the packages it installs live, on its own.

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
needs no capabilities, no published ports, and no host networking. The
image installs Rune for an unprivileged user and runs the node as that
user:

```dockerfile
FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl git \
    && rm -rf /var/lib/apt/lists/*
RUN useradd --create-home --uid 1000 alice
USER alice
WORKDIR /home/alice
RUN curl -fsSL https://rune.build/install.sh | sh \
    && mkdir /home/alice/.rune \
    && printf 'network:\n  hostname: buildbox\n' > /home/alice/.rune/config.yaml
ENV PATH=/home/alice/.local/bin:$PATH
CMD ["rune", "--headless"]
```

Build the image and start the service; the first run prints the sign-in
code to the container log:

```bash
docker build -t rune-node .
docker run -d --name rune --restart unless-stopped \
  -v rune-data:/home/alice/.rune \
  -v /srv/projects:/home/alice/projects \
  rune-node
docker logs -f rune
```

Enter the code from the log in a browser and wait for the `Network node`
line.

Three things matter here. `network.hostname` in the baked-in config is
the name the machine joins under; without it the node is named after
whatever hostname the container has, which changes whenever the
container is recreated. The `rune-data` volume holds the config, the
sign-in, and the network identity, so the container can be recreated
without a new login; the image creates that directory as `alice` so a
new volume is hers rather than root's. And a mounted project directory
must be readable and writable by uid 1000, which is what `alice` is
inside the image. Add the toolchains your projects need to the image; a
`rune://` workspace uses what the container has.

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
service; for Docker, rebuild the image. `rune --version` shows what is
installed.

**A removed machine.** If the machine was unregistered with
`network remove` from another machine on your account, restart the
service to register it again, which takes a slot back.

**Signing in again.** The cached sign-in renews itself, so this is rare:
it is needed after the account's access was revoked, or to move the
machine to another account. Stop the service, run `rune --tui` on the
host, and in the [console](./console.md) run `logout` and then `login`,
which prints the URL to open. Quit and start the service again.

**Starting over.** Everything the node has accumulated lives in its data
directory, `~/.rune` unless the service passes `-d`: the sign-in, the
machine's network identity, the config, the log, and the packages Rune
installed. Stop the service and remove the directory to wipe all of it:

```bash
rm -rf ~/.rune
```

For Docker, remove the `rune-data` volume instead. The next start signs
in from scratch, as in [Before you install the
service](#before-you-install-the-service), and joins as a new machine:
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

**Tools are missing in a `rune://` terminal.** The service's `PATH` does
not include them. Add their directories to the `PATH` in the service
definition and restart it.

**`enter the code` in the log.** No account is signed in on the host.
Open the page named in the log in any browser, enter the code, and the
node joins once the sign-in completes. A code expires after a few
minutes; if it has, restart the service for a fresh one.

**`the API server does not offer sign-in by code`.** The node is talking
to a Rune API server too old to sign machines in by code. Sign in on the
host with `rune --tui` and the console's `login` instead, then start the
service.
