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

package cmdenv

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerDirSeedsInterpCwd(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	r := Runner{
		Executor: &osExecutor{},
		Dir:      dir,
		Stdout:   &out,
	}
	_, err := r.Run(context.Background(), "pwd", nil)
	require.NoError(t, err)
	assert.Equal(t, dir+"\n", out.String())
}

func TestRunnerCdRelativeIsAnchoredAtDir(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	r := Runner{
		Executor: &osExecutor{},
		Dir:      dir,
		Stdout:   &out,
	}
	_, err := r.Run(context.Background(),
		`ROOT=$(cd . && pwd); echo $ROOT`, nil)
	require.NoError(t, err)
	assert.Equal(t, dir+"\n", out.String())
}

func TestRunnerCapturesParameterExpansionResult(t *testing.T) {
	r := Runner{Executor: &osExecutor{}}
	captured, err := r.Run(context.Background(),
		`ROOT=/Users/ernestrc/src/idelsp; ROOT_NAME=${ROOT##*/}`, nil)
	require.NoError(t, err)
	assert.Equal(t, "idelsp", captured["ROOT_NAME"])
}

func TestRunnerCapturesParameterExpansionFromEnvSource(t *testing.T) {
	src := Source(func(name string) (string, bool) {
		if name == "ROOT" {
			return "/Users/ernestrc/src/idelsp", true
		}
		return "", false
	})
	r := Runner{Executor: &osExecutor{}, EnvSource: src}
	captured, err := r.Run(context.Background(),
		`ROOT_NAME=${ROOT##*/}`, nil)
	require.NoError(t, err)
	assert.Equal(t, "idelsp", captured["ROOT_NAME"])
}

func TestRunnerCapturesParameterExpansionAfterExpandBody(t *testing.T) {
	src := Source(func(name string) (string, bool) {
		if name == "ROOT" {
			return "/Users/ernestrc/src/idelsp", true
		}
		return "", false
	})
	line, err := ExpandBody(context.Background(),
		`ROOT_NAME=${ROOT##*/}`, src)
	require.NoError(t, err)
	r := Runner{Executor: &osExecutor{}, EnvSource: src}
	captured, err := r.Run(context.Background(), line, nil)
	require.NoError(t, err)
	assert.Equal(t, "idelsp", captured["ROOT_NAME"])
}

func TestRunnerCapturesDoubleAssignmentWithShortCircuit(t *testing.T) {
	dir := t.TempDir()
	r := Runner{Executor: &osExecutor{}, Dir: dir}
	line := `ROOT=$(echo ".git") && ROOT=$(cd "$ROOT/.." && pwd) || exit 1`
	captured, err := r.Run(context.Background(), line, nil)
	require.NoError(t, err)
	assert.Equal(t, dir, captured["ROOT"],
		"chain capture must reflect the second assignment to ROOT")
}

func TestRunnerOnlyCapturesScriptAssignedVars(t *testing.T) {
	src := Source(func(name string) (string, bool) {
		if name == "ROOT" {
			return "/Users/ernestrc/src/idelsp", true
		}
		return "", false
	})
	r := Runner{Executor: &osExecutor{}, EnvSource: src}
	captured, err := r.Run(context.Background(),
		`ROOT_HASH=hashof_$ROOT`, nil)
	require.NoError(t, err)
	assert.Equal(t, "hashof_/Users/ernestrc/src/idelsp", captured["ROOT_HASH"])
	_, hasROOT := captured["ROOT"]
	assert.False(t, hasROOT,
		"parent-env ROOT must not leak into captured vars")
}
