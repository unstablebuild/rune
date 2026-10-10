# Aliases

An alias is a command that you create by combining one or multiple
command and arguments. Typing the alias at the
[command prompt](./command-prompt.md) runs the commands in order
as a single dispatch, so aliases are how you turn a recurring sequence
into one keystroke (or one binding via `keymap.bindings`).

The simplest form is one alias → one command:

```yaml tab
command:
  aliases:
    e: "edit"
    docs: "! man $LANG"
    fmt: "!! gofmt -w $FILE"
```

```python tab
"aliases": {
    "e": "edit",
    "docs": "! man $LANG",
    "fmt": "!! gofmt -w $FILE",
},
```

Now `:e foo.go` dispatches `edit foo.go`, `:docs` opens the manpage for
the focused file's language, and `:fmt` formats the focused Go file in
place.

Aliases accept positional arguments via `$1` … `$9`:

```yaml tab
command:
  aliases:
    gitblame: "! git blame -L $1 $FILE"
```

```python tab
"aliases": {
    "gitblame": "! git blame -L $1 $FILE",
},
```

`:gitblame 42,50` runs `git blame -L 42,50` on the focused file.
Calling an alias with fewer arguments than it references is a dispatch
error; use `${1:-default}` if the argument should be optional.

## Multi-step aliases

An alias can list several commands. Each step dispatches in order, and
a `!!` step can capture variables that later steps see as `$VAR`:

```yaml tab
command:
  aliases:
    worktreenew:
      - '!! git worktree add "$RUNE_DATADIR/wt/$1" -b $1'
      - "workspaceopen $RUNE_DATADIR/wt/$1"
```

```python tab
"aliases": {
    "worktreenew": [
        '!! git worktree add "$RUNE_DATADIR/wt/$1" -b $1',
        "workspaceopen $RUNE_DATADIR/wt/$1",
    ],
},
```

`:worktreenew feature/x` creates the worktree on disk, then opens it as
a new workspace: one keystroke, two commands, no wrapper script.

The rest of this guide is the full variable and operator reference
(defaults, captures, prefix stripping, arithmetic, ...) that alias
bodies and command arguments draw on.

# Command Variables

Rune expands a set of `$VAR` placeholders every time it dispatches a command. The variables resolve from the editor's live context (the focused file, the cursor, the current workspace, your aliases' positional arguments), and a built-in POSIX-style expansion engine is run over the result, so you can shape values with prefix and suffix trimming, default values, case changes, substring slicing, and arithmetic.

The result is that an alias body or a command argument can refer to the focused file, the current workspace, or a positional argument without writing Go, without writing a wrapper script, and without leaving the prompt.

## A first example

The shortest useful example is an alias that runs `git blame` on whatever you have focused:

```yaml tab
command:
  aliases:
    gitblame: "! git blame $FILE"
```

```python tab
"aliases": {
    "gitblame": "! git blame $FILE",
},
```

`:gitblame` resolves `$FILE` to the focused tab's path, splits the result into command arguments, and runs `git blame /path/to/file.go` in a transient terminal, no wrapper script required.

The same machinery powers alias positional arguments. This alias takes one argument, the new worktree name, and uses it twice while also referencing the workspace identity:

```yaml tab
command:
  aliases:
    worktreenew:
      - '!! ROOT=$(git rev-parse --git-common-dir) && ROOT=$(cd "$ROOT/.." && pwd) || exit 1'
      - '!! ROOT_HASH=$(printf %s "$ROOT" | (sha256sum 2>/dev/null || shasum -a 256) | cut -c1-4)'
      - '!! ROOT_NAME=${ROOT##*/}'
      - '!! WORKTREE=$RUNE_DATADIR/worktrees/$ROOT_NAME-$ROOT_HASH/$1'
      - '!! git worktree add "$WORKTREE" -b $1'
      - "workspaceopen $WORKTREE"
```

