---
sidebar_position: 33
---

# Network

Every machine you run Rune on can join one private, encrypted network of
your own. Once two of them are on it, either one can open a workspace on
the other:

```
workspaceopen rune://<machine>/path/to/project
```

There is no SSH server to run, no port to forward, no public address to
expose, and no credentials to hand out. The machines find each other
through Rune's coordination server and talk directly, so a laptop behind
a home router and a workstation behind an office firewall reach each
other without either being reachable from the internet.

A `rune://` workspace behaves exactly like an [SSH
workspace](./ssh.md) once it is open: files, terminals, language
intelligence, tasks, and the agent all run on the other machine, next to
the code.

## What you need

- **A Rune account.** Run `login` in the [Rune console](./console.md) to
  sign in; Rune offers to sign you in or sign you up when you are not.
  The free plan covers 2 machines on the network, and a paid plan covers
  as many as you like. When a third machine tries to join a free
  account, Rune offers to upgrade and points you at `network remove`;
  after upgrading, run `login` again so the machine picks up the new
  plan.
- **Rune running on both machines, signed into the same account.** A
  machine serves its workspaces from the Rune instance running on it. If
  Rune is not open there, the machine is not reachable, and only your own
  machines are ever allowed to connect. A machine nobody sits at can run
  Rune as a service instead; see [Headless](./headless.md).

## Joining the network

Rune joins on startup, so usually there is nothing to do. Check where
this machine stands with `network status` in the console:

```
network status
```

It reports the name this machine is known by, its state (`Running` once
it has joined), its addresses on the network, the account it belongs to,
and the last error if it could not join. While a sign-in is pending it
prints a URL to open in a browser to authorize the machine.

To join without restarting, or after a `network down`, run `network up`.
It waits until the machine is a member and then prints the status. If
joining takes longer than 30 seconds, Rune tells you so and keeps trying
in the background; run `network status` to see how it went.

### The `network` command

`network` is a [Rune console](./console.md) command: open the console and
run it as `network <subcommand>`, or submit a one-off from the
[command prompt](./command-prompt.md) with `console network <subcommand>`.

| Command | What it does |
| --- | --- |
| `network status` | Show this machine's name, addresses, and sign-in state, including the last error and any pending authorization URL. |
| `network peers` | List the other machines connected to the network, with the `rune://` address each one is reachable at. |
| `network machines` | List every machine registered to your account, whether or not it is on the network right now, and which one you are on. |
| `network remove <machine>` | Unregister a machine from your account, freeing the slot it held. |
| `network up` | Join the network and wait until this machine is a member. |
| `network down` | Leave the network. Open `rune://` workspaces stop working until you run `network up` again. |
| `network help` | Show the command's usage. |

## How many machines you can have

The free plan covers 2 machines. A third one is turned away when it asks
to join, with the offer to upgrade or to make room. `network machines`
shows what is registered to your account:

```
network machines
```

| machine | state | last seen |
| --- | --- | --- |
| carbon (this machine) | online | 2026-03-01 12:04 |
| studio | offline | 2026-02-19 09:31 |

Unlike `network peers`, which reads the network itself, this list comes
from your account, so it also works from a machine that is being kept
off the network. Free the slot a machine holds with:

```
network remove studio
```

Press `Tab` after `network remove` to complete the machine name from your
account's list, so there is nothing to type from memory.

The machine keeps working locally; it is only unregistered from the
network. Running `network up` on it registers it again, which takes a
slot back. A paid plan has no limit, so nothing has to be removed.

A removed machine that is running and online is told right away: Rune
there shows **"This machine has been removed from the network."** One
that is asleep or shut down finds out the next time it is used.

## Opening a workspace on another machine

`network peers` lists what you can open:

```
network peers
```

| machine | workspace | os | state |
| --- | --- | --- | --- |
| carbon | `rune://carbon/` | linux | online |
| studio | `rune://studio/` | macOS | offline |

Open one with `workspaceopen` from the
[command prompt](./command-prompt.md):

```
workspaceopen rune://carbon/src/project
```

The parts of the URL are:

- `carbon` is the machine's name on the network, as shown by
  `network peers`. Typing `rune://` at the `workspaceopen` prompt
  completes it for you.
- `/src/project` is the directory to open on that machine. Any path you
  could open locally there works. Leave it out (`rune://carbon/`) to open
  that machine's home directory.

A `rune://` URL takes no user name: machines authenticate by their own
identity, not by an account on the other end, so `rune://user@carbon/`
is rejected.

Offline machines are listed and completed too. Opening one waits and
connects as soon as it comes back, which is also what happens when a
machine sleeps or changes networks mid-session: Rune reconnects on its
own and the workspace carries on. Two failures are final rather than
retried, because retrying cannot fix them: a machine that is not on the
network at all, and one that belongs to a different account.

## Running a machine without the editor

A build box or a server has code you want to open from your laptop, but
nobody sits in front of it. `rune --headless` puts such a machine on the
network with no editor at all: no window, no terminal UI, just the node
and the workspace server its peers connect to.

```bash
rune --headless
```

