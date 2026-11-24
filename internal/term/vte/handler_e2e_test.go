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

package vte

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/vte/vtetest"
	"unstable.build/rune/internal/workspace"
)

// TestMain gives the real shells a private HOME and the prompt the
// expected screens assume.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "vte-home")
	if err != nil {
		panic(err)
	}
	prev, hadPrev := os.LookupEnv("HOME")
	if err := os.Setenv("HOME", tmp); err != nil {
		panic(err)
	}
	if err := os.Setenv("PS1", "$ "); err != nil {
		panic(err)
	}
	code := m.Run()
	if hadPrev {
		_ = os.Setenv("HOME", prev)
	} else {
		_ = os.Unsetenv("HOME")
	}
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}

// testSequenceShell plays cases on a terminal running the real shell
// at shell in a kernel pty.
func testSequenceShell(t *testing.T, cfg Config, timeout time.Duration, shell string, cases []vtetest.Case) (
	*Handler, chan struct{},
) {
	return testSequenceCommand(t, cfg, timeout, []string{shell}, cases)
}

// testSequenceCommand plays cases on a terminal running commandAndArgs
// as a host process on the file scheme.
func testSequenceCommand(
	t *testing.T, cfg Config, timeout time.Duration, commandAndArgs []string, cases []vtetest.Case,
) (*Handler, chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	uri, err := workspaceapi.CurrentUserHostURI(os.TempDir())
	require.NoError(t, err)

	dataDir := t.TempDir()
	shellRCDir, err := workspace.InstallShellRC(dataDir)
	require.NoError(t, err)
	scheme, err := workspace.NewFileSchemeFunc(dataDir, shellRCDir)(ctx, config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() {
		scheme.Close()
		cancel()
	})
	return newTestSequence(t, cfg, timeout, scheme, scheme, commandAndArgs, cases)
}

func TestHandlerViProgramIntegration(t *testing.T) {
	t.Parallel()
	cases := []vtetest.Case{
		{"echo bla>",
			`$ echo bla          
bla                 
$ ▐                 
                    
                    
                    
                    
                    
                    
                    `},
		{"vi>ihello",
			`hello▐              
~                   
~                   
~                   
~                   
~                   
~                   
~                   
~                   
-- INSERT --        `},
		{"<:quit!>",
			`$ echo bla          
bla                 
$ vi                
$ ▐                 
                    
                    
                    
                    
                    
                    `},
	}

	cfg := DefaultConfig()

	// vi needs quite a bit of time to exit
	waitForIdleVte := defaultWaitForIdleVte * 4

	testSequenceShell(t, cfg, waitForIdleVte, "sh", cases)
}

func TestHandlerAnswersQueriesInSynchronizedUpdateWithRawTty(t *testing.T) {
	// crossterm's cursor::position does this inside ratatui's first
	// frame, and gives up after 2s without a reply.
	const script = `stty raw -echo
printf '\033[?2026h\033[6n'
reply=$(dd bs=1 count=6 2>/dev/null)
printf '\033[?2026l'
stty sane
printf 'reply:%s\n' "$(printf '%s' "$reply" | tr '\033' E)"
sleep 60`
	path := t.TempDir() + "/sync_query.sh"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o600))
	handler, ch := testSequenceCommand(t, DefaultConfig(), defaultWaitForIdleVte,
		[]string{"sh", path}, nil)
	drain(ch)

	w := term.NewStringWriter(20, 10)
	require.Eventually(t, func() bool {
		require.NoError(t, w.Clear(term.Attributes{}))
		handler.Draw(w)
		require.NoError(t, w.Flush())
		return strings.Contains(w.String(), "reply:E[1;1R")
	}, 5*time.Second, 20*time.Millisecond, "screen was:\n%s", w.String())
}