```python tab
"aliases": {
    "worktreenew": [
        '!! ROOT=$(git rev-parse --git-common-dir) && ROOT=$(cd "$ROOT/.." && pwd) || exit 1',
        '!! ROOT_HASH=$(printf %s "$ROOT" | (sha256sum 2>/dev/null || shasum -a 256) | cut -c1-4)',
        '!! ROOT_NAME=${ROOT##*/}',
        '!! WORKTREE=$RUNE_DATADIR/worktrees/$ROOT_NAME-$ROOT_HASH/$1',
        '!! git worktree add "$WORKTREE" -b $1',
        "workspaceopen $WORKTREE",
    ],
},
```

`:worktreenew feature/x` creates a worktree on disk under a workspace-keyed directory, then opens it as a new workspace.

## Where expansion runs

- every command or argument in a command alias body, before the alias is dispatched;
- every argument of a command you dispatch directly via `!` or `!!`;
- the `command` template for `mode: exo` editor, before the guest editor is launched;
- commands used as shell-backed completers `command.aliases.<your-command>.completer`:
```yaml
command:
  aliases:
    gitcheckout:
      command: '!! git -C $WORKSPACE_PATH checkout $1'
      completer: '! git -C $WORKSPACE_PATH branch --format="%(refname:short)"'
```

Here `$WORKSPACE_PATH` is doing real work in the completer: it pins both the suggestions and the dispatched command to the focused workspace's git repo, so `:gitcheckout` keeps working regardless of where the Rune process was launched from and which workspace happens to be focused right now.

## Variables Rune provides

### Positional

| Variable | Value |
| --- | --- |
| `$1` … `$9` | The Nth positional argument passed to the alias, or empty if none. Referencing a `$N` higher than the number of arguments supplied is a dispatch error, not silent empty. |

Positional variables are only meaningful inside an alias body. They are removed from the dispatched argument list so the alias body fully controls where each argument lands.

### Focused file

| Variable | Value |
| --- | --- |
| `$FILE` | Local path of the focused tab (e.g. `/Users/me/src/proj/main.go`). |
| `$FILE_URI` | Full URI of the focused tab (e.g. `file:///Users/me/src/proj/main.go`). |
| `$FILE_DIR` | Directory portion of `$FILE`. |
| `$FILE_BASENAME` | Last path component of `$FILE` (e.g. `main.go`). |
| `$FILE_STEM` | `$FILE_BASENAME` without its extension (e.g. `main`). |
| `$FILE_EXT` | Extension of `$FILE` without the leading dot (e.g. `go`). |
| `$FILE_REL` | `$FILE` rewritten relative to the current workspace root. |
| `$LANG` | Detected language ID for `$FILE` (e.g. `go`, `python`). Empty if Rune cannot detect it. |

All of these are empty when nothing is focused or when the focused tab is not a file (e.g. a terminal pane).

### Cursor and selection

| Variable | Value |
| --- | --- |
| `$LINE` | 1-based line number of the cursor. Empty when there is no editor focused. |
| `$COLUMN` | 1-based column number of the cursor. Empty when there is no editor focused. |
| `$SELECTION` | Currently selected text. Empty when there is no selection. |
| `$WORD` | Identifier under the cursor (`[A-Za-z0-9_]` run). Empty on whitespace or punctuation. |

### Workspace

| Variable | Value |
| --- | --- |
| `$WORKSPACE` | Sanitised basename of the focused workspace (e.g. `proj`). |
| `$WORKSPACE_HASH` | A short, stable hex digest of the workspace's URI. Two workspaces sharing a basename (for example a local repo and a remote checkout of the same project) produce distinct hashes so derived paths do not collide. |
| `$WORKSPACE_URI` | Full URI of the focused workspace. |
| `$WORKSPACE_PATH` | Path portion of `$WORKSPACE_URI`. |
| `$RUNE_DATADIR` | The Rune data directory (by default `~/.rune`) on the machine that hosts the focused workspace, which is the remote machine for a remote workspace. |

