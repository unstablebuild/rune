# OSC 133 marks each prompt and command, which tells Rune a shell running
# a command from one waiting at its prompt even when the user's files turn
# job control off, and with it the process group each job gets. Only PS0,
# which came in bash 4.4, can mark a command's start: marking the prompts
# alone would read every command as done.
if (( BASH_VERSINFO[0] > 4 || (BASH_VERSINFO[0] == 4 && BASH_VERSINFO[1] >= 4) )); then
	PS0+='\e]133;C\a'
	# Keeps $? for the prompt commands after it, which may show it.
	__rune_mark_prompt() {
		local s=$?
		printf '\e]133;D\a\e]133;A\a'
		return "$s"
	}
	PROMPT_COMMAND="__rune_mark_prompt${PROMPT_COMMAND:+;$PROMPT_COMMAND}"
fi
