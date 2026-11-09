# Upgrades

Rune checks for new releases in the background and prompts you when one is
available. You can also trigger a check at any time, or turn the
background check off.

## How it works

Rune checks for a new version shortly after startup and once every 24
hours after that. When a new version is available, a floating prompt
offers three choices:

- **Upgrade Now** (`y`): download and install the new version, then
  ask you to restart Rune.
- **Remind Me Later** (`l`): dismiss the prompt and stay quiet until
  the next scheduled check.
- **Skip This Version** (`s`): never remind me about this specific
  version again.

To check immediately, regardless of the schedule, open the command
prompt and run the `upgrade` command. If you are already on the latest
version, Rune confirms it with a notification.

## Configuration

Configure upgrades under the `upgrade` section of your Rune config.

```yaml tab
upgrade:
  auto_check_enabled: true
  check_period: 24h
```

```python tab
config = {
    "upgrade": {
        "auto_check_enabled": True,
        "check_period": "24h",
    },
}
```

| Key                  | Type     | Default  | Effect |
|----------------------|----------|----------|--------|
| `auto_check_enabled` | bool     | `true`   | Whether to run the periodic check. |
| `check_period`       | duration | `"24h"`  | Interval between checks. |
| `channel`            | string   | `stable` | Reserved for future use. |

## Disabling auto-check

Set `auto_check_enabled` to `false` to stop background checks. The
`upgrade` command continues to work from the command prompt, so you
can still upgrade on demand:

```yaml tab
upgrade:
  auto_check_enabled: false
```

```python tab
config = {
    "upgrade": {
        "auto_check_enabled": False,
    },
}
```