Every workspace variable resolves for every supported workspace type. Commands dispatched through an alias run inside the workspace's own filesystem, whether that workspace is local or remote over SSH, so the workspace URI is a meaningful identifier in all cases. A `worktreenew` alias keyed on `$RUNE_DATADIR/worktrees/$WORKSPACE-$WORKSPACE_HASH/$1` does the right thing whether you triggered it from a local repo or from a workspace mounted over SSH.

### Environment fallback

Any `$VAR` that Rune does not recognise falls through to the process environment, so `$HOME`, `$SHELL`, and any other exported variable resolve as expected.

### Escaping

Two consecutive dollar signs produce a literal `$`. Use `$$` whenever you want the next character to read as a literal dollar sign rather than the start of a variable:

```yaml tab
command:
  aliases:
    echo: "! echo $$PATH"   # prints the literal text "$PATH"
    echo1: "! echo $$1"     # prints the literal text "$1"
```

```python tab
"echo": '! echo $$PATH',          # prints the literal text "$PATH"
"echo": '! echo $$1',             # prints the literal text "$1"
```

## Shape the value before it lands as an argument

Each `$VAR` reference can be wrapped in `${…}` and decorated with POSIX-style operators. These all run **before** the value reaches the command handler, so you can normalise a path, fill in a default, or extract a substring inline, without delegating to an external shell utility.

### Default and alternative values

| Form | Behaviour |
| --- | --- |
| `${VAR:-fallback}` | Use `fallback` when `$VAR` is empty or unset. |
| `${VAR-fallback}` | Use `fallback` only when `$VAR` is unset (an empty value stays empty). |
| `${VAR:+yes}` | Substitute `yes` when `$VAR` is non-empty; otherwise empty. |
| `${VAR:?message}` | Refuse to dispatch with `message` if `$VAR` is empty or unset. |

Example: open a documentation pager that defaults to the focused language but accepts an override.

```yaml tab
command:
  aliases:
    doc: "! man ${1:-$LANG}"
```

```python tab
"doc": "! man ${1:-$LANG}",
```

`:doc` shows docs for the current language; `:doc python` overrides.

### Substring and length

| Form | Behaviour |
| --- | --- |
| `${VAR:offset}` | Substring starting at `offset` (0-based). |
| `${VAR:offset:length}` | Substring of given length. |
| `${#VAR}` | Length of `$VAR` in characters. |

### Prefix and suffix trimming

| Form | Behaviour |
| --- | --- |
| `${VAR#pattern}` | Strip shortest matching prefix. |
| `${VAR##pattern}` | Strip longest matching prefix. |
| `${VAR%pattern}` | Strip shortest matching suffix. |
| `${VAR%%pattern}` | Strip longest matching suffix. |

Example: derive a sibling test file path from `$FILE`.

```yaml tab
command:
  aliases:
    opentest: "edit ${FILE%.go}_test.go"
```

```python tab
"opentest": "edit ${FILE%.go}_test.go",
```

`pattern` is a glob (`*`, `?`, `[…]`), not a regular expression.

### Search and replace

| Form | Behaviour |
| --- | --- |
| `${VAR/pattern/replacement}` | Replace first match of `pattern` with `replacement`. |
| `${VAR//pattern/replacement}` | Replace every match. |
| `${VAR/#pattern/replacement}` | Replace a match anchored at the start. |
| `${VAR/%pattern/replacement}` | Replace a match anchored at the end. |

### Case conversion

| Form | Behaviour |
| --- | --- |
| `${VAR^}` / `${VAR^^}` | Uppercase first character / every character. |
| `${VAR,}` / `${VAR,,}` | Lowercase first character / every character. |

### Arithmetic

`$((expression))` evaluates an integer expression. Operands may be literal numbers or other variables.

```yaml tab
command:
  aliases:
    prev: "jumptoline $((LINE - 1))"
    halfway: "jumptoline $((LINE / 2))"
```

