[[ -f "$RUNE_REAL_HOME/.zlogin" ]] && source "$RUNE_REAL_HOME/.zlogin"

bindkey '^G' beep
bindkey '^A' beginning-of-line
bindkey '^E' end-of-line
bindkey '^[[3~' delete-char
