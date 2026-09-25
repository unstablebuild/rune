# Rune default configuration, in Starlark form.
#
# The script's final top-level `config` dict is handed to the Rune config
# loader exactly as if it had been written in YAML.
#
# Use normal Starlark `if` / `for` / dict-merge idioms to customise.

# The Rune loader passes `tui` (bool) as a predeclared global; the script
# branches on it below. The `mode` global is also exposed for backwards
# compatibility but is no longer consulted here; editor mode is selected by
# the editor preset written to the user config by the bootstrap flow.

# Box-drawing frame characters used by the terminal-specific overrides below.
TUI_FRAME_CHARSET = {
    "horizontaltop":    "─",
    "horizontalbottom": "─",
    "verticalright":    "│",
    "verticalleft":     "│",
    "topleft":          "┌",
    "topright":         "┐",
    "bottomleft":       "└",
    "bottomright":      "┘",
}

# GUI frame characters used by the default window-manager configuration.
GUI_FRAME_CHARSET = {
    "horizontaltop":    "▔",
    "horizontalbottom": "▁",
    "verticalright":    "▕",
    "verticalleft":     "▏",
    "topleft":          "🭽",
    "topright":         "🭾",
    "bottomleft":       "🭼",
    "bottomright":      "🭿",
}

def merge(base, overrides):
    """Recursive dict merge; `overrides` values win at every level."""
    out = dict(base)
    for k, v in overrides.items():
        if k in out and type(out[k]) == "dict" and type(v) == "dict":
            out[k] = merge(out[k], v)
        else:
            out[k] = v
    return out

def attr(fg = None, bg = None, flags = None, flag = None):
    """Build a text-attribute dict.

    All arguments are optional; only those explicitly passed show up in the
    result. `flags` may be a single string ("bold") or a list of strings
    (["bold", "italic"]), and `flag` is accepted as a legacy alias for the
    singular form. The rune config loader accepts both.
    """
    out = {}
    if fg != None:
        out["fg"] = fg
    if bg != None:
        out["bg"] = bg
    if flag != None:
        out["flag"] = flag
    if flags != None:
        out["flags"] = flags
    return out

