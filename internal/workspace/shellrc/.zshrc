# set it before as well, in case sourcing zshrc fails
bindkey '^G' beep
bindkey '^A' beginning-of-line
bindkey '^E' end-of-line
bindkey '^[[3~' delete-char

[[ -f "$RUNE_REAL_HOME/.zshrc" ]] && source "$RUNE_REAL_HOME/.zshrc"

bindkey '^G' beep
bindkey '^A' beginning-of-line
bindkey '^E' end-of-line
bindkey '^[[3~' delete-char

# after the user's files, so Rune's environment wins over them
if [[ -r "$ZDOTDIR/env.sh" ]]; then
	source "$ZDOTDIR/env.sh"
fi

# OSC 133 marks each prompt and command, which tells Rune a shell running
# a command from one waiting at its prompt even when the user's files turn
# job control off, and with it the process group each job gets.
__rune_mark_prompt() { printf '\e]133;D\a\e]133;A\a' }
__rune_mark_command() { printf '\e]133;C\a' }
autoload -Uz add-zsh-hook
add-zsh-hook precmd __rune_mark_prompt
add-zsh-hook preexec __rune_mark_command
