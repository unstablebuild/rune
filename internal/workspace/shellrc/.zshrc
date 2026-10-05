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