config = {
    # Logging file path. This property is not reloaded on reloadWorkspace.
    "log_path":  "~/.rune/debug.log",
    # Log level. This property is not reloaded on reloadWorkspace.
    "log_level": "info",
    # Whether to use the system clipboard or Rune's in-memory clipboard.
    "clipboard": "system",
    # LSP configuration.
    "lsp": {
        # Icons used by the auxiliary bar when LSP diagnostics are displayed.
        "icons": {
            "error":       "✖",
            "warning":     "▲",
            "information": "◉",
            "hint":        "󰌵",
        },
    },
    # LLM models configuration. The router reads these keys directly to
    # construct provider clients. Each provider sub-key carries the bits
    # specific to that backend (base_url, ...). Legacy top-level
    # `openai`/`anthropic` blocks live in rune-agent's own settings tree
    # and are NOT consulted here.
    "models": {
        # Global sampling and budgeting parameters.
        "temperature":           0,
        "top_p":                 0,
        "max_tokens":            0,
        "max_completion_tokens": 0,
        "frequency_penalty":     0,
        "presence_penalty":      0,
        # Reasoning summary preference for providers that support it.
        # Valid: "auto", "concise", "detailed", "disabled".
        "reasoning_summary":     "auto",
        # When True, the router logs HTTP request/response details.
        "debug_http":            False,
        "openai": {
            "base_url":            "",
            # "" lets the model decide; otherwise one of
            # none, minimal, low, medium, high, xhigh, max.
            "reasoning_effort":    "",
        },
        "anthropic": {
            "base_url":         "",
            "reasoning_effort": "",
            # "ephemeral", "5m", "1h", or "" to disable prompt caching.
            "cache_control":    "",
        },
        "gemini": {
        },
        "bedrock": {
            # Named profile from the shared AWS config files. Empty uses
            # the default credential chain. API keys and their regions are
            # stored via `models providers bedrock add <name> <region>`.
            "profile":          "",
            # Override the Bedrock runtime endpoint (VPC endpoint or
            # gateway). Empty uses the standard regional endpoint.
            "base_url":         "",
            "reasoning_effort": "",
            # "default" enables prompt caching, "" disables it.
            "cache_control":    "",
        },
        "codex": {
            # OpenAI models via a ChatGPT Codex subscription (browser
            # sign-in, no api_key). Sign in with `models providers codex
            # login`. The "openai" block above bills the same models by
            # API key instead.
            "base_url": "",
        },
        "claude": {
            # Anthropic (Claude) models via a Claude Pro/Max subscription
            # (browser sign-in, no api_key). Sign in with `models
            # providers claude login`. The "anthropic" block above bills
            # the same models by API key instead.
            "base_url": "",
        },
        "custom": {
            "url":     "",
            "api_key": "",
            # Map of model name -> context window in tokens.
            "available_models": {},
        },
        "local": {
            # Local GGUF model cache root. Empty means $RUNE_DATADIR/models.
            "models_cache_dir":   "",
            # Logical batch size (--batch-size). 0 picks the server default.
            "batch_size":         0,
            # Number of layers to offload to the GPU. Negative offloads
            # every layer the backend supports.
            "n_gpu_layers":       -1,
            # Generation thread count (--threads). 0 lets the server pick.
            "threads":            0,
            # Enable flash-attention when supported by the backend.
            "flash_attention":    False,
            # Caps tokens generated per completion. 0 means "until EOG".
            "max_output_tokens": 0,
            # Overrides the chat template embedded in the GGUF.
            "chat_template":      "",
            # Minimum chunk size (tokens) for KV-cache shift-reuse. 0
            # disables shift-reuse (falls back to LCP-only matching).
            "n_cache_reuse":      0,
            # Loopback address llama-server binds to. Empty means 127.0.0.1.
            "host":               "",
            # Explicit path to the llama-server binary. Empty resolves it
            # via `pkg install llama-server`, then $PATH.
            "server_bin_path":    "",
            # Maximum number of local model servers running at once. When a
            # new model would exceed the cap the least-recently-used idle
            # server is stopped. 0 uses the built-in default (1).
            "max_servers":        0,
            # How long a server may sit idle before it is stopped, e.g.
            # "5m". Empty uses the built-in default.
            "idle_timeout":       "",
            # How long to wait for a server to become ready, e.g. "120s".
            # Empty uses the built-in default.
            "startup_timeout":    "",
            # Extra arguments appended verbatim to the llama-server command.
            "extra_args":         [],
            # Sampler chain. Leaving any value at zero inherits the
            # llama-server upstream default. Keys mirror SamplerParams
            # exactly.
            "sampling": {
                "seed":            0xFFFFFFFF,
                "temperature":     0.8,
                "top_k":           40,
                "top_p":           0.95,
                "min_p":           0.05,
                "repeat_penalty":  1.0,
                "repeat_last_n":   64,
                "freq_penalty":    0,
                "presence_penalty":0,
                "typical_p":       0,
                "top_n_sigma":     0,
                "mirostat":        0,
                "mirostat_tau":    0,
                "mirostat_eta":    0,
                "dynatemp_range":  0,
                "dynatemp_exponent": 0,
                "xtc_probability": 0,
                "xtc_threshold":   0,
                "dry_multiplier":  0,
                "dry_base":        0,
                "dry_allowed_length": 0,
                "dry_penalty_last_n": 0,
            },
        },
    },
    "console": {
        "max_history": 2000,
        # Open the companion console's input line in insert mode when the
        # editor is modal (vi). Set to False to start in normal mode.
        # Ignored in modeless mode, which has no normal mode.
        "modal_start_insert": True,
        # Prefix shown on the companion console's input line. Defaults
        # to "> " when empty.
        "prompt": "rune> ",
    },
    # Toggle workspace transition animations.
    #
    # Both default to True. Setting either to False suppresses the
    # corresponding animation; everything else (init/shutdown
    # shaders, the :shaderrun command, ...) is unaffected.
    #
    # The open animation is only ever played after a loading
    # animation, so disabling "loading_workspace" effectively
    # disables both.
    #
    # "open_workspace" also accepts a dict to override the shader
    # name and lifetime used for the open transition:
    #
    #
    # "shader" accepts any of the names listed by :shaderrun and
    # "duration" accepts any Go duration string. Both keys are
    # optional and fall back to the built-in defaults.
    "animations": {
        # Plays while a workspace is being installed
        # (addWorkspace). The default gray-fade desaturates the
        # screen to signal the IDE is busy.
        "loading_workspace": True,
        # Plays when a workspace finishes installing. The default
        # is a quick burn sweep that reveals the new content.
        "open_workspace": {
            "enabled":  True,
            #"shader":   "burn",
            #"duration": "1s",
        },
        # Plays a shineFrame sweep over the command prompt frame
        # while it's open. Set to False to suppress the effect.
        "command_prompt": {
            "enabled":  True,
            "color":    "red",
            "angular_width": 0.33,
            "cycles": 1,
        },
        # Plays over content tabs that an extension marks as having
        # work in progress (e.g. a rune-agent turn), until the work
        # ends or the tab closes. "shader" is one of blaze, burn,
        # inferno, noise, pulse, shine or trippy. "fps" is the redraw
        # cadence and "loop" how long one visual loop lasts, as a Go
        # duration string.
        "active_content_tab": {
            "enabled": True,
            "shader":  "shine",
            "fps":     10,
            "loop":    "1400ms",
        },
        # Plays over the workspace tabs that own an active content
        # tab, whether or not the workspace is focused. Takes the same
        # keys as active_content_tab.
        "active_workspace_tab": {
            "enabled": True,
            "shader":  "shine",
            "fps":     10,
            "loop":    "1400ms",
        },
    },
    # Self-upgrade configuration. Rune polls a public manifest endpoint
    # to discover new releases and prompts before installing them.
    "upgrade": {
        # When True, Rune periodically checks for a newer release and
        # prompts to install it. Set to False to opt out — the
        # `:upgrade` command continues to work either way.
        "auto_check_enabled": True,
        # How often the background check runs. Accepts any Go duration
        # string (e.g. "12h", "168h").
        "check_period":       "24h",
        # Release channel. Reserved for future use; only "stable" is
        # currently honoured.
        "channel":            "stable",
    },
    "gui": {
        # Font size in points. When unset, Rune picks a DPI-aware default
        # (larger on low-DPI displays). The guifontsize command and the
        # <m-=> / <m--> bindings adjust it at runtime.
        # "font_size":          13,
        # Add or remove pixels from the font's default column width.
        "column_width_offset": -1,
        # Add or remove pixels from the font's default line height.
        "line_height_offset":   0,
        # Number of lines scrolled per unit of mouse-wheel movement. Higher
        # values scroll faster; fractional deltas from high-resolution devices
        # such as trackpads are accumulated so smooth scrolling is scaled too.
        "scroll_multiplier":    3,
        # Pick a theme by name; see the `guitheme` command for the full list.
        "default_theme":        "romero",
        # Enables or disables transparent-window rendering, effectively
        # enabling or disabling the guiopacity command.
        "enable_transparent_window": True,
        # Initial window opacity when transparent rendering is enabled.
        "window_opacity":       {"fg": 1, "bg": 1},
        # Initial background blur when transparent rendering is enabled and
        # the background opacity is not 1.
        "window_blur_radius":   100,
        # Enable or disable font ligatures for sequences like => and ->.
        "ligatures":            False,
        # Themes available to `default_theme` and the `guitheme` command.
        "themes":               GUI_THEMES,
        # Remap physical keys before they reach the editor (GUI only). Both
        # sides use the same syntax as command key bindings (see the
        # `key-mapping` docs). This is useful for keys the OS swallows or that
        # the input layer otherwise drops, such as CapsLock. Example: treat
        # CapsLock as Escape. Keys like <capslock>, <numlock>, <scrolllock>
        # and <menu> never act on their own — they only do something when
        # remapped here.
        "key_mapping": {
        #     "<capslock>": "<esc>",
        },
        # Buttons of the quick menu, the vertical bar of native buttons
        # floating over the right edge of the window (macOS only; ignored
        # elsewhere). Order is top to bottom, and an empty list removes the
        # bar along with the column it reserves.
        #   symbol  — SF Symbol name drawn as the button image.
        #   title   — hover tooltip.
        #   command — any Rune command line, as a space-separated string or
        #             a list of words. It also identifies the button, so it
        #             must be unique across the menu.
        "quick_menu": [
            {"symbol": "command", "title": "Command Prompt", "command": "echo {prompt}"},
            {"symbol": "plus", "title": "Open Project", "command": "workspaceopen"},
            {"symbol": "sidebar.left", "title": "File Explorer", "command": "fexplorer"},
            {"symbol": "apple.terminal", "title": "New Terminal", "command": "terminalnewtab"},
            {"symbol": "rectangle.lefthalf.filled", "title": "Split Left",
             "command": "windownew left"},
            {"symbol": "rectangle.righthalf.filled", "title": "Split Right",
             "command": "windownew right"},
            {"symbol": "rectangle.tophalf.filled", "title": "Split Up",
             "command": "windownew up"},
            {"symbol": "rectangle.bottomhalf.filled", "title": "Split Down",
             "command": "windownew down"},
            {"symbol": "arrow.up.left.and.arrow.down.right", "title": "Toggle Maximize",
             "command": "windowtogglemaximize"},
            {"symbol": "arrow.triangle.branch", "title": "New Worktree",
             "command": "echo {prompt}worktreenew<space>"},
            {"symbol": "sparkles", "title": "Agent", "command": "agent"},
            {"symbol": "document.viewfinder", "title": "Find File", "command": "searchfile"},
            {"symbol": "text.viewfinder", "title": "Find in Files", "command": "searchtext"},
            {"symbol": "ellipsis.curlybraces", "title": "Find Definition",
                "command": "echo {prompt}lsp<space>definition<space>"},
            {"symbol": "bolt", "title": "Console", "command": "console help"},
            {"symbol": "gearshape", "title": "Settings", "command": "config"},
            {"symbol": "questionmark.circle", "title": "Rune Help", "command": "help"},
        ],
    },
    "editor": {
        # Editor mode and exo settings are not configured here. The
        # bootstrap flow writes an editor preset with the user's choice
        # ("modal", "standard", "emacs", or "exo" with a preset). Without an
        # editor preset, Rune defaults to modal as configured below.
        # Enable or disable syntax-driven indentation.
        "autoindent": True,
        # Auto-pair quotes, brackets, and braces while editing. Explicitly off
        # for modal mode by default; the standard override enables it.
        "auto_pair":  False,
        # Auto-save dirty buffers after a brief idle period. Off by default;
        # set to True to flush file tabs ~2s after the last edit.
        "auto_save":  False,
        "tabspaces": 4,
        # Where swap files are kept.
        #
        #   True   (default) keep them together under swap/ in the data
        #          directory (--datadir). Each entry is named after the
        #          edited file's full path, with "+" for the separators, so
        #          files with the same name never collide. Nothing is
        #          written next to the file, so no .gitignore entry is
        #          needed and files in read-only directories stay editable.
        #   False  keep the swap next to the edited file, as ".<name>.rswp".
        #          Visible to git; needs write permission on the file's
        #          directory.
        #
        # The data directory is resolved per file, on the host that owns it,
        # so a remote workspace keeps its swaps on the remote machine. Files
        # on a host with no data directory of its own keep the swap next to
        # them, as with False.
        "swap_dir": True,
        # Maximum buffer size (in bytes) for which Rune installs a syntax
        # tree on tab open. Files larger than this skip syntax parsing to
        # avoid freezing the editor inside the tree-sitter parser on
        # large log files and other big blobs. Set to 0 to disable the
        # guard and always parse.
        "max_size_for_syntax": 1048576,
        "modal": {
            # Default text attributes.
            "attr":        attr(fg = "default", bg = "default"),
            "message_bar": {
                # Base attributes of the message bar and the search prompt.
                "attr":   attr(fg = "default", bg = "gray"),
                # Layout of the superimposed message bar shown while
                # searching. The Message component supports the same styling
                # operators as status-bar components.
                "layout": '░▒▓█ {{ .Message | fg "white" }} ',
            },
            # Search result attributes.
            "search_attr": attr(fg = "grey", bg = "yellow"),
        },
        "standard": {
            # Default text attributes.
            "attr":        attr(fg = "default", bg = "default"),
            # Search result attributes.
            "search_attr": attr(fg = "grey", bg = "yellow"),
            "search": {
                "find_key":          "<m-f>",
                "replace_key":       "<m-r>",
                "attr":              attr(fg = "default", bg = "default"),
                "input_attr":        attr(fg = "default", bg = "default"),
                "placeholder_attr":  attr(fg = "gray", bg = "default"),
                "frame_attr":        attr(fg = "gray", bg = "default"),
                "focus_frame_attr":  attr(fg = "silver", bg = "default"),
                "button_attr":       attr(fg = "default", bg = "gray"),
                "button_hover_attr": attr(fg = "default", bg = "blue"),
                "match_attr":        attr(fg = "grey", bg = "yellow"),
                "current_match_attr": attr(fg = "default", bg = "default"),
            },
        },
        "emacs": {
            # Default text attributes.
            "attr":        attr(fg = "default", bg = "default"),
            "message_bar": {
                # Base attributes of the echo area and of the transient mode
                # label (ISEARCH, QUERY, ...) shown in the status bar.
                "attr":   attr(fg = "default", bg = "gray"),
                # Layout of the overlaid Emacs echo area. The Message
                # component supports the same styling operators as status-bar
                # components.
                "layout": '░▒▓█ {{ .Message | fg "white" }} ',
            },
            # Search result attributes.
            "search_attr": attr(fg = "grey", bg = "yellow"),
        },
        # External editor configuration, only consulted when mode == "exo".
        #
        # `command` is the argv template Rune executes inside a vte to open a
        # file. Available substitutions:
        #   {file}  absolute path to the file to open (always required)
        #   {line}  1-based line number for the initial cursor
        #   {col}   1-based column number for the initial cursor
        #
        # `goto` is a Rune key sequence (handler.ParseSequence syntax) that,
        # when injected into the vte, makes the editor place its cursor at
        # {line}/{col}. The same {line}/{col} substitutions are recognised
        # inside the sequence string. Leave empty to disable :goto / mouse
        # click-to-line for this editor.
        #
        # Examples (uncomment one or write your own):
        #
        # Vim / Neovim
        #   "command": 'vim "+call cursor({line}, {col})" {file}',
        #   "goto":    "<esc>:{line}<enter>{col}|",
        #
        # Neovim
        #   "command": 'nvim "+call cursor({line}, {col})" {file}',
        #   "goto":    "<esc>:{line}<enter>{col}|",
        #
        # Helix
        #   "command": "hx {file}:{line}:{col}",
        #   "goto":    "<esc>:goto<space>{line}<enter>",
        #
        # Kakoune
        #   "command": "kak {file} +{line}:{col}",
        #   "goto":    "<esc>:edit<space>-existing<space>{file}<space>{line}<space>{col}<enter>",
        #
        # Micro
        #   "command": "micro {file}:{line}:{col}",
        #   "goto":    "<c-l>{line}:{col}<enter>",
        #
        # Emacs (no window system)
        #   "command": "emacs -nw +{line}:{col} {file}",
        #   "goto":    "<a-x>goto-line<enter>{line}<enter>",
        #
        # Nano
        #   "command": "nano +{line},{col} {file}",
        #   "goto":    "<c-_>{line},{col}<enter>",
        "exo": {
            "command": 'vim "+call cursor({line}, {col})" {file}',
            "goto":    "<esc>:{line}<enter>{col}|",
            # Rune-native editor used to serve URIs the external editor
            # cannot meaningfully edit (memory:// pseudo-URIs such as
            # the file explorer's tab). Valid values are "modal", "standard",
            # or "emacs". "modeless" remains a deprecated alias for "standard".
            "fallback": "modal",
            # Experimental. When True, Rune overlays its own location-list
            # attributes (syntax highlights, LSP diagnostics, debugger variables)
            # on top of the external editor's output. Set to False to
            # keep the external editor's native highlights untouched.
            "experimental_highlights": False,
        },
        "indents": {
            "chatito": "spaces",
            "nim": "spaces",
            "yaml": "spaces",
        },
        # Used to determine the max columns to allow in text wrapping operations
        "ruler": 90,
        # Syntax highlight overrides. Any omitted key inherits the default
        # highlight attributes provided by the active language/parser.
        # Capture names fall back to their dotted prefix, so "keyword.function"
        # inherits "keyword" unless it is given its own entry here.
        "highlights": {
            "function":             attr(fg = "default"),
            "function.builtin":     attr(fg = "yellow"),
            "function.method":      attr(fg = "default"),
            "type":                 attr(fg = "default"),
            "property":             attr(fg = "default"),
            "variable":             attr(fg = "default"),
            "operator":             attr(fg = "default"),
            "keyword":              attr(fg = "yellow"),
            "string":               attr(fg = "magenta"),
            "escape":               attr(fg = "default"),
            "number":               attr(fg = "red"),
            "constant.builtin":     attr(fg = "default"),
            "comment":              attr(fg = "blue"),
            "markup.heading":       attr(fg = "yellow", flags = "bold"),
            "markup.raw":           attr(fg = "magenta"),
            "markup.raw.delimiter": attr(fg = "yellow"),
            "markup.link":          attr(fg = "cyan", flags = "underline"),
            "markup.link.url":      attr(fg = "cyan", flags = "underline"),
            "markup.link.label":    attr(fg = "yellow", flags = "underline"),
            "markup.link.text":     attr(fg = "cyan", flags = "underline"),
            "markup.list":          attr(fg = "yellow", flags = "bold"),
            "markup.quote":         attr(fg = "blue", flags = "italic"),
            "markup.strong":        attr(fg = "yellow", flags = "bold"),
            "markup.italic":        attr(fg = "yellow", flags = "italic"),
            "markup.strikethrough": attr(fg = "blue", flags = "strikethrough"),
            "markup.underline":     attr(fg = "yellow", flags = "underline"),
            "markup.math":          attr(fg = "magenta"),
            "text.title":           attr(fg = "yellow", flags = "bold"),
            "text.literal":         attr(fg = "magenta"),
            "text.uri":             attr(fg = "cyan", flags = "underline"),
            "text.reference":       attr(fg = "yellow", flags = "underline"),
            "text.emphasis":        attr(fg = "yellow", flags = "italic"),
            "text.strong":          attr(fg = "yellow", flags = "bold"),
        },
        # status_bar configures the editor's bottom status bar.
        "status_bar": {
            # Whether the editor renders the status bar at all.
            "enabled": True,
            # Layout of the status bar. Available components include:
            # Status, Filepath, GitShortRef, GitDiffAdd, GitDiffDel,
            # ShiftRight, CursorColumn, CursorLine, TotalLines, and Language.
            # Pipe operators support bg, fg, bold, italic, underline,
            # reverse, and dim.
            "layout": ' {{ .Status | bg "red" | fg "white" | bold }} █▓▒░  {{ .Filepath | fg "white" }}  {{ .GitShortRef | fg "white" }}   {{ .GitDiffAdd | fg "green" }}   {{ .GitDiffDel | fg "red" }} {{ .ShiftRight }} {{ .CursorColumn | fg "white" }}:{{ .CursorLine | fg "white" }}  {{ .TotalLines | fg "white" }} lines  ░▒▓█ {{ .Language | bold | fg "white" | bg "red"}} ',
            # Default background attributes when the layout does not override
            # them explicitly.
            "background_attr":         attr(bg = "gray"),
            # Error color used for status-bar messages.
            "foreground_error_attr":   attr(fg = "yellow"),
        },
        # aux_bar configures the vertical bar shown to the left of file tabs.
        "aux_bar": {
            # Whether the editor renders the auxiliary bar at all.
            "enabled":           True,
            # Enable or disable rendering fold marks.
            "folds":             True,
            # Git mode: bar | inline | all | disabled.
            "git":               "inline",
            "icons":             True,
            # Line-number mode: disabled | absolute | relative.
            "lines":             "absolute",
            # Whether to highlight the cursor position in the bar.
            "highlight_cursor":  True,
            "git_del_inline_attr":    attr(fg = "maroon", flags = "bold"),
            "git_add_inline_attr":    attr(fg = "green", flags = "bold"),
            "git_del_locations_attr": attr(bg = "maroon", flags = "dim"),
            "git_add_locations_attr": attr(bg = "green", flags = "dim"),
            "line_number_attr":       attr(fg = "gray", bg = "default"),
            "highlight_cursor_attr":  attr(fg = "white", bg = "gray", flags = "bold"),
        },
        # Whether certain language-specific folds start hidden when a file is
        # opened.
        "initial_folds": True,
        # file_explorer configures the :fexplorer tree view. Indent
        # and icon attributes default to gray so the guides and glyphs
        # recede visually behind file names.
        "file_explorer": {
            "indent_attr": attr(fg = "gray"),
            "icon_attr":   attr(fg = "gray"),
            # When True, the explorer refuses edits and writes; browsing,
            # expanding and opening files still work.
            "read_only":   False,
            # Key that leaves read_only for the rest of the visit.
            "edit_key":    "<shift-esc>",
            # Width the split falls back to when the tree renders nothing,
            # so an empty or fully ignored workspace stays usable.
            "min_width":   24,
            # Bottom row naming the next action available in the current
            # mode, styled by hint_attr.
            "hint":        True,
            "hint_attr":   attr(fg = "gray"),
        },
    },
    # This maps extension ID to extension configuration.
    # New extensions are added when installed via the shell's
    # command pkg install.
    "extensions": {},
    # Extension and plugin permission authorizer.
    "authorizer": {
        # Automatically grant non-command permission requests from extensions
        # and plugins. Persisted decisions still apply.
        "auto_authorize_extensions": True,
        # Automatically grant workspace command execution requests.
        "auto_authorize_commands": False,
    },
    "updates": {
        # Automatically install language packages on demand. When False,
        # Rune prompts before installing a missing package. First-run
        # onboarding installs without prompting regardless of this setting.
        "auto_install": False,
    },
    # Anonymous usage telemetry. See the Telemetry page in the Rune docs for
    # the full list of what is reported.
    "telemetry": {
        # Report anonymous usage and system information. When False, Rune
        # sends nothing and never creates an install identifier.
        "enabled": True,
    },
    # Interactive tutorials. Each entry maps a `:tutorial start <name>` to
    # the path of a Starlark file that calls `tutorial(...)` with a
    # list of `step(...)` entries. Empty by default — extensions and
    # user overlays add their own.
    "tutorials": {},
    # Configuration for the command prompt.
    "command": {
        # Key that opens the command prompt.
        "key":          ":",
        # Key that toggles between command list and history in the prompt.
        "history_key":  "<m-r>",
        # Maximum number of commands stored in history.
        "max_history":  20000,
        # Aliases layer on top of the built-ins.
        "aliases":      {
            "e":              {"command": "edit", "completer": "files"},
            "w":              "write",
            "save":           "write",
            # `upgrade` lives in the console so the manifest check and
            # the install can stream progress; keep the short prompt
            # entry pointing at it.
            "upgrade":        "console upgrade",
            "sed":            "!! gsed -i $1 $FILE",
            "gitnextchange":  "jumptolocation next gitchange",
            "gitprevchange":  "jumptolocation previous gitchange",
            "lspnextdiagnostic": "jumptolocation next lsp-diagnostics",
            "lspprevdiagnostic": "jumptolocation previous lsp-diagnostics",
            # locationpicker re-shell-quotes each token before
            # handing the joined command to $SHELL -c, so alias
            # bodies do not need to add extra single/double
            # quoting for values that survive Layer 1 prompt
            # tokenisation as a single token (no embedded
            # whitespace).
            "grep":           "locationpicker grep -n -R $1",
            "todogrep":       "locationpicker grep -n -R -E (TODO|FIXME)",
            # ERE, not BRE: with `\|` alternation, Apple's git drops
            # the `^=======$` branch while GNU's keeps it, so the same
            # alias listed different markers per platform.
            "conflicts":      "locationpicker git grep -n --column -E '^(<<<<<<<|=======$|>>>>>>>)'",
            "gitgrep":        "locationpicker git grep -n --column -- $1",
            # gitchanges: present every changed hunk (working tree
            # vs HEAD) as a `path:line:1:hunk` location. The awk
            # script reads `git diff -U0` from a sub-pipe and
            # extracts the post-image start line of each `@@` hunk
            # header. -U0 keeps each hunk anchored to a single
            # changed line so the picker entry points directly at
            # the edit. Single-quoting protects the awk body from
            # Layer 1 prompt tokenisation; locationpicker then
            # re-shell-quotes it so the whole script reaches awk
            # as one argv element.
            "gitchanges":     "locationpicker awk 'BEGIN { cmd = \"git diff HEAD --no-color -U0\"; while ((cmd | getline line) > 0) { if (substr(line, 1, 6) == \"+++ b/\") f = substr(line, 7); else if (substr(line, 1, 2) == \"@@\") { match(line, /[+][0-9]+/); print f \":\" substr(line, RSTART+1, RLENGTH-1) \":1:\" line } } }'",
            "gitblame":       "! git blame $FILE",
            "gitdiff":        "! git diff",
            "shadercancel":   "shaderrun nop 500ms",
            "tabnew":         "echo {prompt}edit<space>",
            "tabsearch":      "echo {prompt}tabfocus<space>",
            "workspacesearch":"echo {prompt}workspacefocus<space>",
            "worktreenew": [
                '!! ROOT=$(git rev-parse --git-common-dir) && ROOT=$(cd "$ROOT/.." && pwd) || exit 1',
                '!! ROOT_HASH=$(printf %s "$ROOT" | (sha256sum 2>/dev/null || shasum -a 256) | cut -c1-4)',
                '!! ROOT_NAME=${ROOT##*/}',
                '!! WORKTREE=$RUNE_DATADIR/worktrees/$ROOT_NAME-$ROOT_HASH/$1',
                '!! git worktree add "$WORKTREE" -b $1',
                'workspaceopen $WORKTREE',
                'workspaceready workspacerename $1',
            ],
            "worktreeopen": {
                "command": [
                    '!! ROOT=$(git rev-parse --git-common-dir) && ROOT=$(cd "$ROOT/.." && pwd) || exit 1',
                    '!! ROOT_HASH=$(printf %s "$ROOT" | (sha256sum 2>/dev/null || shasum -a 256) | cut -c1-4)',
                    '!! ROOT_NAME=${ROOT##*/}',
                    '!! WORKTREE=$RUNE_DATADIR/worktrees/$ROOT_NAME-$ROOT_HASH/$1',
                    "workspaceopen $WORKTREE",
                    "workspaceready workspacerename $1",
                    "workspaceready shaderrun shine 600ms",
                ],
                "completer": "! $SHELL -c 'git worktree list | tail -n +2 | cut -d\" \" -f1 | xargs -n1 basename'",
            },
            "worktreeremove": {
                "command": [
                    "!! git worktree remove $1",
                    "shaderrun embers 600ms",
                ],
                "completer": "! $SHELL -c 'git worktree list | tail -n +2 | cut -d\" \" -f1 | xargs -n1 basename'",
            },
            "docs":           "workspaceopen docs:///",
            "help": [
                "workspaceopen docs:///",
                "extensionready rune-agent ? I need help",
            ],
        },
        # Key bindings are owned entirely by the editor preset written to
        # the user config (preset_modal.yaml, preset_standard_*.yaml,
        # preset_emacs.yaml). Set a value to "" there to unbind.
        "key_bindings": {},
        # Prompt colors.
        "element_attr":       attr(fg = "default", bg = "default", flags = ["dim"]),
        "manual_attr":        attr(fg = "default", bg = "default"),
        "matched_text_attr":  attr(fg = "blue", bg = "default", flags = ["bold"]),
        "focus_element_attr": attr(fg = "purple", bg = "default"),
        # Glyphs used to stitch the prompt's manual/list separator
        # row into the surrounding window frame.
        "separator_charset": {
            "left":             "🭼",
            "horizontal_left":  "▁",
            "horizontal_right": "▁",
            "right":            "🭿",
        },
    },
    "browser": {
        # What the workspace bar shows: the simplified path ("path"), the
        # workspace number ("number"), or false to disable the bar entirely.
        "workspace_bar": "path",
        "window_manager": {
            # Dim unfocused panes. True/False toggle brightness dimming;
            # "b&w" (or "bw") strips color from unfocused panes instead.
            "dim":                   True,
            "frame":                 True,
            "frame_attr":            attr(fg = "gray", bg = "default"),
            "focus_frame_attr":      attr(fg = "silver", bg = "default"),
            "frame_charset":         GUI_FRAME_CHARSET,
            "focus_frame_charset":   GUI_FRAME_CHARSET,
            "scroll_bar_attr":       attr(fg = "blue", bg = "default"),
            "scroll_bar_char":       "▐",
            "scroll_bar_hover_char": "█",
            # Solid bar drawn over floating windows' top frame line.
            # Dragging the bar moves the window, dragging edges resizes
            # it, double-clicking it maximizes the window, and the close
            # icon closes it. The icon and title backgrounds follow the
            # frame foreground.
            "window_bar":            True,
            "window_bar_charset":    {
                "left":       "█",
                "horizontal": "█",
                "right":      "█",
            },
            "close_icon":            "",
            "close_icon_attr":       attr(fg = "red"),
        },
        "union_frames":       False,
        "frameunion_charset": {
            "left":   "🭽",
            "right":  "🭾",
            "top":    "🭿",
            "bottom": "🭼",
        },
        # Separator characters used to space out tab names.
        "focus_tab_attr":          attr(fg = "silver", bg = "default"),
        "dirty_tab_attr":          attr(fg = "red", bg = "default", flags = ["italic"]),
        "non_focus_tab_attr":      attr(fg = "gray", bg = "default"),
        "focus_tab_icon_attr":     attr(fg = "yellow", bg = "default"),
        "non_focus_tab_icon_attr": attr(fg = "silver", bg = "default"),
        "focus_tab_highlight_attr": attr(fg = "red", bg = "default"),
        "focus_tab_highlight_char":  "\U00100006",
        # Icons for tabs; `terminal` is used for terminal tabs while
        # `default` is the fallback for files.
        "icons": {
            "default":  "",
            "terminal": "",
            "directory": "",
            "open_directory": "",
        },
        # If set, every tab icon rendered by the browser is forced
        # to this single rune, ignoring per-source icons (file
        # glyphs, `browser.icons`, terminal/shell icons, etc.).
        # Empty disables.
        "tab_override_icon": "",
        "tab_name_separator": "   ",
        # Prompt configuration used when Rune asks the user questions.
        "prompt": {
            "text_attr":       attr(bg = "gray", flag = "bold"),
            "highlight_attr":  attr(bg = "red", flag = "bold"),
            "background_attr": attr(fg = "default", bg = "default"),
        },
    },
    # Workspace configuration. This configuration is never reloaded.
    "workspace": {
        # Path to the home workspace opened when Rune starts. Defaults to the
        # user's home directory. A leading "~" expands to the user's home
        # directory.
        "home":         "~",
        # Configuration for remote workspaces connected over SSH.
        "ssh":          {
            "timeout":         "5s",
            "skip_preflight":  True,
        },
        # Automatically restore the previous session's files, terminals, and
        # window layout.
        "auto_restore": True,
        # Index workspace symbols for faster lookups.
        "symboldb":     True,
        # Attributes for custom ASCII or image wallpapers.
        "wallpaper_attr":            attr(fg = "blue", bg = "default"),
        "wallpaper_background_attr": attr(bg = "default"),
        # Character drawn on the bottom row of the workspace tab bar to
        # highlight the workspace currently in focus.
        "focus_tab_highlight_char":  "",
        # Per-workspace notice shown in a floating window when the
        # workspace opens. Either `path` (resolved against the
        # workspace filesystem) or `literal` provides the content;
        # when both are set, `literal` wins. A `.md` path renders as
        # markdown; other paths render as plain text. `show` is
        # "once" (deduplicated by content fingerprint) or "always".
        "notice": {
            "path":    "",
            "literal": "",
            "show":    "once",
        },
    },
    # Private network of your own Rune instances. When joined, this
    # machine serves its workspaces to your other machines, which can
    # then be opened with
    # `workspaceopen rune://<machine>/<path>`. Only machines signed in
    # with your own account are allowed to connect. Manage it with the
    # `network` console command.
    "network": {
        # Whether to join the network on startup. When off, the network
        # stays available through `network up` — no restart needed.
        "auto_join":   True,
        # Name this machine is known by on the network, and the name used
        # in rune:// addresses. Empty uses the machine's hostname.
        "hostname":    "",
        # Port workspaces are served on. Only reachable from the network.
        "port":        7473,
    },
    # Notification pop-up configuration.
    "notifications": {
        "auto_close":   "5s",
        "padding":      1,
        "progress_bar": True,
        "progress_format": {
            "start":       "\U00100000",
            "current":     "\U00100001",
            "current_tip": "\U00100002",
            "remain":      "\U00100003",
            "end":         "\U00100004",
        },
        "attr":            attr(fg = "default", bg = "default"),
        "background_attr": attr(fg = "default", bg = "default"),
        "frame_charset":   GUI_FRAME_CHARSET,
    },
    "terminal": {
        # Whether to respect the title set by the shell.
        "dynamic_tab_name":   True,
        # Maximum emulator lines. Increasing this slows resizing linearly.
        "max_lines":          1000,
        # Number of pre-initialized emulator instances kept in the reservoir.
        # A value of 0 disables the reservoir.
        "initial_reservoir":  0,
        "attr":               attr(fg = "default", bg = "default"),
        "selection_attr":     attr(flags = "reverse"),
        # Tab attributes used when a bell arrives while the window is unfocused.
        "needs_attention_attr": attr(fg = "red", flags = "blink"),
        # Scrollback search, opened over the terminal itself. Its theme
        # follows editor.standard.search; only the key that opens it can be
        # overridden here.
        "search": {
            "find_key": "<m-f>",
        },
        # Configuration for programs executed via the `!` command.
        "plugin": {
            # Layout of the plugin status bar. Available components include:
            # Command, StatusIcon, ExitStatus, Elapsed, AlignRight, AlignCenter.
            # Pipe operators support bg, fg, bold, italic, underline, reverse,
            # and dim.
            "bar_layout": ' {{ .StatusIcon | bg "gray" | fg "white" }} █▓▒░{{ .AlignCenter}}{{ .Command | fg "white" | bold }}{{ .AlignRight }}  ░▒▓█ {{ .Elapsed | fg "white" | bg "gray" }} ',
            "status_error_icon":   "",
            "status_error_attr":   attr(fg = "red"),
            "status_success_icon": "",
            "status_success_attr": attr(fg = "green"),
            "animation": "⠃⠅⠆⠘⠨⠰⠉⠒⠤⠑⠡⠢⠊⠌⠔⠇⠸⠎⠱⠣⠜⠪⠕⠋⠙⠓⠚⠍⠩⠥⠬⠖⠲⠦⠴⠏⠹⠧⠼⠫⠝⠮⠵⠺⠗⠞⠳⠛⠭⠶⠟⠻⠷⠾⠯⠽⠿",
            "bar_background_attr": attr(bg = "default"),
            # Whether the plugin bar is rendered at the bottom instead of top.
            "bar_align_bottom":    True,
        },
    },
}


