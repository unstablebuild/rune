# Fuzzy-search extension configuration.
#
# Rune's package manager runs this script after installing the
# extension_fuzzy_search bundle. Predeclared globals exposed by the host:
#
#   RUNE_DATADIR      string  Rune's data directory.
#   RUNE_PKG_ID       string  the installed package's ID.
#   RUNE_PKG_VERSION  string  the installed package's version.
#   RUNE_EDITOR_MODE  string  "vim" | "helix" | "standard" | "emacs" — the
#                             resolved host editor mode (empty when not yet
#                             known). The host resolves the deprecated
#                             "modal" and "modeless" aliases to "vim" and
#                             "standard", and substitutes exo with its
#                             configured editor.exo.fallback value, so
#                             neither an alias nor "exo" ever reaches here.
#   RUNE_OS           string  the host's GOOS, e.g. "darwin" or "linux".
#
# The script writes the merged settings into the user's rune config so that
# fuzzy_search options and search* aliases/key bindings are only registered
# when the extension is actually installed.

mode = RUNE_EDITOR_MODE

# Super belongs to the desktop on Linux, where the Rune presets keep their
# app-wide commands on Ctrl+Alt instead.
if RUNE_OS == "linux":
    file_key = "<c-a-o>"
    text_key = "<c-a-\\\\>"
else:
    file_key = "<m-p>"
    text_key = "<m-\\\\>"

def attr(fg = None, bg = None, flags = None):
    out = {}
    if fg != None:
        out["fg"] = fg
    if bg != None:
        out["bg"] = bg
    if flags != None:
        out["flags"] = flags
    return out

config = {
    "extensions": {
        "fuzzy_search": {
            "path": "$RUNE_DATADIR/bin/extension_fuzzy_search",
            "config": {
                "file": {
                    "case_sensitive":     True,
                    "algo":               "fuzzy",
                    "history_key":        file_key,
                    "history":            50000,
                    "element_attr":       attr(fg = "default", bg = "default", flags = ["dim"]),
                    "matched_text_attr":  attr(fg = "blue", bg = "default", flags = ["bold"]),
                    "count_attr":         attr(fg = "default", bg = "default"),
                    "focus_element_attr": attr(fg = "purple", bg = "default"),
                },
                "line": {
                    "case_sensitive":     True,
                    "algo":               "fuzzy",
                    "history_key":        text_key,
                    "history":            2000,
                    "element_attr":       attr(fg = "default", bg = "default", flags = ["dim"]),
                    "matched_text_attr":  attr(fg = "blue", bg = "default", flags = ["bold"]),
                    "count_attr":         attr(fg = "default", bg = "default"),
                    "focus_element_attr": attr(fg = "purple", bg = "default"),
                },
                "syntax": {
                    "case_sensitive": True,
                    "algo":           "fuzzy",
                },
            },
        },
    },
    "tutorials": {
        "fuzzy_search": "$RUNE_DATADIR/pkg/$RUNE_PKG_ID/$RUNE_PKG_VERSION/fuzzy_search.star",
    },
    "command": {
        "aliases": {
            "searchfunc":      "echo {prompt}searchast<space>locals.scm<space>local.definition.method|local.definition.function<enter>",
            "searchvar":       "echo {prompt}searchast<space>locals.scm<space>local.definition.var<enter>",
            "searchtype":      "echo {prompt}searchast<space>locals.scm<space>local.definition.type<enter>",
        },
        "key_bindings": {},
    },
}

if mode == "emacs":
    config["command"]["key_bindings"] = {
        "<c-x><c-f>": "searchfile",
        "<a-s>o":     "searchtext",
    }
else:
    config["command"]["key_bindings"] = {
        file_key:  "searchfile",
        text_key:  "searchtext",
        "<a-s-f>": "searchfunc",
        "<a-s-v>": "searchvar",
        "<a-s-s>": "searchtype",
    }

# Sublime-style project search; the Linux standard preset already binds
# <ctrl-shift-f> to searchtext.
if mode == "standard" and RUNE_OS != "linux":
    config["command"]["key_bindings"]["<s-m-f>"] = "searchtext"
