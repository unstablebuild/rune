[[ -f "$RUNE_REAL_HOME/.zlogin" ]] && source "$RUNE_REAL_HOME/.zlogin"

bindkey '^G' beep
bindkey '^A' beginning-of-line
bindkey '^E' end-of-line
bindkey '^[[3~' delete-char

# login shells read /etc/zlogin and ~/.zlogin after .zshrc, so apply Rune's
# environment again after them
if [[ -r "$ZDOTDIR/env.sh" ]]; then
	source "$ZDOTDIR/env.sh"
fi
