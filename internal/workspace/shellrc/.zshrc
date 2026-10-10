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