if tui:
    tui_cmd_bindings = {
        "<c-w>":          "tabclose",
        "<c-l>":          "tabnext",
        "<c-h>":          "tabprevious",
        "<c-x><c-v>":     "clipboardpaste",
        "<c-x><c-c>":     "clipboardcopy",
        "<c-x><c-h>":     "windowfocus left",
        "<c-x><c-l>":     "windowfocus right",
        "<c-x><c-j>":     "windowfocus down",
        "<c-x><c-k>":     "windowfocus up",
        "<c-x><c-w>":     "windowclose",
        "<c-x>h":         "windowdefaultsplit h",
        "<c-x>v":         "windowdefaultsplit v",
        "<c-j>":          "lspnextdiagnostic",
        "<c-k>":          "lspprevdiagnostic",
        "<c-x><c-f>":     "windowtogglemaximize",
        "<c-x><c-t>":     "lsp hover",
        "<c-x><c-e>":     "lsp references",
        "<c-x><c-g>":     "lsp definition",
        "<c-x><c-b>":     "lsp format",
        "<c-x><c-p>":     "searchfile",
        "<c-x><c-\\>":    "searchtext",
        "<c-x><enter>":   "terminalneworsplit",
        "gf":             "editfileoncursor",
        "<c-x>1":         "workspacefocus 1",
        "<c-x>2":         "workspacefocus 2",
        "<c-x>3":         "workspacefocus 3",
        "<c-x>4":         "workspacefocus 4",
        "<c-x>5":         "workspacefocus 5",
        "<c-x>6":         "workspacefocus 6",
        "<c-x>7":         "workspacefocus 7",
        "<c-x>8":         "workspacefocus 8",
        "<c-x>9":         "workspacefocus 9",
    }

    config = merge(config, {
        "default_attr": attr(fg = "default", bg = "#1e1e1e"),
            "log_path":  "~/.rune/debug.log",
        "log_level": "info",
        "input_mode": ["esc", "mouse"],
        "editor": {
            "modal": {
                "attr":        attr(fg = "default", bg = "#1e1e1e"),
                "message_bar": {"attr": attr(fg = "default", bg = "#1e1e1e")},
                "search_attr": attr(fg = "default", bg = "#1e1e1e", flags = "reverse"),
            },
            "standard": {
                "attr":        attr(fg = "default", bg = "#1e1e1e"),
                "search_attr": attr(fg = "default", bg = "#1e1e1e", flags = "reverse"),
                "search": {
                    "find_key":          "<m-f>",
                    "replace_key":       "<m-r>",
                    "attr":              attr(fg = "default", bg = "#1e1e1e"),
                    "input_attr":        attr(fg = "default", bg = "#1e1e1e"),
                    "placeholder_attr":  attr(fg = "gray", bg = "#1e1e1e"),
                    "frame_attr":        attr(fg = "#3a3a3a", bg = "#1e1e1e"),
                    "focus_frame_attr":  attr(fg = "silver", bg = "#1e1e1e"),
                    "button_attr":       attr(fg = "default", bg = "gray"),
                    "button_hover_attr": attr(fg = "default", bg = "blue"),
                    "match_attr":        attr(fg = "default", bg = "#1e1e1e", flags = "reverse"),
                    "current_match_attr": attr(fg = "default", bg = "#1e1e1e"),
                },
            },
            "aux_bar": {
                "git_del_inline_attr":    attr(fg = "maroon", flags = "bold"),
                "git_add_inline_attr":    attr(fg = "green", flags = "bold"),
                "git_del_locations_attr": attr(bg = "maroon", flags = "dim"),
                "git_add_locations_attr": attr(bg = "green", flags = "dim"),
                "line_number_attr":       attr(fg = "gray", bg = "default"),
                "highlight_cursor_attr":  attr(fg = "white", bg = "gray", flags = "bold"),
            },
            "status_bar": {"background_attr": attr(bg = "gray")},
        },
        "extensions": {
            "fuzzy_search": {
                "path": "extension_fuzzy_search",
                "config": {
                    "file": {
                        "history_key":         "<c-p>",
                        "element_attr":        attr(fg = "default", bg = "#1e1e1e"),
                        "matched_text_attr":   attr(fg = "#D34728", bg = "#1e1e1e", flags = ["bold"]),
                        "count_attr":          attr(fg = "default", bg = "#1e1e1e"),
                        "focus_element_attr":  attr(fg = "#c6c6c6", bg = "#1e1e1e", flags = ["bold"]),
                    },
                    "line": {
                        "history_key":         "<c-\\\\>",
                        "element_attr":        attr(fg = "default", bg = "#1e1e1e"),
                        "matched_text_attr":   attr(fg = "#D34728", bg = "#1e1e1e", flags = ["bold", "italic"]),
                        "count_attr":          attr(fg = "default", bg = "#1e1e1e"),
                        "focus_element_attr":  attr(fg = "#c6c6c6", bg = "#1e1e1e", flags = ["bold"]),
                    },
                },
            },
        },
        "command": {
            "key_bindings": {
                "<c-w>":          "tabclose",
                "<c-l>":          "tabnext",
                "<c-h>":          "tabprevious",
                "<c-x><c-v>":     "clipboardpaste",
                "<c-x><c-c>":     "clipboardcopy",
                "<c-x><c-h>":     "windowfocus left",
                "<c-x><c-l>":     "windowfocus right",
                "<c-x><c-j>":     "windowfocus down",
                "<c-x><c-k>":     "windowfocus up",
                "<c-x><c-w>":     "windowclose",
                "<c-x>h":         "windowdefaultsplit h",
                "<c-x>v":         "windowdefaultsplit v",
                "<c-j>":          "lspnextdiagnostic",
                "<c-k>":          "lspprevdiagnostic",
                "<c-x><c-f>":     "windowtogglemaximize",
                "<c-x><c-t>":     "lsp hover",
                "<c-x><c-e>":     "lsp references",
                "<c-x><c-g>":     "lsp definition",
                "<c-x><c-b>":     "lsp format",
                "<c-x><c-p>":     "searchfile",
                "<c-x><c-\\\\>": "searchtext",
                "<c-x><enter>":   "terminalneworsplit",
                "gf":             "editfileoncursor",
                "<c-x>1":         "workspacefocus 1",
                "<c-x>2":         "workspacefocus 2",
                "<c-x>3":         "workspacefocus 3",
                "<c-x>4":         "workspacefocus 4",
                "<c-x>5":         "workspacefocus 5",
                "<c-x>6":         "workspacefocus 6",
                "<c-x>7":         "workspacefocus 7",
                "<c-x>8":         "workspacefocus 8",
                "<c-x>9":         "workspacefocus 9",
            },
            "element_attr":      attr(fg = "default", bg = "#1e1e1e"),
            "manual_attr":       attr(fg = "default", bg = "#1e1e1e"),
            "matched_text_attr": attr(fg = "#D34728", bg = "#1e1e1e", flags = ["bold", "italic"]),
            "focus_element_attr":attr(fg = "#c6c6c6", bg = "#1e1e1e", flags = ["bold"]),
            "separator_charset": {
                "left":             "├",
                "horizontal_left":  "─",
                "horizontal_right": "─",
                "right":            "┤",
            },
        },
        "browser": {
            "union_frames": True,
            "window_manager": {
                # Dim unfocused panes. True/False toggle brightness
                # dimming; "b&w" (or "bw") strips color instead.
                "dim":                   True,
                "frame":                 True,
                "frame_attr":            attr(fg = "#3a3a3a", bg = "#1e1e1e"),
                "focus_frame_attr":      attr(fg = "#c6c6c6", bg = "#1e1e1e", flags = "bold"),
                "frame_charset":         TUI_FRAME_CHARSET,
                "focus_frame_charset":   TUI_FRAME_CHARSET,
                "scroll_bar_attr":       attr(fg = "#c6c6c6", bg = "#1e1e1e"),
                "scroll_bar_char":       "┃",
                "scroll_bar_hover_char": "║",
                "window_bar":            False,
            },
            "tab_name_separator":  "  ",
            "frameunion_charset":  {
                "left":   "├",
                "right":  "┤",
                "top":    "┬",
                "bottom": "┴",
            },
            "focus_tab_attr":          attr(fg = "#c6c6c6", bg = "#1e1e1e", flags = ["bold"]),
            "dirty_tab_attr":          attr(fg = "#D34728", bg = "#1e1e1e", flags = ["italic"]),
            "non_focus_tab_attr":      attr(fg = "gray", bg = "#1e1e1e"),
            "focus_tab_icon_attr":     attr(fg = "#c6c6c6", bg = "#1e1e1e", flags = ["bold"]),
            "non_focus_tab_icon_attr": attr(fg = "gray", bg = "#1e1e1e"),
            "focus_tab_highlight_attr": attr(fg = "yellow", bg = "#1e1e1e"),
            "focus_tab_highlight_char": "━",
            "prompt": {
                "text_attr":       attr(fg = "default", bg = "#1e1e1e"),
                "highlight_attr":  attr(fg = "#c6c6c6", bg = "default", flag = "reverse"),
                "background_attr": attr(fg = "default", bg = "#1e1e1e"),
            },
        },
        "workspace": {
            "wallpaper_attr":            attr(fg = "#D34728", bg = "#1e1e1e"),
            "wallpaper_background_attr": attr(bg = "#1e1e1e"),
            "focus_tab_highlight_char":  "━",
        },
        "notifications": {
            "attr":            attr(fg = "default", bg = "#1e1e1e"),
            "background_attr": attr(fg = "default", bg = "#1e1e1e"),
            "frame_charset":   TUI_FRAME_CHARSET,
        },
        "terminal": {
            "attr":                  attr(fg = "default", bg = "#1e1e1e"),
            "selection_attr":        attr(flags = "reverse"),
            "needs_attention_attr":  attr(fg = "red", flags = "blink"),
            "plugin": {
                "bar_align_bottom":    False,
            },
        },
    })
