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

package cmdenv

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpandBodySubstitutesDataInRealShells(t *testing.T) {
	// ! steps run in the host's sh, which differs from Rune's
	// interpreter in how it reads some quoting; bash is what sh is on
	// macOS and what [[ ]] and $'…' need elsewhere.
	for _, shell := range []string{"bash", "sh"} {
		path, err := exec.LookPath(shell)
		if err != nil {
			t.Logf("skipping %s: %v", shell, err)
			continue
		}
		// [[ -v ]] arrived in bash 4.2; macOS ships 3.2.
		hasVarSetTest := exec.Command(path, "-c", "[[ -v HOME ]]").Run() == nil
		t.Run(shell, func(t *testing.T) {
			for _, c := range shellContextBodies() {
				if c.name == "variable name test" && !hasVarSetTest {
					continue
				}
				for _, v := range adversarialValues {
					t.Run(c.name+"/"+snippet(v), func(t *testing.T) {
						src := Source(func(name string) (string, bool) {
							return v, name == "X"
						})
						line, err := ExpandBody(context.Background(), c.body, src)
						if c.refused != nil && c.refused(v) {
							require.Error(t, err)
							return
						}
						require.NoError(t, err)

						dir := t.TempDir()
						cmd := exec.Command(path, "-c", line)
						cmd.Dir = dir
						cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}
						var stdout, stderr bytes.Buffer
						cmd.Stdout, cmd.Stderr = &stdout, &stderr
						runErr := cmd.Run()
						_, statErr := os.Stat(filepath.Join(dir, "pwned"))
						require.True(t, os.IsNotExist(statErr),
							"%q ran a command from the value", line)
						if shell == "sh" && c.name == "ANSI-C quotes" {
							// A bash extension a POSIX sh need not support.
							return
						}
						require.NoError(t, runErr, "%q: %s", line, stderr.String())
						assert.Equal(t, c.want(v), stdout.String(), "line %q", line)
					})
				}
			}
		})
	}
}
