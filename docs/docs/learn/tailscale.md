---
sidebar_position: 33.5
---

# Tailscale

If your machines are already on a [Tailscale](https://tailscale.com)
network, Rune on any of them can open a workspace on any other, with
nothing to set up:

```
workspaceopen tailscale://<machine>/path/to/project
```

Rune goes through the Tailscale client each machine already runs. It
does not join your tailnet as another device, it needs no Rune account,
and there is no configuration: every machine running Rune serves its
workspaces to the tailnet for as long as Tailscale is connected, and
Tailscale's own ownership and policy decide who may open them.

Everything Rune does on a [workspace](./workspaces.md) works the same
on one opened this way: the [file explorer](./file-explorer.md),
terminals, language servers, tasks and the agent all run on the machine
that holds the code.

## What you need

- Tailscale installed and connected on both machines, to the same
  tailnet. `tailscale status` lists the machines each one can see.
- Rune running on the machine whose workspace you open. It serves only
  while it runs; a server nobody sits at runs [`rune
  --headless`](./headless.md).
- A tailnet policy that lets the two machines reach each other on TCP
  port 7473. The policy a new tailnet starts with does.
- A Tailscale client that routes tailnet traffic through the operating
  system. The macOS apps, the Windows app and `tailscaled` on Linux all
  do; a client in [userspace networking
  mode](https://tailscale.com/docs/concepts/userspace-networking) does
  not, and Rune can neither serve nor open workspaces through it. See
  [the macOS variants](https://tailscale.com/docs/concepts/macos-variants)
  if you are unsure which client a Mac runs.

## Opening a workspace

`tailscale://` URLs name a machine by its
[MagicDNS](https://tailscale.com/docs/features/magicdns) name, the first
label of it as `tailscale status` shows it, then a path on that machine:

```
workspaceopen tailscale://workstation/home/me/src/app
```

The full MagicDNS name and the machine's tailnet address work too. A URL
without a path, `tailscale://workstation/`, opens your home directory
there.

Type `workspaceopen tailscale://` and completion offers the online
machines that serve workspaces and could let you in. Pick one and it
offers that machine's directories as you type. To tell which machines
serve, Rune connects to port 7473 of each machine you own and of each
tagged machine, and remembers the answers for a minute. Machines that
belong to other users are not asked: they never let you in.

## Who can open a workspace

A workspace is a shell on the machine it is opened on: its files, its
terminals, any command. Rune treats opening one the way [Tailscale
SSH](https://tailscale.com/docs/features/tailscale-ssh) treats a login,
and never lets in a machine Tailscale SSH would turn away:

| The machine serving the workspace is | Who can open a workspace on it |
| --- | --- |
| owned by a user (untagged) | That user's other untagged machines, and no one else, whatever the policy says. |
| [tagged](https://tailscale.com/docs/features/tags) | Machines the policy [grants](#granting-access-to-a-tagged-machine) `rune.build/cap/workspace` on it, and machines an `accept` rule in the policy's `ssh` section lets log in as the local user Rune runs as. |

So on a tailnet only you use, all your machines open workspaces on each
other, and on a tailnet you share with others nobody gets into your
machines. A tagged machine, such as a shared build server, lets in only
whom the policy names.

A few more rules come from Tailscale SSH:

- A rule in the `ssh` section applies as Tailscale SSH applies it: the
  first rule that matches the machine and maps the requested user to a
  local one decides. `autogroup:nonroot` never matches `root`.
- Rules with `"action": "check"` and rules that record sessions only
  Tailscale SSH can enforce, so they let no one in. Grant
  `rune.build/cap/workspace` to the machines that should still get in.
- Rune refuses connections from the serving machine's own tailnet
  address. Any user of that machine could make one, and Tailscale would
  vouch for each of them as the machine itself.

A policy change reaches the machine within seconds. A workspace that is
already open is checked against it again every 10 seconds.

### Granting access to a tagged machine

Grant the [app capability](https://tailscale.com/docs/features/access-control/grants/grants-app-capabilities)
`rune.build/cap/workspace` to the machines that may open workspaces on
a tagged one, and let them reach its port 7473:

```json
{
  "grants": [
    {
      "src": ["group:dev"],
      "dst": ["tag:buildbox"],
      "ip": ["tcp:7473"],
      "app": {"rune.build/cap/workspace": [{}]}
    }
  ]
}
```

Or let them log in as the user Rune runs as, here `rune`, as you would
for Tailscale SSH. An `ssh` rule does not open the port by itself, so the
policy must still let them reach port 7473:

```json
{
  "ssh": [
    {"action": "accept", "src": ["group:dev"], "dst": ["tag:buildbox"], "users": ["rune"]}
  ]
}
```

The [grants](https://tailscale.com/docs/features/access-control/grants)
and [policy file syntax](https://tailscale.com/docs/reference/syntax/policy-file)
references cover the rest, and [targets and
selectors](https://tailscale.com/docs/reference/targets-and-selectors)
lists what `src` and `dst` accept.

## What keeps machines apart

Each of these stops one machine from opening workspaces on another, even
when Rune would let it in:

- **The policy does not let them reach each other.** A connection that
  no rule allows is dropped, and opening the workspace times out. Allow
  TCP port 7473 to the serving machine in the policy's
  [ACLs](https://tailscale.com/docs/features/access-control/acls) or
  [grants](https://tailscale.com/docs/features/access-control/grants).
  [The ACL examples](https://tailscale.com/docs/reference/examples/acls)
  show common setups.
- **The serving machine blocks incoming connections.** Turn on "Allow
  incoming connections", the opposite of shields up, in its [client
  preferences](https://tailscale.com/docs/features/client/manage-preferences).
- **A machine is waiting for approval.** On a tailnet with [device
  approval](https://tailscale.com/docs/features/access-control/device-management/device-approval),
  an unapproved machine reaches nothing.
- **A machine's key expired.** Sign it in again, or turn off [key
  expiry](https://tailscale.com/docs/features/access-control/key-expiry)
  for it.
- **A machine was tagged.** A tagged machine belongs to its tags, not to
  you, so tagging one of your machines locks your other machines out of
  it until the policy grants them access, and tagging the one you open
  workspaces from locks it out of your untagged machines for good.
- **The machine was [shared](https://tailscale.com/docs/features/sharing)
  with you.** It belongs to whoever shared it. An untagged one never lets
  you in; a tagged one lets in whom its owners' policy names.

## Machines several people use

Who may open a workspace depends on the Tailscale identity of the
machine, not on the local account Rune runs as. On a machine signed into
Tailscale as you where other people have local accounts, the first of
them to run Rune serves their own account to all your machines, as long
as that Rune runs. Tag such a machine, and let people in with `ssh`
rules that name their local users, as you would for Tailscale SSH: Rune
then admits a machine only when a rule lets it log in as the user that
particular Rune runs as.

## Servers and containers

A machine without Tailscale, such as a container, can still serve its
workspaces to your tailnet: `rune --headless --control-url` joins it as
a machine of its own, signed in by URL. See
[Headless](./headless.md#tailscale).

## How it works

Rune reads the tailnet from the local Tailscale client: the machines on
it, who owns each one, and the `ssh` rules of the policy that apply to
this machine. Any local process may read these, and Rune changes
nothing in the client.

Each machine serves its workspaces on port 7473 of its tailnet addresses
only, never on its other networks, and starts and stops serving as
Tailscale connects and disconnects. For every request, Rune asks the
client which machine it comes from. That answer cannot be forged:
packets only arrive from a tailnet address once the machine behind it
proved its identity to Tailscale.

## `tailscale://` and `rune://`

| | `tailscale://` | [`rune://`](./network.md) |
| --- | --- | --- |
| Network | your own tailnet, through the Tailscale client | Rune's own network, built into Rune |
| Account | none | a Rune account on every machine |
| Who gets in | Tailscale's ownership and your policy | the machines of your Rune account |
| Machines | any that run Tailscale and Rune | any that run Rune |

## Troubleshooting

**"this machine is not connected to Tailscale."** Start Tailscale and
sign it in. `tailscale status` should list your other machines.

**"peer not found in network."** The name in the URL is not a machine of
your tailnet. Use the name `tailscale status` shows.

**Opening the workspace times out.** Rune is not running on the other
machine, or something on [this list](#what-keeps-machines-apart) keeps
the two apart. `tailscale ping <machine>` tells the two cases apart.

**"belongs to ..., not to ..."** The serving machine is owned by another
user, who is the only one who can open workspaces on it.

**"is a tagged machine."** A tagged machine never opens workspaces on a
machine owned by a user. Open them from one of your untagged machines.

**"may not open workspaces here."** The serving machine is tagged, and
the policy neither grants your machine `rune.build/cap/workspace` on it
nor lets it log in as the user Rune runs as there. See [Granting access
to a tagged machine](#granting-access-to-a-tagged-machine).

**"is a check rule"** or **"records sessions."** The `ssh` rule that
matches can only be enforced by Tailscale SSH. Make it an `accept` rule
without session recording, or grant `rune.build/cap/workspace`.

**"connections from this machine's own tailnet address are refused."**
The URL names the machine you are on. Open the directory as a local
workspace instead.

**Completion after `tailscale://` is empty.** No machine that could let
you in is online and serving: check that Rune runs on the other machine
and that it is yours or tagged.

**The log says "not serving workspaces on ...: address already in
use".** Another Rune on the machine, run by another local user, already
serves on that address.
