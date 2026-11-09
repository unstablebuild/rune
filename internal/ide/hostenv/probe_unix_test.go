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

//go:build !windows

package hostenv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeShell writes an executable shell script with the given body and returns
// its path. The script ignores all the interactive/login flags Rune passes.
func fakeShell(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fakeshell")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755))
	return path
}

// fakeLoginShell writes a fake shell that prints the given pre-marker banner
// lines, then the env marker, then a NUL-delimited PATH entry — mimicking a
// real interactive shell whose rc files emit chatter before `env -0` runs.
func fakeLoginShell(t *testing.T, path string, banner ...string) string {
	t.Helper()
	var body strings.Builder
	for _, l := range banner {
		body.WriteString("printf '%s\\n' " + posixQuote(l) + "\n")
	}
	body.WriteString("printf '%s' " + posixQuote(shellEnvMarker) + "\n")
	if path != "" {
		body.WriteString("printf 'PATH=%s\\0' " + posixQuote(path) + "\n")
	}
	return fakeShell(t, body.String())
}

func probe(t *testing.T, timeout time.Duration, userShell func() (string, error)) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return ProbeLoginPATH(ctx, userShell)
}

func noUserShell() (string, error) { return "", nil }

func TestLoginShellPATHCmdDetachesFromTerminal(t *testing.T) {
	cmd := loginShellPATHCmd(context.Background(), "/bin/sh")

	assert.Nil(t, cmd.Stdin, "login shell stdin must not be inherited from the terminal")
	require.NotNil(t, cmd.SysProcAttr)
	assert.True(t, cmd.SysProcAttr.Setsid, "login shell must run in a new session")
}

func TestProbeLoginPATH(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		banner  []string
		want    string
		wantErr bool
	}{
		{
			name:   "ignores leading banner lines",
			path:   "/login/bin:/usr/bin",
			banner: []string{"welcome-banner", "", "PATH=/should/be/ignored"},
			want:   "/login/bin:/usr/bin",
		},
		{
			name:   "decoys before the marker do not win",
			path:   "/login/bin:/usr/bin",
			banner: []string{"some banner", "PATH=/decoy/should/not/win", "HOME=/decoy"},
			want:   "/login/bin:/usr/bin",
		},
		{
			name: "single entry",
			path: "/usr/bin",
			want: "/usr/bin",
		},
		{
			name:    "no PATH after marker is an error",
			path:    "",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SHELL", fakeLoginShell(t, tc.path, tc.banner...))

			got, err := probe(t, 10*time.Second, noUserShell)
			if tc.wantErr {
				assert.Error(t, err, "got PATH %q", got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestProbeLoginPATHTimesOut(t *testing.T) {
	t.Setenv("SHELL", fakeShell(t, "trap '' TERM\nwhile true; do sleep 1; done\n"))

	start := time.Now()
	_, err := probe(t, 200*time.Millisecond, noUserShell)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "probe blocked too long")
}

func TestProbeLoginPATHFallsBackShell(t *testing.T) {
	t.Setenv("SHELL", "")
	fake := fakeLoginShell(t, "/fallback/bin")

	got, err := probe(t, 10*time.Second, func() (string, error) { return fake, nil })
	require.NoError(t, err)
	assert.Equal(t, "/fallback/bin", got)
}