:::tip[Keep it running]
A machine serves its workspaces only while `rune --headless` runs. To
start it at boot and keep it up, install it as a service: the
[Headless](./headless.md) guide has recipes for systemd, launchd, OpenRC,
runit, and Docker.
:::

It needs no display and no graphical libraries, so it runs on a minimal
server install or inside a container. Everything the editor would show
you goes to standard output, and the Rune log (`log_path`) is teed there
too, so `journalctl` or `docker logs` shows what the machine is doing.

The first run has no account signed in, so Rune prints a code to
authorize the machine:

```
To sign this machine in, open

    https://auth.rune.build/activate

in any browser and enter the code ABCD-EFGH (expires 14:32).
```

Open that page on any machine, laptop or phone, enter the code, and
Rune signs in as soon as you approve it. Nothing needs to reach the
machine running Rune: it only polls out, so no port forward or SSH
tunnel is involved.

The sign-in is cached in the data directory, so later runs go straight
to joining and print the node's name, state, and addresses. From then on
the machine shows up in `network peers` on your other machines and
`workspaceopen rune://<machine>/path` works against it.

A headless node serves the network and nothing else, so it needs
`network.auto_join` left on: with it off there is no console to run
`network up` in, and Rune says so and exits.

## Configuration

The network reads its settings from the `network` section of your
[Rune config](../config.md). The defaults suit most machines.

```yaml tab
network:
  auto_join: true
  hostname: ""
  port: 7473
```

```python tab
config["network"] = {
    "auto_join": True,
    "hostname": "",
    "port": 7473,
}
```

| Key | Type | Default | Effect |
| --- | --- | --- | --- |
| `auto_join` | bool | `true` | Join the network when Rune starts. With it off, the network stays one `network up` away, with no restart needed. |
| `hostname` | string | the machine's own hostname | The name this machine advertises, and the name used in its `rune://` addresses. |
| `port` | int | `7473` | The port workspaces are served on. It is reachable only from the network, never from the machine's other interfaces. |

Names are made unique when a machine joins, so a second machine with the
same hostname is given a suffixed name. `network peers` always shows the
exact name to put in a `rune://` URL.

## How it works

Rune embeds a userspace [WireGuard](https://www.wireguard.com/) node in
its own process. There is no daemon to install, nothing runs as root,
and the rest of your system's traffic is untouched: only Rune uses the
network.

When a machine joins, Rune's coordination server checks your account,
hands the machine short-lived credentials, and tells your machines about
each other. Your files and terminals do not travel through it. Traffic
goes directly between the two machines, encrypted end to end, and falls
back to an encrypted relay only when no direct path can be established.

Each machine serves its workspaces on port 7473 of its network address
only. Every request carries the identity of the machine behind it, and
Rune refuses any whose owning account is not yours, so sharing a network
is never enough to read a machine's files or run commands on it.

A machine's network identity is stored in your Rune data directory,
under `~/.rune/runenet`. `network down` leaves the network but keeps that
identity, so `network up` rejoins without a new sign-in.

## `rune://` and `ssh://`

Both schemes open a directory on another machine and behave the same way
once open. They differ in what they need from you:

| | [`ssh://`](./ssh.md) | `rune://` |
| --- | --- | --- |
| Addressed by | user, host, and port | machine name |
| Reachability | the host must be reachable over SSH | neither machine needs to be reachable from outside |
| Credentials | your SSH keys and `known_hosts` | your Rune account, nothing to manage |
| The other end runs | a workspace server that Rune starts over SSH | the Rune instance already running there |
| Toolchains | Rune mirrors your local language packages onto the host | the machine's own Rune install and packages |

Use `ssh://` for machines that are not yours, or that do not run Rune.
Use `rune://` for your own machines.

## Troubleshooting

**"The network links the machines you sign in on..."** No account is
signed in on this machine. Choose **Sign in**, or **Sign up** to create
an account.

**"The free Rune plan includes 2 machines on the network."** Your
account already has as many machines registered as the free plan covers.
Run `network machines` to see them and `network remove <machine>` to
free a slot, or choose **Upgrade**. After upgrading, run `login` again
so this machine picks up the change.

**"This machine has been removed from the network."** Someone ran
`network remove` for this machine from another one on your account. It
keeps working locally but is off the network. If that was a mistake, run
`network up` to register it again, which takes a slot back.

**A machine is missing from `network peers`.** Check that Rune is
running on it, that `network status` there reports state `Running`, and
that both machines are signed into the same account.

**`network status` prints an authorization URL.** The machine is waiting
to be authorized. Open the URL in a browser and it finishes joining.

**"peer not found in network."** The name in the `rune://` URL is not a
machine on your network. Use the name `network peers` reports, which is
the one the coordination server assigned and may differ from the
machine's local hostname.

**The connection is refused.** The other machine belongs to a different
account. Sign both machines into the same account.

**"not a member after 30s."** Joining is still in progress in the
background. Run `network status` to see whether it succeeded, and read
its `error` field if it did not.

**Workspaces stopped working after `network down`.** Leaving the network
disconnects open `rune://` workspaces. Run `network up` to rejoin.
