// Copyright (C) 2017-2026 The Rune Authors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or (at
// your option) any later version.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

//go:build e2e

package crosshost

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/workspace/workspacessh"
)

// localDataDir stands in for the data directory of the editor driving the
// remote host, and is what a client that leaked its own $RUNE_DATADIR would
// send.
func localDataDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".rune-local")
	t.Setenv("RUNE_DATADIR", dir)
	return dir
}

func TestRemoteCommandExpandsDataDirOnTheRemoteHost(t *testing.T) {
	local := localDataDir(t)
	h := startHost(t)
	// A data directory other than the default proves the server expands the
	// one it runs with, not a well-known path.
	const dataDir = remoteHome + "/.rune-alt"
	h.writeFile(t, dataDir+"/tools/print-args", `#!/bin/sh
printf 'self<%s>\n' "$0"
for arg; do printf 'arg<%s>\n' "$arg"; done
printf 'env<%s>\n' "$E2E_ENV"
printf 'keep<%s>\n' "$E2E_KEEP"
printf 'datadir<%s>\n' "$(printenv RUNE_DATADIR)"
`, 0o755)
	s := h.connect(t, h.workspaceDir(t, "ws"), workspacessh.WithRemoteDataDir(".rune-alt"))

	out := run(t, s, workspaceapi.Cmd{
		Path: "$RUNE_DATADIR/tools/print-args",
		Args: []string{"$RUNE_DATADIR/plain", "${RUNE_DATADIR}/braced", "$HOME/other"},
		Env:  []string{"E2E_ENV=$RUNE_DATADIR/env", "E2E_KEEP=$HOME/env"},
	})

	assert.Equal(t, strings.Join([]string{
		"self<" + dataDir + "/tools/print-args>",
		"arg<" + dataDir + "/plain>",
		"arg<" + dataDir + "/braced>",
		"arg<$HOME/other>",
		"env<" + dataDir + "/env>",
		"keep<$HOME/env>",
		"datadir<" + dataDir + ">",
	}, "\n")+"\n", out,
		"the remote host expands $RUNE_DATADIR in the path, arguments and "+
			"environment to its own data directory, and leaves every other "+
			"variable to the program")
	assert.NotContains(t, out, local, "the client's data directory must not cross to the remote host")
}

func TestRemoteServerAppliesItsOwnGUIEnv(t *testing.T) {
	local := localDataDir(t)
	t.Setenv("RUNE_E2E_VAR", "from-local-process")
	h := startHost(t)
	const dataDir = remoteHome + "/.rune"
	h.writeFile(t, dataDir+"/config.yaml", `gui:
  env:
    RUNE_E2E_VAR: from-remote-config
    RUNE_E2E_DATA: $RUNE_DATADIR/data
    PATH: $RUNE_DATADIR/tools/bin:$PATH:/opt/after/bin
`, 0o644)
	ws := h.workspaceDir(t, "ws")
	h.writeFile(t, ws+"/.rune/config.yaml", `gui:
  env:
    RUNE_E2E_WS_VAR: from-workspace-config
`, 0o644)

	env := environ(t, h.connect(t, ws, workspacessh.WithRemoteDataDir(".rune")))

	assert.Equal(t, dataDir, env["RUNE_DATADIR"])
	assert.Equal(t, "from-remote-config", env["RUNE_E2E_VAR"],
		"the remote host applies its own user config")
	assert.Equal(t, dataDir+"/data", env["RUNE_E2E_DATA"],
		"$RUNE_DATADIR in gui.env names the remote data directory")
	assert.Equal(t, "from-workspace-config", env["RUNE_E2E_WS_VAR"],
		"the remote host overlays the workspace config")
	path := pathEntries(env["PATH"])
	require.GreaterOrEqual(t, len(path), 3, "PATH=%q", env["PATH"])
	assert.Equal(t, []string{dataDir + "/bin", dataDir + "/tools/bin"}, path[:2],
		"the data directory's bin and then the gui.env entries lead PATH")
	assert.Equal(t, "/opt/after/bin", path[len(path)-1],
		"gui.env entries after $PATH end it")
	assert.NotContains(t, env["PATH"], local)
	h.exec(t, "test -d "+dataDir+"/bin && test -d "+dataDir+"/lib")
}

func TestRemoteServerKeepsServiceAndLoginShellPATH(t *testing.T) {
	h := startHost(t)
	// sshd starts `rune -x` with this PATH, the way a service manager would,
	// and the login shell assigns a PATH of its own, as Debian's /etc/profile
	// does, before the profile adds to it.
	h.writeFile(t, remoteHome+"/.ssh/environment",
		"PATH=/opt/service-only/bin:/usr/local/bin:/usr/bin:/bin\n", 0o600)
	h.writeFile(t, remoteHome+"/.profile",
		"export PATH=\"/opt/login-only/bin:$PATH\"\n", 0o644)

	env := environ(t, h.connect(t, h.workspaceDir(t, "ws")))

	path := pathEntries(env["PATH"])
	assert.Equal(t, remoteHome+"/.rune/bin", path[0])
	assert.Contains(t, path, "/usr/local/games",
		"the login shell's PATH, which /etc/profile assigns, is the base")
	requireBefore(t, path, "/opt/login-only/bin", "/opt/service-only/bin")
	assert.Contains(t, path, remoteHome+"/.local/bin",
		"entries only the inherited PATH has are kept after the login PATH")
}

