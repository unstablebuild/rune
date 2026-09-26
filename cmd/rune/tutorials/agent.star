# Copyright (C) 2017-2026 The Rune Authors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# This program is free software: you can redistribute it and/or modify
# it under the terms of the GNU General Public License as published by
# the Free Software Foundation, either version 3 of the License, or (at
# your option) any later version.
#
# This program is distributed in the hope that it will be useful, but
# WITHOUT ANY WARRANTY; without even the implied warranty of
# MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
# General Public License for more details.
#
# You should have received a copy of the GNU General Public License
# along with this program. If not, see <https://www.gnu.org/licenses/>.


# agent.star — the Rune Agent tutorial.

ck = command_key()
mode = editor_mode()

# A focused console is in INSERT mode under a modal editor, so the
# command key would be typed into the shell line instead.
if mode == "vim" or mode == "helix":
    shell_esc_step = "1. Press `<esc>` to go back to NORMAL mode.\n\n"
    shell_prompt_step_num = "2"
    shell_run_step_num = "3"
else:
    shell_esc_step = ""
    shell_prompt_step_num = "1"
    shell_run_step_num = "2"

def keypress(cmd, *args):
    k = key_for(cmd, *args)
    if k:
        return "press `" + k + "`"
    return ("open the command prompt (`" + ck + "`) and run `" +
            cmd + ((" " + " ".join(args)) if len(args) else "") + "`")

history_key = key_for("history")
history_opens = ("press `" + history_key + "` to open") if history_key else "run `history` to open"

# Links open on Ctrl-click on Linux, where Super belongs to the desktop.
link_modifier = "`<ctrl>`" if os() == "linux" else "`<meta>`"

cleanup_md = """\
Let's start fresh. Clear the layout: """ + keypress("windowcloseall") + """.
"""

agent_install_md = """\
The **Rune Agent** is Rune's builtin AI coding assistant. It ships as a
package you install on demand, so the first step is to install it.

Packages are installed from the **Rune console**, which is not the
command prompt you have been using.

- The prompt (`""" + ck + """`) is one line, and closes again as soon as
  the command runs.

- The console is a separate, durable tab with its own REPL, for the
  commands that want a persistent output window — installing a package,
  checking an extension's status.

The console **sets up** Rune, the prompt **drives** it.

Open the console now:

1. Press `""" + ck + """` to open the command prompt.

2. Type `console` and press Enter.
"""

agent_pkg_install_md = """\
You're in Rune's console now.

- Type `pkg install rune-agent` and press `<enter>`.

- The install takes a few seconds. This step clears once it finishes.

If the agent is already installed, press **Skip** below to move on.
"""

agent_open_md = """\
Start a conversation with the agent using the `agent` command.

""" + shell_esc_step + shell_prompt_step_num + """. Press `""" + ck + """` to open the command prompt.

""" + shell_run_step_num + """. Run `agent`.

`agent` takes two optional arguments, a conversation name and a model:

- `agent` alone starts a new conversation on your default provider.

- `agent <name>` names the conversation.

- `agent <name> <model>` names it and picks the model.
"""

help_md = """\
You're almost done 🎉 A few tips worth remembering:

- If you find yourself wondering what commands you typed on a previous session,
  """ + history_opens + """ the command prompt in history mode and search through your command history.

- If you need a hand, or want to learn about hacking on Rune, join us on
  Discord: https://discord.gg/quxhV7khwg 👾 hold """ + link_modifier + """ and click the
  link to open it in your browser.

- There's a `help` command that opens the documentation on a separate workspace
  and fires a help agent that you can ask questions.

Try it now: """ + keypress("help") + """.

Happy hacking 🔥
"""

def teach_provider(provider, label, action_tokens, run_md):
    wait_shell(
        title = "Connect " + label,
        args  = ["models", "providers", provider] + action_tokens,
        text  = run_md,
    )

def teach_cleanup():
    wait_command(
        title   = "Clear the layout",
        command = "windowcloseall",
        text    = cleanup_md,
    )

def teach_agent():
    wait_command(
        title   = "Set up the Rune Agent",
        command = "console",
        text    = agent_install_md,
    )

    wait_shell(
        title = "Install the agent package",
        args  = ["pkg", "install", "rune-agent"],
        text  = agent_pkg_install_md,
    )

    pick = choice(
        message = ("Which provider do you want to connect?\n\n" +
                   "- **OpenAI**: GPT family, billed by API key.\n" +
                   "- **Anthropic**: Claude family, billed by API key.\n" +
                   "- **Gemini**: Gemini family, billed by API key.\n" +
                   "- **Codex**: GPT-5 Codex family. Sign in with " +
                   "ChatGPT (OAuth) and use your ChatGPT subscription, " +
                   "no API key.\n" +
                   "- **Claude**: Claude family through your Claude " +
                   "Pro/Max subscription. Sign in with Claude (OAuth), " +
                   "no API key."),
        options = ["OpenAI", "Anthropic", "Gemini", "Codex", "Claude"],
    )
    if not pick.selected:
        notify(level = info,
               message = ("Configure a provider any time with the " +
                          "`models providers` console command."))
        return

    if pick.value == "OpenAI":
        run_md = """\
Add your OpenAI credentials. The key is stored securely and never
written to your config file.

1. In Rune's console, run `models providers openai add default`.
2. Paste your API key at the redacted prompt.
"""
        teach_provider("openai", "OpenAI", ["add", "default"], run_md)
    elif pick.value == "Anthropic":
        run_md = """\
Add your Anthropic credentials. The key is stored securely and never
written to your config file.

1. In Rune's console, run `models providers anthropic add default`.
2. Paste your API key at the redacted prompt.
"""
        teach_provider("anthropic", "Anthropic", ["add", "default"], run_md)
    elif pick.value == "Gemini":
        run_md = """\
Add your Gemini credentials. The key is stored securely and never
written to your config file.

1. In Rune's console, run `models providers gemini add default`.
2. Paste your API key at the redacted prompt.
"""
        teach_provider("gemini", "Gemini", ["add", "default"], run_md)
    elif pick.value == "Codex":
        run_md = """\
Codex authenticates through your browser. No API key is needed.

1. In Rune's console, run `models providers codex login`.
2. Finish the sign-in in your browser.
"""
        teach_provider("codex", "Codex", ["login"], run_md)
    else:
        run_md = """\
Claude signs in through your browser with your Claude Pro or Max
subscription. There is no API key to paste.

Agent activity through this provider draws from your Claude plan's
separate monthly Agent SDK credit, not your interactive usage limits.
Once that credit runs out, further usage bills at standard API rates if
you have usage credits enabled, and otherwise pauses until the credit
refreshes. The separate `anthropic` provider bills the same models by
API key instead.

1. In Rune's console, run `models providers claude login`.
2. Finish the sign-in in your browser.
"""
        teach_provider("claude", "Claude", ["login"], run_md)

    wait_command(
        title   = "Open the Rune Agent",
        command = "agent",
        text    = agent_open_md,
    )

def teach_help():
    wait_command(
        title   = "One last thing",
        command = "help",
        text    = help_md,
    )

def run():
    if not workspace_open():
        fail("Open a workspace first before running this tutorial. Open the " +
             "command prompt with `" + ck + "` and run `workspaceopen`.")
        return

    teach_cleanup()
    teach_agent()
    teach_help()

tutorial(id = "agent", title = "Rune Agent", version = "14", entry = run)