```python tab
"prev": "jumptoline $((LINE - 1))",
"halfway": "jumptoline $((LINE / 2))",
```

The full set of operators mirrors POSIX shell arithmetic: `+ - * / % **`, bitwise `& | ^ ~ << >>`, comparisons `== != < <= > >=`, logical `! && ||`, and the ternary `? :`.

## What Rune does not allow

Expansion in Rune is intentionally stricter than a POSIX shell. Specifically:

- **No command substitution outside `!`/`!!`.** `$(…)` and backticks are rejected at expansion time for plain (non-shell) alias steps, so a regular alias body cannot silently spawn a subprocess every time the command runs. Prefix the step with `!` or `!!` to run the whole body through Rune's shell interpreter; there `$(…)`, pipelines, and assignments are first-class, and assignments are even captured for subsequent alias steps (see [Shell-backed alias steps](#shell-backed-alias-steps--and-)).
- **No globbing.** A `$VAR` whose value contains `*`, `?`, or `[…]` stays literal at dispatch time; pattern expansion happens later, inside your shell, only when an alias is `!`-or-`!!`-prefixed.
- **No field splitting.** `$VAR` always lands as a single argument, even if its value contains spaces. This differs from POSIX shells by design: alias bodies do not need to defensively wrap variables in quotes.
- **Strict positional checking.** Referencing `$5` when only three arguments were supplied is a dispatch error, not silent empty. Use `${5:-default}` when you want a defaulted optional argument.

## Shell-backed alias steps (`!` and `!!`)

An alias step that starts with `!` or `!!` runs its body through Rune's built-in, in-process
shell interpreter instead of dispatching it as a Rune command. The two forms differ only in how
their output is surfaced:

- `!body` opens a floating terminal that shows the command's stdout/stderr live.
- `!!body` runs the body headlessly: stdout is discarded, stderr is buffered and only surfaces
  if the command fails. This is the form aliases use when they only care about side effects
  (e.g. `git worktree add`, `sed -i`, …) or when the output is meant to feed the *next* alias
  step rather than the screen.

Both forms support standard shell syntax (pipelines, redirections, command substitutions, and
assignments) and execute on the focused workspace's host. Local (`file://`) and remote
(`ssh://`) workspaces run the same alias bodies.

### Capture a value in one step, reuse it in later commands

Multi-command aliases can capture variables in one step and expand them in any subsequent
step of the same dispatch. This is what makes `!`/`!!` steps composable with the rest of the
alias: a shell step can compute a value (from the filesystem, from `git`, from any host
command) and the next Rune command in the chain receives it as a plain `$VAR`.

Concretely: inside a `!` or `!!` step, any plain string assignment performed by the shell line
is captured and becomes visible to the *subsequent* steps of the same alias dispatch as a
`$VAR` expansion. That includes assignments fed by command substitution:

```yaml tab
command:
  aliases:
    worktreeopen:
      - '!! WORKTREE=$(git worktree list --porcelain | awk -v name="$1" ''$1=="worktree" && $2 ~ "/" name "$" {print $2; exit}'')'
      - "workspaceopen $WORKTREE"
      - "workspaceready workspacerename $1"
```

```python tab
"worktreeopen": [
    '!! WORKTREE=$(git worktree list --porcelain | awk -v name="$1" \'$1=="worktree" && $2 ~ "/" name "$" {print $2; exit}\')',
    "workspaceopen $WORKTREE",
    "workspaceready workspacerename $1",
],
```

The first step runs `git worktree list` on the focused workspace's host and assigns the
resolved path to `WORKTREE`. The next step expands `$WORKTREE` against the same alias dispatch,
so the alias works whether it was invoked from the parent repository or from inside one of the
worktrees; the absolute path is determined by `git`, not by Rune.

Captures behave like a small, per-alias scope:

- They are scoped to a single alias dispatch; the chain is created when the alias starts and
  is discarded after the last step. Two separate `:worktreeopen` invocations do not share state.
