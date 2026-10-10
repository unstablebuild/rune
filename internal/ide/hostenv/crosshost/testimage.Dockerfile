# Appended to deploy/rune-headless/Dockerfile by the crosshost e2e suite,
# so it continues that file's runtime stage: the remote host under test runs
# the very `rune` binary the headless image ships. Nothing here builds on its
# own.
#
# The layer adds what the suite needs to reach and probe that binary:
#   - sshd, so the client connects the way an SSH workspace does and starts
#     `rune -x` through ~/.local/bin/rune;
#   - zsh and fish, whose terminal startup the suite checks next to bash's;
#   - git, whose http-backend serves the packages the suite installs.
#
# ~/.local/bin/rune points every API endpoint at a closed local port, so the
# remote rune never reaches the network and a missing service fails fast
# rather than after a timeout.
USER root
RUN set -eux; \
	apt-get update; \
	apt-get install -y --no-install-recommends openssh-server zsh fish git; \
	rm -rf /var/lib/apt/lists/*; \
	ssh-keygen -A; \
	mkdir -p /run/sshd; \
	usermod -p '*' rune; \
	install -d -o rune -g rune -m 700 /home/rune/.ssh; \
	install -d -o rune -g rune /home/rune/.local /home/rune/.local/bin; \
	printf '%s\n' '#!/bin/sh' \
		'exec /usr/local/bin/rune --rune-http-address=http://127.0.0.1:9 "$@"' \
		> /home/rune/.local/bin/rune; \
	chmod 755 /home/rune/.local/bin/rune; \
	chown rune:rune /home/rune/.local/bin/rune; \
	printf '%s\n' \
		'PasswordAuthentication no' \
		'KbdInteractiveAuthentication no' \
		'PermitUserEnvironment yes' \
		'AllowUsers rune' \
		> /etc/ssh/sshd_config.d/crosshost.conf

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/usr/sbin/sshd", "-D", "-e"]