// shellDotfiles override the probe variable and assign PATH outright, in
// every startup file a terminal shell may read, and mark which ones it read.
var shellDotfiles = map[string]string{
	".profile": `export RUNE_E2E_VAR=from-profile
export RUNE_E2E_LOGIN=read
export PATH=/usr/bin:/bin
`,
	// Guarded like Debian's default, since bash also reads it for the
	// command sshd runs, `rune -x` itself.
	".bashrc": `case $- in *i*) ;; *) return ;; esac
export RUNE_E2E_VAR=from-bashrc
export RUNE_E2E_RC=read
export PATH=/usr/bin:/bin
`,
	".zshrc": `export RUNE_E2E_VAR=from-zshrc
export RUNE_E2E_RC=read
export PATH=/usr/bin:/bin
`,
	".zlogin": `export RUNE_E2E_VAR=from-zlogin
export RUNE_E2E_LOGIN=read
export PATH=/usr/bin:/bin
`,
	".config/fish/config.fish": `set -gx RUNE_E2E_VAR from-fish-config
set -gx RUNE_E2E_RC read
status is-login; and set -gx RUNE_E2E_LOGIN read
set -gx PATH /usr/bin /bin
`,
}

// probe prints the probe variables and PATH in any of the shells under test.
const probe = `printf 'VAR<%s> RC<%s> LOGIN<%s> PATH<%s>\n' "$RUNE_E2E_VAR" "$RUNE_E2E_RC" "$RUNE_E2E_LOGIN" "$PATH"`

func TestRemoteTerminalShellsApplyRuneEnvironmentLast(t *testing.T) {
	h := startHost(t)
	const dataDir = remoteHome + "/.rune"
	h.writeFile(t, dataDir+"/config.yaml", `gui:
  env:
    RUNE_E2E_VAR: from-gui-env
    PATH: $RUNE_DATADIR/tools/bin:$PATH:/opt/after/bin
`, 0o644)
	for name, content := range shellDotfiles {
		h.writeFile(t, remoteHome+"/"+name, content, 0o644)
	}
	s := h.connect(t, h.workspaceDir(t, "ws"))

	for _, tc := range []struct {
		name  string
		path  string
		args  []string
		login bool
		rc    bool
	}{
		{name: "bash login", path: "/bin/bash", args: []string{"-l"}, login: true},
		{name: "bash login long option", path: "/bin/bash", args: []string{"--login", "-i"}, login: true},
		{name: "bash interactive", path: "/bin/bash", rc: true},
		{name: "zsh login", path: "/usr/bin/zsh", args: []string{"-l"}, login: true, rc: true},
		{name: "zsh interactive", path: "/usr/bin/zsh", rc: true},
		{name: "fish login", path: "/usr/bin/fish", args: []string{"-l"}, login: true, rc: true},
		{name: "fish interactive", path: "/usr/bin/fish", rc: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := terminal(t, s, tc.path, tc.args, probe+"; exit\n")

			if tc.login {
				assert.Equal(t, "read", marked(t, out, "LOGIN"),
					"the shell reads the user's login startup files")
			}
			if tc.rc {
				assert.Equal(t, "read", marked(t, out, "RC"),
					"the shell reads the user's interactive startup file")
			}
			assert.Equal(t, "from-gui-env", marked(t, out, "VAR"),
				"gui.env wins over the user's startup files")
			path := pathEntries(marked(t, out, "PATH"))
			require.GreaterOrEqual(t, len(path), 3, "PATH=%q", path)
			assert.Equal(t, []string{dataDir + "/bin", dataDir + "/tools/bin"}, path[:2],
				"Rune's entries lead the PATH the startup files assigned")
			assert.Contains(t, path, "/usr/bin", "the assigned PATH follows")
			assert.Equal(t, "/opt/after/bin", path[len(path)-1])
		})
	}

	// Shells that do not run in a terminal are left to their startup files,
	// which shows the files above do override what the shell inherits.
	for _, tc := range []struct {
		name string
		path string
		args []string
		want string
	}{
		{name: "bash command", path: "/bin/bash", args: []string{"-l", "-c", probe}, want: "from-profile"},
		{name: "zsh command", path: "/usr/bin/zsh", args: []string{"-l", "-c", probe}, want: "from-zlogin"},
		{name: "fish command", path: "/usr/bin/fish", args: []string{"-c", probe}, want: "from-fish-config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := run(t, s, workspaceapi.Cmd{Path: tc.path, Args: tc.args})

			assert.Equal(t, tc.want, marked(t, out, "VAR"))
			assert.Equal(t, "/usr/bin:/bin", marked(t, out, "PATH"))
		})
	}
}
