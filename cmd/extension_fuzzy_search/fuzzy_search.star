# fuzzy_search.star — the fuzzy-search extension's onboarding tutorial.
#
# Rune registers this tutorial when the fuzzy-search package is
# installed (see the `tutorials` entry in config.star) and offers to run
# it immediately. It introduces the three search commands the extension
# adds and then drives the user through one live search: type a query,
# move the selection down and up the results list, and open the focused
# result with <enter>.
#
# The DSL is interpreted by ide/idetutorial/starlarktutorial. The entry
# function runs on its own Starlark goroutine; each blocking builtin
# (wait_command, wait_event) is one screen of the tutorial tile that
# stays up until the user performs the described action, so the steps
# below read top to bottom. Every key reaches the focused finder while a
# step is up: the live search steps use wait_event, which resolves only
# when the host observes the named editor event.

ck = command_key()

def keyhint(cmd, *args):
    k = key_for(cmd, *args)
    return (" Default key: `" + k + "`.") if k else ""

def keypress(cmd, *args):
    # The user's real bound key for cmd, phrased as a keypress
    # instruction. Falls back to the command-prompt wording when the
    # command is unbound so the copy adapts to custom rebinds.
    k = key_for(cmd, *args)
    if k:
        return "press `" + k + "`"
    return "open the command prompt (`" + ck + "`) and run `" + cmd + "`"

def keypress_sentence(cmd, *args):
    # keypress() phrased as a sentence opener: capitalize only the first
    # letter so a bound key like `<m-p>` is not lowercased by capitalize().
    s = keypress(cmd, *args)
    return s[0].upper() + s[1:]

intro_md = """\
**Fuzzy Search** adds fast, fuzzy finders for everything in your
workspace. Results are scored and sorted best-match first, and each
finder opens in its own window so your editor layout stays put.

## The commands

- `searchfile`: fuzzy-find **files** by name.""" + keyhint("searchfile") + """
- `searchtext`: fuzzy-find **file contents**, line by line.""" + keyhint("searchtext") + """
- `searchast`: fuzzy-find **syntax nodes** with a tree-sitter query. It
  takes query arguments, so it has no key of its own.

## Inside a finder

Type to filter. The list re-ranks as you type. Move the selection with
the arrow keys or `<ctrl-j>` / `<ctrl-k>`, press `<enter>` to open the
focused result in a new tab, and `<esc>` to cancel.
"""

open_file_md = """\
Let's run a file search.

""" + keypress_sentence("searchfile") + """ to open the file finder.
"""

find_file_md = """\
The file finder is open. Every file in the workspace is listed, ranked
by match score.

1. **Type a few characters** of a filename you expect to exist. The
   list filters and re-ranks as you type.
2. **Move the selection** with the arrow keys, or `<ctrl-j>` /
   `<ctrl-k>`, to walk down and up the results.
3. **Press `<enter>`** to open the focused result in a new tab.

The tutorial continues once you open a file.
"""

text_md = """\
`searchtext` works the same way but searches **file contents** instead
of names, so you can jump straight to the line you're after.""" + keyhint("searchtext") + """

Open the text finder now: """ + keypress("searchtext") + """. Inside any
finder, `<esc>` cancels without opening a result.
"""

wrap_up_msg = ("That's Fuzzy Search: `searchfile` for file names, `searchtext` " +
               "for file contents, `searchast` for syntax nodes. Replay this tour " +
               "any time with `tutorial start fuzzy_search`. Happy searching!")

def teach_file_search():
    wait_command(
        title   = "Search files",
        command = "searchfile",
        text    = intro_md + "\n" + open_file_md,
    )
    notify(level = success, message = "File finder open.")

    # wait_event passes every key through to the focused finder, so the
    # user types, navigates, and opens a result freely; the step resolves
    # on the file-open event rather than from keystrokes.
    wait_event(
        event = "open",
        title = "Find and open a file",
        text  = find_file_md,
    )
    notify(level = success, message = "You opened a file from the finder.")


def teach_text_search():
    wait_command(
        title   = "Search file contents",
        command = "searchtext",
        text    = text_md,
    )
    notify(level = success, message = "Text finder open.")


def run():
    teach_file_search()
    teach_text_search()
    notify(level = success, message = wrap_up_msg)


tutorial(id = "fuzzy_search", title = "Fuzzy Search", version = "4",
         entry = run)
