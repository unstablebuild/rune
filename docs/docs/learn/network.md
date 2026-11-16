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
through Rune's coordination server and punch through the NATs in front
of them, so from a cafe you can open the workspaces on the machine at
home without opening a hole in your home network's firewall.

A `rune://` workspace behaves exactly like an [SSH
workspace](./ssh.md) once it is open: files, terminals, language
intelligence, tasks, and the agent all run on the other machine, next to
the code.

## What you need

- **A Rune account.** Run `login` in the [Rune console](./console.md) to
  sign in; Rune offers to sign you in or sign you up when you are not.
  Free covers up to 2 nodes on the network, Rune Pro covers up to 10,
  and Enterprise includes unlimited nodes. If your account reaches its
  limit, remove a node with `network remove` or upgrade your plan;
  after upgrading, run `login` again so the machine picks up the new
  plan.
- **Rune running on both machines, signed into the same account.** A
  machine serves its workspaces from the Rune instance running on it. If
  Rune is not open there, the machine is not reachable, and only your own
  machines are ever allowed to connect. A machine nobody sits at can run
  Rune as a service instead; see [Headless](./headless.md).

## Joining the network

Rune joins on startup, when you're logged in, so usually there is nothing to do. Check
where this machine stands with `network status` in the console. Otherwise,
you can connect with:

```
network up
```

### The `network` command

`network` is a [Rune console](./console.md) command: open the console and
run it as `network <subcommand>`, or submit a one-off from the
[command prompt](./command-prompt.md) with `console network <subcommand>`.

| Command | What it does |
| --- | --- |
| `network status` | Show this machine's name, addresses, and sign-in state, including the last error and any pending authorization URL. |
| `network peers` | List the other machines connected to the network, with the `rune://` address each one is reachable at. |
| `network ping <machine>` | Ping another machine on the network and show how each ping got there, directly or through a relay, and how long it took. |
| `network machines` | List every machine registered to your account, whether or not it is on the network right now, and which one you are on. |
| `network remove <machine>` | Unregister a machine from your account, freeing the slot it held. |
| `network up` | Join the network and wait until this machine is a member. |
| `network down` | Leave the network. Open `rune://` workspaces stop working until you run `network up` again. |
| `network help` | Show the command's usage. |

When your account reaches its limit, another machine cannot join until
you remove one or upgrade. `network machines` shows what is registered
to your account:

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
slot back. Free and Rune Pro accounts have node limits even when a
registered machine is offline. Enterprise has no node limit.

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
  `network peers`. You rarely need to look it up: see
  [Completing machines and directories](#completing-machines-and-directories).
- `/src/project` is the directory to open on that machine. Any path you
  could open locally there works. Leave it out (`rune://carbon/`) to open
  that machine's home directory.

A `rune://` URL takes no user name: machines authenticate by their own
identity, not by an account on the other end, so `rune://user@carbon/`
is rejected.

Offline machines are listed by `network peers`, and you can still type
one out by hand. Opening it waits and
connects as soon as it comes back, which is also what happens when a
machine sleeps or changes networks mid-session: Rune reconnects on its
own and the workspace carries on. Two failures are final rather than
retried, because retrying cannot fix them: a machine that is not on the
network at all, and one that belongs to a different account.

### Completing machines and directories

Typing `rune://` at the `workspaceopen` prompt lists the machines that
are online right now, so jumping to another machine is a pick from the
list:

```
workspaceopen rune://
```

Pick a machine and completion moves on to its directories, which Rune
asks that machine for as you type, one level at a time.

### Checking the connection to a machine

`network ping` shows how this machine reaches another one:

```
network ping carbon
```

```
pong from carbon (100.64.0.2) via DERP(fra) in 31ms
pong from carbon (100.64.0.2) via 192.168.1.5:41641 in 2ms
```

Each line is one ping, with the way it went and its round trip.
`via DERP(<region>)` means it went through a relay server, because the
two machines could not reach each other directly yet. An address such as
`192.168.1.5:41641` means it went straight to the other machine. The
machines keep looking for a direct path, so the first pings are often
relayed. `network ping` stops at the first direct reply, or after 10
pings. Press `Tab` after `network ping` to complete the machine name.

## Connecting to a server with no display

```bash
rune --headless
```

A machine serves its workspaces only while `rune --headless` runs. To
start it at boot and keep it up, install it as a service: the
[Headless](./headless.md) guide has recipes for systemd, launchd, OpenRC,
and runit. On a host with Docker there is nothing to install: the
[`unstablebuild/rune` image](./headless.md#docker) is a ready-made node.

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

Rune embeds a [Tailscale](https://tailscale.com) node in its own process. There is no
daemon to install, nothing runs as root, and the rest of your system's traffic is
untouched: only Rune uses the network.

When a machine joins, Rune's coordination server checks your account,
hands the machine short-lived credentials, and tells your machines about
each other. Your files and terminals do not travel through it: knowing
where to find each other, the two machines hole-punch through their NATs
and talk directly, encrypted end to end, and fall back to an encrypted
relay only when no direct path can be established.

Each machine serves its workspaces on port 7473 of its network address
only. Every request carries the identity of the machine behind it, and
Rune refuses any whose owning account is not yours, so sharing a network
is never enough to read a machine's files or run commands on it.

## `rune://` and `ssh://`

Both schemes open a directory on another machine and behave the same way
once open. They differ in what they need from you:

| | [`ssh://`](./ssh.md) | `rune://` |
| --- | --- | --- |
| Addressed by | user, host, and port | machine name |
| Reachability | the host must be reachable over SSH, so a NAT or firewall in front of it needs a forwarded port | neither machine needs to be reachable from outside; Rune traverses the NATs between them |
| Credentials | your SSH keys and `known_hosts` | your Rune account, nothing to manage |
| The other end runs | a workspace server that Rune starts over SSH | the Rune instance already running there |

Use `ssh://` for machines that are not yours, or that do not run Rune.
Use `rune://` for your own machines.

If your machines are already on a Tailscale network, Rune also opens
workspaces through it, with no Rune account: see
[Tailscale](./tailscale.md).

## Troubleshooting

**"The network links the machines you sign in on..."** No account is
signed in on this machine. Choose **Sign in**, or **Sign up** to create
an account.

**The account has reached its node limit.** Free allows up to 2 nodes;
Rune Pro allows up to 10. On Free, the message reads **"The free Rune
plan includes 2 machines on the network."**
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

**Completion after `rune://` is empty.** No other machine is online, or
this machine is not on the network. Run `network status` to check this
machine and `network peers` to see the state of the others.

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

**Workspaces on another machine feel slow.** Run `network ping
<machine>`. If it ends with **"direct connection not established"**, the
traffic between the two machines goes through a relay server, which
adds its round trip to everything you do there. A firewall that blocks
UDP between the machines is the usual cause.
