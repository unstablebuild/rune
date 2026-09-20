---
sidebar_position: 31
---

# SSH workspaces

Open a directory on a remote machine and work in it as if it were local.
Rune runs the interface on your machine and everything else on the remote
host: your files, terminals, language intelligence, tasks, and agent all live
next to the code, over your existing SSH connection.

There is nothing new to learn. The [file explorer](./file-explorer.md),
terminals, and [extensions](../develop/extensions.md) behave exactly as they
do locally, because Rune routes them through the workspace's scheme without the
rest of the IDE needing to know whether the files sit on your disk or across an
SSH connection.

For your own machines, the [network](./network.md) opens the same kind of
workspace without SSH. The two are worth telling apart before you pick one:

| | `ssh://` | [`rune://`](./network.md) |
| --- | --- | --- |
| Reaches | any host you can already `ssh` to, whether or not it is yours | your own machines, signed into your Rune account |
| Through a NAT | needs a forwarded port or a jump host | traverses the NAT on its own, so a laptop in a cafe reaches a desktop at home |
| You manage | SSH keys, `known_hosts`, ports | nothing beyond signing in |

The network guide has the [full comparison](./network.md#rune-and-ssh).

## Getting started

### Install Rune on the remote

A remote workspace needs Rune installed on the remote machine. It can be any
machine you can reach over SSH, from a datacenter box to another laptop on your
network. Rune runs a lightweight workspace server there, over your existing SSH
connection, to serve files and run commands. Install it with the same one-line
installer used for a desktop install:

```
curl -fsSL https://rune.build/install.sh | sh
```

This installs Rune to `~/.local/rune.app` and links the binary at
`~/.local/bin/rune`. That is the only supported location on the remote, and
Rune looks for it there automatically. You do not need a graphical environment
on the remote, and you do not need to add anything to your `PATH`: Rune finds
`~/.local/bin/rune` even in the non-interactive shell that SSH uses to launch
the workspace server.

The remote must meet the same platform requirements as a Linux desktop install
(architecture and glibc). See the
[Prerequisites](/#prerequisites) for the details. The graphics requirement does
not apply to the remote: only the local machine renders the interface.

### Open the remote directory

On your local machine, open the [command prompt](./command-prompt.md) and run
`workspaceopen` with an `ssh://` URL:

```
workspaceopen ssh://user@host/path/to/project
```

The parts of the URL are:

- `user` is the remote account to log in as. Omit it (and the `@`) to use your
  local username.
- `host` is the remote machine's hostname or IP address. Add a port with
  `host:2222` when the remote does not listen on the default SSH port.
- `/path/to/project` is the directory on the remote host to open. A leading
  `~` expands to the remote user's home directory, so
  `ssh://user@host/~/project` opens `project` under that user's home.

Rune connects, starts the workspace server on the remote host, and opens the
directory as a new workspace. From here everything works as it does locally.

## Authentication

Rune authenticates using your existing SSH setup, so a host you can already
reach with the `ssh` command usually works without extra configuration.

- **Public keys.** By default Rune tries the standard keys in `~/.ssh`
  (for example `id_ed25519` and `id_rsa`). To use specific keys instead, set
  `private_keys` (see the [table below](#configuration)). When a key is
  passphrase-protected, Rune prompts you for the passphrase.
- **Passwords.** If key authentication is unavailable, Rune prompts for a
  password.
- **Host keys.** Rune verifies the remote against your `~/.ssh/known_hosts`
  file, exactly like the `ssh` command. When a host is not yet recorded
  (trust-on-first-use) or its key has changed, Rune prompts you once with three
  choices: **trust** to connect and record the key, **trust once** to connect
  for this session only without changing `known_hosts`, or **cancel** to abort.
  A recorded key is written to `known_hosts`, so future connections and the
  `ssh`/`git` commands all trust it. Set `strict_host_key_checking: false` to
  record new or changed keys automatically without a prompt, or `insecure: true`
  to skip host-key verification entirely. Point Rune at a different file with
  `known_hosts` when you keep host keys somewhere other than the default.

## Configuration

Remote connections read their settings from the `workspace.ssh` section of your
[Rune config](../config.md). The defaults suit most machines, so start with an
empty section and add keys only when a particular host needs them.

```yaml tab
workspace:
  ssh:
    timeout: "5s"
    private_keys:
      - "~/.ssh/id_ed25519"
```

```python tab
config = {
    "workspace": {
        "ssh": {
            "timeout": "5s",
            "private_keys": [
                "~/.ssh/id_ed25519",
            ],
        },
    },
}
```

| Key | Type | Default | Effect |
| --- | --- | --- | --- |
| `timeout` | duration | `"5s"` | How long to wait for the connection to establish before giving up. |
| `private_keys` | list of strings | standard `~/.ssh` keys | Private key files to offer for authentication. Set this to use specific keys instead of the defaults. |
| `known_hosts` | string | `~/.ssh/known_hosts` | Path to the `known_hosts` file used to verify the remote's host key. |
| `strict_host_key_checking` | bool | `true` | When on, an unknown or changed host key prompts you before it is trusted, then records the accepted key to `known_hosts`. Set to `false` to record new or changed keys automatically without a prompt (still safer than `insecure`, which skips verification). |
| `insecure` | bool | `false` | Skip host-key verification. Convenient for throwaway hosts, but it removes protection against a spoofed remote, so leave it off for anything you care about. |
| `kbd_interactive` | bool | `false` | Enable keyboard-interactive (PAM-style) authentication for hosts that require it. |
| `provision_packages` | bool | `true` | Mirror the language toolchains you use locally onto the remote when connecting. Set to `false` to skip provisioning, for example when the remote already has its toolchains managed. See [Language toolchains on the remote host](#language-toolchains-on-the-remote-host). |
| `command` | string | *(none)* | Delegate the connection to your system `ssh` binary instead of Rune's built-in client. Useful when you rely on ProxyJump, an SSH agent, or other options from your `~/.ssh/config`. Use `%h` for the host and `%p` for the port, for example `ssh -W %h:%p bastion`. |

### Using your system SSH configuration

Rune's built-in SSH client does not read `~/.ssh/config`, so options such as
jump hosts, per-host identity files, and custom proxies defined there are not
applied automatically. To reuse that setup, set `command` so Rune connects
through your system `ssh` binary, which does read `~/.ssh/config`:

```yaml tab
workspace:
  ssh:
    command: "ssh -W %h:%p"
```

```python tab
config = {
    "workspace": {
        "ssh": {
            "command": "ssh -W %h:%p",
        },
    },
}
```

Rune substitutes `%h` with the target host and `%p` with the port from the
workspace URL before running the command.

### Per-workspace config on the remote host

The `workspace.ssh` settings above live in your **local** Rune config: they
govern how the IDE dials the remote. Everything that runs *inside* the remote
workspace, on the other hand, sees the remote host's own environment: its
`$PATH`, its shell, and the tools installed on it.

A good practice is to check a `.rune/config.yaml` file into the remote project
(or drop one in the remote user's home) so the workspace carries its own
settings. This is the natural place to pin things that depend on where the code
actually runs, such as the paths to language servers and toolchains on that
host. See [Configuration](../config.md) for the full set of keys.

```yaml tab
extensions:
  go:
    config:
      lsp_path: "/usr/local/bin/gopls"
```

```python tab
config = {
    "extensions": {
        "go": {
            "config": {
                "lsp_path": "/usr/local/bin/gopls",
            },
        },
    },
}
```

### Language toolchains on the remote host

Language intelligence runs where your code runs. When you open a remote
workspace, the language servers and build tools (for example `gopls`, `ty`,
`rust-analyzer`, `cargo`, `uv`) execute on the **remote host**, not on your
local machine, so they need to be available on the remote. Rune handles this
for you.

When you open a remote workspace, Rune provisions the remote automatically. It
mirrors the language packages you already use locally onto the remote host,
installing them into the remote account's own Rune data directory and putting
their tools on the path the workspace uses. In practice this means a machine
that has never run Rune before comes up with the same Go, Python, and Rust
support you have locally, without a manual setup step. Provisioning runs each
time you connect, so a host stays in sync as you add or update packages on your
machine, and it only mirrors the versions you actually use.

While the remote is being provisioned, Rune shows its progress as it installs
each package. A large toolchain can take a moment on the first connection;
later connections reuse what is already installed and start quickly. If a
package cannot be installed on the remote, Rune reports it and still opens the
workspace, so a single missing toolchain never blocks your session.

If you manage the remote's toolchains yourself, or you would rather not install
anything on connect, turn provisioning off with `provision_packages: false` in
your local `workspace.ssh` config. Rune then leaves the remote untouched and
resolves tools from your config overrides and the remote's `$PATH` instead.

```yaml tab
workspace:
  ssh:
    provision_packages: false
```

```python tab
config = {
    "workspace": {
        "ssh": {
            "provision_packages": False,
        },
    },
}
```

Rune then resolves each tool by looking, in order, at:

1. an explicit override in your config (such as `extensions.go.config.lsp_path`),
2. the packages Rune provisioned on the remote and other well-known install
   locations for that toolchain, and
3. the command name on the remote host's `$PATH`.

This means most remotes just work. When you would rather manage a toolchain
yourself, or a tool lives somewhere non-standard, install it on the remote or
point Rune at it with the matching config key, ideally from the remote
[`.rune/config.yaml`](#per-workspace-config-on-the-remote-host) so the setting
travels with the project. An explicit override always wins over the provisioned
package, so you stay in control:

| Extension | Override key | Points at |
| --- | --- | --- |
| Go | `extensions.go.config.lsp_path` | the `gopls` binary on the remote host |
| Rust | `extensions.rust.config.lsp_path` | the `rust-analyzer` binary on the remote host |
| Python | `extensions.python.config.command` | the language-server command to run on the remote host |

## Troubleshooting

**"rune executable was not found on remote."** The remote does not have Rune
installed where Rune expects it. Run the installer from
[Install Rune on the remote](#install-rune-on-the-remote) and confirm the
binary exists at `~/.local/bin/rune` for the account you connect as.

**The connection is refused or times out.** Verify you can reach the host with
the plain `ssh` command first. If `ssh user@host` works but Rune does not,
check that the port in the URL matches the remote, and that any jump hosts or
proxy settings from your `~/.ssh/config` are carried over with the `command`
setting.

**Host key verification fails.** For a remote whose host key is not in your
`known_hosts`, or whose key has changed, Rune prompts you once to trust the key.
Choose **trust** to record it, **trust once** to connect for this session only
without changing `known_hosts`, or **cancel** to refuse the connection and leave
`known_hosts` unchanged. Cancel when you did not expect the change, as it can
indicate a man-in-the-middle. To record new or changed keys without a prompt,
set `strict_host_key_checking: false`; to skip verification entirely,
set `insecure: true`.

**`known_hosts` can't be read.** If the file exists but a line is malformed,
Rune can't verify the host against it and won't rewrite it. It prompts you to
**trust once** and connect for this session only; fix the file to record the
host permanently.

**Language features do not work on a remote workspace.** Rune provisions the
language packages you use locally onto the remote when you connect, and reports
any package it could not install. If a language still does not work, the tool
may live somewhere non-standard on the host, or you may be managing it yourself
outside Rune. Install the toolchain on the remote, or set the matching override
key from the remote `.rune/config.yaml`. See
[Language toolchains on the remote host](#language-toolchains-on-the-remote-host).
