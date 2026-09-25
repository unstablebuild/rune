# navigation.star — the code-navigation tutorial.
#
# Teaches how to move around code in Rune: finding a file by name,
# finding where text lives, and the language-server verbs that jump to
# a definition, list references and implementations, show hover docs,
# and step back and forth through the cursor history (jumplist).
#
# The DSL is interpreted by ide/idetutorial/starlarktutorial. The entry
# function runs on its own Starlark goroutine; each blocking builtin
# (wait_command, wait_event, wait_shell) is one screen of the tutorial
# tile that stays up until its milestone is met, and returns a real
# value so authors can branch, loop, and compose helpers with regular
# Starlark control flow.

ck = command_key()

# How to move through a location picker / finder list, phrased per mode.
# Keep the arrows as a universal fallback while naming each preset's
# completion bindings.
if editor_mode() == "modal":
    move_phrase = "`<ctrl-j>` / `<ctrl-k>` (or `<up>` / `<down>`)"
elif editor_mode() == "emacs":
    move_phrase = "`<ctrl-p>` / `<ctrl-n>` (or `<up>` / `<down>`)"
else:
    move_phrase = "the arrow keys `<up>` / `<down>`"

if editor_mode() == "emacs":
    def_by_name_key = "<ctrl-alt-.>"
else:
    def_by_name_key = "<alt-shift-d>"
def_by_name_cta = "press `" + def_by_name_key + "`"

# The `jumptoast` prefill fuzzy-jumps to a function or method defined in
# the current file. It is bound as a prompt-prefill macro whose chord
# differs by mode. `key_for` cannot resolve prefill macros, so hardcode
# it; TestNavigationTutorialJumpToSymbolKeyMatchesPreset pins each chord
# against the preset that binds it.
if editor_mode() == "emacs":
    jump_symbol_key = "<meta-j>"
else:
    jump_symbol_key = "<alt-f>"

def keyhint(cmd, *args):
    k = key_for(cmd, *args)
    return (" Default key: `" + k + "`.") if k else ""

def keypress(cmd, *args):
    # The user's real bound key for cmd, phrased as a keypress
    # instruction. Falls back to the command-prompt wording when the
    # command is unbound, so copy adapts to mode and custom rebinds.
    k = key_for(cmd, *args)
    if k:
        return "press `" + k + "`"
    return "open the command prompt (`" + ck + "`) and run `" + cmd + "`"

def keylabel(cmd, *args):
    # The bound key as a bare label, for copy that names a key rather
    # than asks for it. Falls back to the command name when unbound.
    k = key_for(cmd, *args)
    return "`" + (k if k else cmd) + "`"

cleanup_md = """\
Let's start fresh. Clear the layout: """ + keypress("windowcloseall") + """.
"""

# Everything from `jumptoast` onwards reads the open file's syntax tree
# or asks its language server, so a file with no code in it strands the
# rest of the lesson.
source_file_note = """
> These steps read the open file's syntax tree and ask its language
> server, so they need a **source file**: one with functions or types
> written in it. If a `LICENSE` or a `README.md` is in front of you,
> open a code file first.
"""

intro_md = """\
You already know how to open files and move windows. This tutorial is
about moving around **code**: jumping between files, symbols, and
definitions without reaching for the mouse.

We will answer these questions, one at a time:

- How do I find a file by name?
- How do I find where some text lives?
- How do I jump to a function in the file I already have open?
- How do I jump to where a symbol is defined?
- How do I navigate back and forth as I explore?
- What else can I ask about the symbol under my cursor?
"""

searchfile_md = """\
`searchfile` opens a fuzzy finder over every file in the workspace, so
you can reach a file without knowing where it sits in the tree.

- Type part of a name and matches rank as you type.

- The characters need not be contiguous: `navistar` finds
  `navigation.star`.

Open it now: """ + keypress("searchfile") + """.
"""

searchfile_picker_md = """\
The finder is open. Narrow the list down, then take a file from it.

- Type a few characters of the name. They do not have to be contiguous,
  and the closest matches rank first.

- Move through the results with """ + move_phrase + """.

- Press `<enter>` to open the selected file.

The file opens as a tab in the window you came from.
"""