- They shadow the process environment for the duration of the dispatch, but they do **not**
  shadow Rune builtins like `$FILE`, `$WORD`, `$LANG`, … Pick variable names that do not collide
  with the builtins listed above (a safe convention is to use a name that hints at the alias's
  domain, e.g. `WORKTREE` for worktree aliases).
- Only plain string assignments are captured. Array assignments and shell-internal variables
  (`HOME`, `PWD`, `UID`, `EUID`, `GID`, `IFS`, `OPTIND`, `OLDPWD`) are excluded.
- `!` steps share the same machinery, but because their stdout is shown to the user, captures
  are still recorded and visible to subsequent steps in the same chain.

### Inline expansion vs. shell expansion

Rune expands the `$1`…`$9` positional arguments and any Rune builtin or environment variable in
the `!`/`!!` body *before* handing the line to the shell interpreter. Each substituted value is
shell-quoted, so a positional argument that contains whitespace or shell metacharacters becomes
a single field instead of being re-split. Names that Rune does not know are left in the body
unchanged; the shell interpreter then resolves them against the host environment (or the chain
captures from earlier steps).

You can defer expansion to the shell by escaping the dollar with `$$`:

- `$$VAR` in the alias body becomes a literal `$VAR` at dispatch time, which the shell
  then expands against its own environment;
- `$(…)` and backticks are always passed through verbatim, so the shell handles command
  substitution.

## End-to-end examples

### Search the focused symbol with `rg`

```yaml tab
command:
  aliases:
    rgword: "! rg --color=always ${WORD:?cursor must be on a word} $WORKSPACE_PATH"
```

```python tab
"rgword": "! rg --color=always ${WORD:?cursor must be on a word} $WORKSPACE_PATH",
```

### Open the test file next to the focused source

```yaml tab
command:
  aliases:
    opentest: "edit ${FILE%.go}_test.go"
```

```python tab
"opentest": "edit ${FILE%.go}_test.go",
```

### Yank the location string `path:line:col` to the clipboard

```yaml tab
command:
  aliases:
    yankloc: '! printf "%s:%s:%s" "$FILE_REL" "$LINE" "$COLUMN" | pbcopy'
```

```python tab
"yankloc": '! printf "%s:%s:%s" "$FILE_REL" "$LINE" "$COLUMN" | pbcopy',
```

### Create a sibling worktree that never collides with another repo of the same name

```yaml tab
command:
  aliases:
    worktreenew:
      - '!! ROOT=$(git rev-parse --git-common-dir) && ROOT=$(cd "$ROOT/.." && pwd) || exit 1'
      - '!! ROOT_HASH=$(printf %s "$ROOT" | (sha256sum 2>/dev/null || shasum -a 256) | cut -c1-4)'
      - '!! ROOT_NAME=${ROOT##*/}'
      - '!! WORKTREE=$RUNE_DATADIR/worktrees/$ROOT_NAME-$ROOT_HASH/$1'
      - '!! git worktree add "$WORKTREE" -b $1'
      - "workspaceopen $WORKTREE"
```

```python tab
"aliases": {
    "worktreenew": [
        '!! ROOT=$(git rev-parse --git-common-dir) && ROOT=$(cd "$ROOT/.." && pwd) || exit 1',
        '!! ROOT_HASH=$(printf %s "$ROOT" | (sha256sum 2>/dev/null || shasum -a 256) | cut -c1-4)',
        '!! ROOT_NAME=${ROOT##*/}',
        '!! WORKTREE=$RUNE_DATADIR/worktrees/$ROOT_NAME-$ROOT_HASH/$1',
        '!! git worktree add "$WORKTREE" -b $1',
        "workspaceopen $WORKTREE",
    ],
},
```

### Run an inline `sed` against the focused file

```yaml tab
command:
  aliases:
    sed: "!! gsed -i $1 $FILE"
```

```python tab
"sed": "!! gsed -i $1 $FILE",
```

`:sed s/foo/bar/g` rewrites the focused file in place.
