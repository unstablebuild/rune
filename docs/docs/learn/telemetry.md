---
sidebar_position: 99
description: Exactly what usage data Rune reports, what it never reports, and how to turn telemetry off.
---

# Telemetry

Rune reports a small amount of anonymous usage data so we can see which
features and languages people actually use, and which platforms and
versions need support. This page lists every field that leaves your
machine, and how to stop all of it with one setting.

New installs are prompted to choose whether to enable telemetry during the
first-run bootstrap wizard, and their choice is recorded in `config.yaml`.
Existing configurations without an explicit setting default to enabled.
Everything below is sent over HTTPS to `api.rune.build`.

## What Rune sends

There are exactly two reports.

### Once per launch

A system report is sent when Rune starts:

Example startup report:

```json
{
  "Type": "ClientSystem",
  "InstallID": "7570ed69-3cf0-4aa4-b817-57351e31a30e",
  "Tampered": false,
  "InstallIDErr": "",
  "SID": "ad560109-289a-45e3-bf74-e581ca3cc101",
  "Version": "v0.8.2",
  "EditorMode": "vim",
  "SystemArquitecture": "x86_64",
  "SystemOS": "linux",
  "SystemName": "nua",
  "SystemRelease": "7.2.3-arch1-3",
  "SystemVersion": "#1 SMP PREEMPT_DYNAMIC Sun, 06 Sep 2026 13:01:04 +0000"
}
```

| Field | Value |
| --- | --- |
| Install identifier | A random identifier generated the first time you run Rune. It is not derived from your hardware, your account, or anything else about you. |
| Reset flag | Whether the install identifier appears to have been reset. See [Your install identifier](#your-install-identifier). |
| Session identifier | A random identifier generated fresh on every launch. |
| Rune version | The version of the running build. |
| Editor mode | `vim`, `helix`, `standard`, `emacs`, or `exo`. |
| Operating system | For example `darwin` or `linux`. |
| Architecture | For example `arm64` or `x86_64`. |
| Kernel release and version | The kernel strings your operating system reports. |
| Host name | The machine's network name. |

### Once an hour

A usage report is sent every hour, and once more when you quit Rune.
Every counter is reset after a successful report, so each one covers
only the period since the last report:

Example usage report:

```json
{
  "Type": "ClientUsage",
  "SID": "ad560109-289a-45e3-bf74-e581ca3cc101",
  "EditorMode": "vim",
  "Opened": 14,
  "Closed": 11,
  "Edited": 58,
  "Flushed": 4,
  "WatchedChanges": 9,
  "Commands": 32,
  "Languages": {
    "go": 14,
    "markdown": 2,
    "python": 3
  }
}
```

| Field | Value |
| --- | --- |
| Session identifier | The same per-launch identifier as above. |
| Editor mode | `vim`, `helix`, `standard`, `emacs`, or `exo`. |
| Files opened, closed, edited, saved | Four counts. Counts only, never which files. |
| Watched file changes | How many files changed on disk under a language server's watch. |
| Commands run | How many commands you ran. |
| Languages | A count of files opened per language, for example `go: 14, python: 3`. |

The language list is normalized before it is sent. Rune derives the
language from the file type alone, discards the path, and reports
anything it cannot confidently identify as `other`. Unusually long
identifiers are treated the same way, and a single session reports at
most 64 distinct languages.

## What Rune never sends

Telemetry never includes:

- File names, paths, or workspace locations.
- File contents, or any fragment of them.
- Terminal output, command arguments, or shell history.
- Agent prompts, agent responses, or anything else you type.
- Your source code, in any form.

Rune's AI agent talks directly to the model provider you configure.
Prompts and code never pass through Unstable Build's servers.

## Your account

When you are signed in, the usage report is sent with your account
credentials attached, so reports can be associated with your
subscription. When you are signed out, the report is sent without them.

Signing out does not turn telemetry off. Use the setting below.

## Your install identifier

The install identifier lets us tell one installation apart from another
without knowing who you are. Rune keeps a second copy of it outside your
configuration directory so the identifier survives a reinstall. If the
primary copy is gone while the second copy remains, Rune restores the
identifier and sets the reset flag on its next report.

When telemetry is off, no install identifier is created, stored, or
copied anywhere.

## Turning telemetry off

Set `telemetry.enabled` to `false` in your Rune config:

```yaml tab
telemetry:
  enabled: false
```

```python tab
config["telemetry"]["enabled"] = False
```

Restart Rune for the change to take effect. From then on Rune sends no
usage or system reports at all, and stops collecting the counters that
would have gone into them. Nothing is queued for later and nothing is
written to disk.

## What this setting does not cover

Turning telemetry off does not silence the rest of Rune's network
activity, because none of it is background reporting:

- **Crash reports.** If Rune crashes, it asks on the next launch whether
  to send the report, and does nothing unless you agree. A crash report
  contains the stack trace and the tail of Rune's debug log, which can
  include workspace paths and file names. Decline it and the report stays
  on your machine.
- **Update checks.** Rune checks for new releases shortly after startup
  and once a day after that. The check sends no data about you. Turn it
  off with `upgrade.auto_check_enabled`, described in
  [Upgrades](./upgrades.md).
- **Signing in and package downloads.** Authentication and language
  package installs are things you ask Rune to do.
- **AI providers.** Agent traffic goes directly to the provider you
  configured, under that provider's own terms.