console_md = """\
`searchfile` and `searchtext` come from the **fuzzy-search** extension,
which is not installed yet. Packages are installed from the **Rune
console**, not the command prompt you have been using.

- The prompt (`""" + ck + """`) is one line, and closes as soon as the
  command runs.

- The console is a durable tab with its own REPL, for commands that
  want a persistent output window — like installing a package.

The console **sets up** Rune, the prompt **drives** it.

Open the console: """ + keypress("console") + """.
"""

pkg_install_md = """\
You are in Rune's console now.

- Type `pkg install fuzzy-search` and press `<enter>`.

- The install takes a few seconds.

This step clears itself once the install finishes.
"""

searchfile_missing_msg = ("`searchfile` still isn't available, so the finder " +
                          "steps will be skipped. Install the fuzzy-search " +
                          "extension with `pkg install fuzzy-search` from " +
                          "Rune's console, then rerun this tutorial to try it.")

searchtext_md = """\
`searchtext` searches the *contents* of every file in the workspace,
not just their names.

- Reach for it when you remember a string, an error message or a
  comment, but not which file holds it.

- Results are lines, so picking one jumps straight to that
  line.""" + keyhint("searchtext") + """

Open it now: """ + keypress("searchtext") + """.
"""

searchtext_picker_md = """\
The search is open. Narrow the list down, then take a line from it.

- Type the word you are after, or a few characters of it. The
  characters do not have to be contiguous, and the closest matches
  rank first.

- Move through the results with """ + move_phrase + """.

- Press `<enter>` to jump to the selected line.
"""

searchtext_missing_msg = ("`searchtext` also comes from the fuzzy-search " +
                          "extension, which is not installed here. Install it " +
                          "with `pkg install fuzzy-search` from Rune's console, " +
                          "then rerun this tutorial.")

jump_symbol_md = """\
Workspace-wide search is not always what you want. When the file is
already open and you only need to reach a function inside it,
`jumptoast` jumps over every function and method defined in the current
file. It prefills the command prompt with `jumptoast`, which queries
the file's syntax tree for matching definitions; sibling prefills do
the same for variables and types.

Try it now: press `""" + jump_symbol_key + """`, then pick a function
from the list:

- Type a few characters of its name. There is no need to type the
  whole thing: the list narrows as you type.

- Move through the matches with """ + move_phrase + """.

- Press `<tab>` to take the highlighted match, then `<enter>` to jump
  to it.
""" + source_file_note

lsp_intro_md = """\
Text search finds strings. To understand code, Rune talks to a
**language server**: the same engine that powers autocomplete and
diagnostics.

- The cross-language `lsp` command exposes it.

- It knows what symbols mean, not just where their letters appear.

The next few steps use `lsp` to jump to definitions, list references
and read documentation.
"""

lsp_definition_cursor_md = """\
This is the verb you will reach for most: `lsp definition` with no
argument jumps to where the symbol under your cursor is
defined.""" + keyhint("lsp", "definition") + """

Try it now:

1. Put your cursor on a symbol.

2. Then """ + keypress("lsp", "definition") + """.
"""

lsp_definition_name_md = """\
Your cursor does not have to be on the symbol at all.

- `lsp definition <name>` fuzzy-matches a symbol by name across the
  whole workspace and jumps to it.

- `lsp references` and `lsp hover` take a name the same way.

Search for one now: """ + def_by_name_cta + """, then pick a symbol
from the list:

- Type a few characters of its name: the list narrows as you type.
  Cannot think of one? Peruse the list and take whatever looks
  interesting.

- Move through the matches with """ + move_phrase + """.

- Press `<tab>` to take the highlighted match, then `<enter>` to jump
  to it.
"""

cursorhistory_md = """\
Every jump you make — a definition jump, a search result, a file opened
from the explorer — is pushed onto the **cursor history**, Rune's
jumplist. You can walk it in both directions without retracing your
steps.

- `cursorhistory prev` goes back to where you
  were.""" + keyhint("cursorhistory", "prev") + """

- `cursorhistory next` goes forward
  again.""" + keyhint("cursorhistory", "next") + """

- `cursorhistory jump` opens a picker over the whole history, to leap
  several stops at once: """ + keylabel("cursorhistory", "jump") + """.

- """ + keylabel("lspnextdiagnostic") + """ / """ + keylabel("lspprevdiagnostic") + """ step through the current
  file's diagnostics.

Go back to where you jumped from: """ + keypress("cursorhistory", "prev") + """.
"""

cursorhistory_next_md = """\
Going back left a breadcrumb ahead of you: `cursorhistory next` follows
it forward to where you just were, so an exploration can be retraced in
either direction.""" + keyhint("cursorhistory", "next") + """

Go forward again: """ + keypress("cursorhistory", "next") + """.
"""

lsp_more_md = """\
The other `lsp` verbs work the same way: put your cursor on a symbol
and run one.

- `lsp references` lists every place a symbol is
  used.""" + keyhint("lsp", "references") + """

- `lsp implementation` finds the concrete types that satisfy an
  interface, or the interfaces a type
  satisfies.""" + keyhint("lsp", "implementation") + """

- `lsp hover` shows the symbol's type and documentation
  inline.""" + keyhint("lsp", "hover") + """

Try `lsp references` now:

1. Put your cursor on a symbol and """ + keypress("lsp", "references") + """.

2. The **location picker** opens with every use of that symbol. Move
   through it with """ + move_phrase + """.

3. Press `<enter>` to jump to the one you want.
"""

def teach_cleanup():
    wait_command(
        title   = "Navigate code 🧭",
        command = "windowcloseall",
        text    = intro_md + "\n" + cleanup_md,
    )


def teach_fuzzy_search_install():
    # The finder steps below drive a package-provided command. Install
    # it up front so the first `searchfile` is a real search instead of
    # Rune's install prompt.
    if command_exists("searchfile"):
        return False
    wait_command(
        title   = "Install the finder",
        command = "console",
        text    = console_md,
    )
    wait_shell(
        title = "Install the finder",
        args  = ["pkg", "install", "fuzzy-search"],
        text  = pkg_install_md,
    )
    return True


def teach_searchfile(just_installed):
    if not just_installed and not command_exists("searchfile"):
        notify(level = warn, message = searchfile_missing_msg)
        return
    wait_command(
        title   = "Find a file by name",
        command = "searchfile",
        text    = searchfile_md,
    )
    wait_event(
        event = "open",
        title = "Find a file by name",
        text  = searchfile_picker_md,
    )


def teach_searchtext(just_installed):
    if not just_installed and not command_exists("searchtext"):
        notify(level = warn, message = searchtext_missing_msg)
        return
    wait_command(
        title   = "Search for text",
        command = "searchtext",
        text    = searchtext_md,
    )
    wait_event(
        event = "open",
        title = "Search for text",
        text  = searchtext_picker_md,
    )


def teach_jump_symbol():
    wait_command(
        title   = "Jump to a function in this file",
        command = "jumptoast",
        text    = jump_symbol_md,
    )


def teach_definition_at_cursor():
    wait_command(
        title   = "Go to definition",
        command = "lsp definition",
        text    = lsp_intro_md + "\n" + lsp_definition_cursor_md,
    )


def teach_definition_by_name():
    wait_command(
        title   = "Find a definition by name",
        command = "lsp definition <name>",
        text    = lsp_definition_name_md,
    )


def teach_cursorhistory():
    wait_command(
        title   = "Navigate back and forth",
        command = "cursorhistory prev",
        text    = cursorhistory_md,
    )

    wait_command(
        title   = "Jump forward",
        command = "cursorhistory next",
        text    = cursorhistory_next_md,
    )


def teach_lsp_more():
    wait_command(
        title   = "Ask about a symbol",
        command = "lsp references",
        text    = lsp_more_md,
    )


def run():
    if not workspace_open():
        fail("Open a workspace first before running this tutorial. Open the " +
             "command prompt with `" + ck + "` and run `workspaceopen`.")
        return

    if not is_lsp_server_running():
        fail("For this tutorial to be useful, you should run it in a workspace " +
             "that contains a project module of one of our supported languages. " +
             'Run the "help" command to learn about which languages we support. ' +
             "If the workspace is a monorepo with multiple nested projects, " +
             "opening a file in a sub-project will start the language server " +
             "automatically.")
        return

    teach_cleanup()

    fuzzy_search_installed = teach_fuzzy_search_install()
    teach_searchfile(fuzzy_search_installed)
    teach_searchtext(fuzzy_search_installed)
    teach_jump_symbol()
    teach_definition_at_cursor()
    teach_definition_by_name()
    teach_cursorhistory()
    teach_lsp_more()


tutorial(id = "navigation", title = "Navigate code", version = "26", entry = run)
