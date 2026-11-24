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

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"unstable.build/rune/internal/extension/langext/langexttest"
)

// pinnedBearVersion is the Bear the ccpp package ships, kept in sync
// with the bear submodule of rune-language-ccpp; e2e.yml installs it.
// Another Bear fails the tests rather than validate one the package does
// not ship: Bear 3, which distributions still carry, has no parse-sh.
const pinnedBearVersion = "4.2.4"

// generateFixtures holds one project per build system and outcome; each
// source has an #error guard that only its build's flags satisfy.
const generateFixtures = "testdata/generate"

// findPinnedBear returns the Bear on PATH, skipping when there is none
// and failing when it is not the pinned version.
func findPinnedBear(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("bear")
	if err != nil {
		t.Skip("bear not found; install Bear " + pinnedBearVersion + " to run the generation e2e tests")
	}
	out, err := exec.Command(bin, "--version").CombinedOutput()
	require.NoError(t, err, "bear --version failed: %s", out)
	require.Containsf(t, string(out), "bear "+pinnedBearVersion,
		"%s is not the Bear %s the ccpp package ships; install it, or move the pin and "+
			"rune-language-ccpp's bear submodule together", bin, pinnedBearVersion)
	return bin
}

// requireTools skips the test unless every build tool its fixture needs
// is on PATH. e2e.yml installs them and checks that they resolve.
func requireTools(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s not found; install it to run this generation e2e test", name)
		}
	}
}

// newGenerateEnv brings the extension up on the generation fixture
// named fixture, with the pinned Bear as the ccpp package's, answering
// prompts with prompter.
func newGenerateEnv(t *testing.T, fixture string, prompter *fakePrompter) *e2eEnv {
	t.Helper()
	requireTools(t, "make")
	inst := &langexttest.Installer{Files: []string{findPinnedBear(t)}}
	return newE2EEnvOn(t, filepath.Join(generateFixtures, fixture), nil, prompter, inst)
}

// waitClean blocks until clangd compiles file without diagnostics, which
// its #error guard allows only with its build's flags.
func (e *e2eEnv) waitClean(t *testing.T, file string) {
	t.Helper()
	require.Eventually(t, func() bool {
		diags, ok := e.cb.diagnosticsFor(e.uris[file])
		return ok && len(diags) == 0
	}, e2eWait, e2eTick, "clangd never compiled %s with its build's flags", file)
}

// waitUnflagged blocks until clangd reports that it compiled file
// without its build's flags.
func (e *e2eEnv) waitUnflagged(t *testing.T, file string) {
	t.Helper()
	e.waitDiagnostic(t, file, func(d semanticapi.Diagnostic) bool {
		return strings.Contains(d.Message, "compiled without the")
	})
}

// readEntries returns the compile commands of the database at path.
func readEntries(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var entries []map[string]any
	require.NoError(t, json.Unmarshal(data, &entries))
	return entries
}

// entryArgs returns the compiler arguments of a database entry.
func entryArgs(t *testing.T, entry map[string]any) []string {
	t.Helper()
	raw, ok := entry["arguments"].([]any)
	require.True(t, ok, "entry has no arguments: %v", entry)
	args := make([]string, 0, len(raw))
	for _, a := range raw {
		args = append(args, a.(string))
	}
	return args
}

func TestE2E_CcppCompdbGenerateRecordsAMakeBuild(t *testing.T) {
	env := newGenerateEnv(t, "make", &fakePrompter{})
	env.open(t, "src/main.c")
	env.waitUnflagged(t, "src/main.c")
	out, err := runCcpp(t, env.repl, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "ccpp compdb generate writes one by running make -nBw | bear parse-sh")

	out, err = runCcpp(t, env.repl, "compdb", "generate")
	require.NoError(t, err)
	assert.Contains(t, out, "Wrote compile_commands.json with 1 compile command. clangd restarted.")
	require.Len(t, env.servers(), 2, "the restart starts a new clangd")

	entries := readEntries(t, filepath.Join(env.dir, compileCommandsName))
	require.Len(t, entries, 1)
	assert.Equal(t, filepath.Join(env.dir, "src"), entries[0]["directory"],
		"the recursive make's directory, where -I../include resolves")
	assert.NoFileExists(t, filepath.Join(env.dir, "src", "main.o"), "a dry run builds nothing")
	env.waitClean(t, "src/main.c")

	out, err = runCcpp(t, env.repl, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "compile_commands.json (found by clangd)")
}

func TestE2E_CcppCompdbOfferedOnOpen(t *testing.T) {
	prompter := &fakePrompter{events: key('y')}
	env := newGenerateEnv(t, "make", prompter)
	env.open(t, "src/main.c")

	// Whether the database lands before clangd reads main.c or the
	// restart after it does, main.c ends up with the Makefile's flags.
	env.waitClean(t, "src/main.c")
	require.Len(t, prompter.asked(), 1)
	assert.Contains(t, prompter.asked()[0], "The Make project at the workspace root")
	require.Len(t, readEntries(t, filepath.Join(env.dir, compileCommandsName)), 1)
	require.Eventually(t, func() bool {
		return env.notify.hasMessage("Wrote `compile_commands.json` with 1 compile command")
	}, e2eWait, e2eTick, "got %v", env.notify.notifMessages())

	// Reopening asks nothing more.
	env.editor.open(t, filepath.Join(env.dir, "src", "main.c"))
	_, err := runCcpp(t, env.repl, "status")
	require.NoError(t, err)
	assert.Len(t, prompter.asked(), 1)
}

func TestE2E_CcppCompdbAlwaysGeneratesEveryProjectWithoutAsking(t *testing.T) {
	prompter := &fakePrompter{events: key('a')}
	env := newGenerateEnv(t, "projects", prompter)
	env.open(t, "app/main.c")
	env.waitClean(t, "app/main.c")
	require.Len(t, prompter.asked(), 1)
	assert.Contains(t, prompter.asked()[0], "The Make project at `app`")

	env.open(t, "lib/lib.c")
	env.waitClean(t, "lib/lib.c")
	assert.Len(t, prompter.asked(), 1, "Always answers for every project of the workspace")
	require.Len(t, readEntries(t, filepath.Join(env.dir, "lib", compileCommandsName)), 1)
	require.Eventually(t, func() bool {
		return env.notify.hasMessage("Wrote `lib/compile_commands.json` with 1 compile command")
	}, e2eWait, e2eTick, "got %v", env.notify.notifMessages())

	out, err := runCcpp(t, env.repl, "compdb", "auto")
	require.NoError(t, err)
	assert.Contains(t, out, "gets one without asking")
}

func TestE2E_CcppCompdbRegenerateReadsTheChangedBuild(t *testing.T) {
	env := newGenerateEnv(t, "make", &fakePrompter{})
	env.open(t, "src/main.c")
	_, err := runCcpp(t, env.repl, "compdb", "generate")
	require.NoError(t, err)
	env.waitClean(t, "src/main.c")

	// The build stops passing the define main.c insists on.
	makefile := filepath.Join(env.dir, "src", "Makefile")
	data, err := os.ReadFile(makefile)
	require.NoError(t, err)
	edited := bytes.Replace(data, []byte("-DFROM_MAKE=1 "), nil, 1)
	require.NotEqual(t, data, edited)
	require.NoError(t, os.WriteFile(makefile, edited, 0o644))

	out, err := runCcpp(t, env.repl, "compdb", "generate")
	require.NoError(t, err)
	assert.Contains(t, out, "Wrote compile_commands.json with 1 compile command. clangd restarted.")
	require.Len(t, env.servers(), 3)
	entries := readEntries(t, filepath.Join(env.dir, compileCommandsName))
	require.Len(t, entries, 1)
	assert.NotContains(t, entryArgs(t, entries[0]), "-DFROM_MAKE=1")
	env.waitUnflagged(t, "src/main.c")
}

func TestE2E_CcppCompdbGenerateReportsAFailedBuild(t *testing.T) {
	env := newGenerateEnv(t, "broken", &fakePrompter{})
	env.open(t, "main.c")

	_, err := runCcpp(t, env.repl, "compdb", "generate")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "`make -nBw` failed")
	assert.Contains(t, err.Error(), "No rule to make target", "make's own explanation reaches the user")
	assert.NoFileExists(t, filepath.Join(env.dir, compileCommandsName))
	assert.Len(t, env.servers(), 1, "a failed generation leaves clangd as it was")
}

func TestE2E_CcppCompdbGenerateExplainsAnEmptyDatabase(t *testing.T) {
	env := newGenerateEnv(t, "unrecorded", &fakePrompter{})
	env.open(t, "src/main.c")

	out, err := runCcpp(t, env.repl, "compdb", "generate")
	require.NoError(t, err)
	assert.Contains(t, out, "compile_commands.json lists no compile commands.")
	assert.Contains(t, out, "bear -- make at the project root")
	assert.Empty(t, readEntries(t, filepath.Join(env.dir, compileCommandsName)))
	assert.Len(t, env.servers(), 1, "an empty database is no reason to restart clangd")
}

func TestE2E_CcppCompdbGenerateRecordsAnAutotoolsBuild(t *testing.T) {
	requireTools(t, "autoreconf", "autoconf", "automake", "aclocal")
	env := newGenerateEnv(t, "autotools", &fakePrompter{})
	env.open(t, "src/main.c")
	env.waitUnflagged(t, "src/main.c")

	out, err := runCcpp(t, env.repl, "compdb", "generate")
	require.NoError(t, err)
	assert.Contains(t, out, "Wrote compile_commands.json with 1 compile command. clangd restarted.")
	entries := readEntries(t, filepath.Join(env.dir, compileCommandsName))
	require.Len(t, entries, 1)
	assert.Equal(t, filepath.Join(env.dir, "src"), entries[0]["directory"])
	// A per-target flag puts a command substitution in Automake's compile
	// line, which a dry run cannot hand to Bear.
	assert.Contains(t, entryArgs(t, entries[0]), "-DFROM_AUTOTOOLS=1")
	env.waitClean(t, "src/main.c")

	// Generating again on the built tree compiles nothing: the database
	// keeps its entry, and make's header dependencies are left alone.
	deps := filepath.Join(env.dir, "src", ".deps", "app-main.Po")
	before, err := os.ReadFile(deps)
	require.NoError(t, err)
	require.Contains(t, string(before), "util.h")
	out, err = runCcpp(t, env.repl, "compdb", "generate")
	require.NoError(t, err)
	assert.Contains(t, out, "Wrote compile_commands.json with 1 compile command.")
	after, err := os.ReadFile(deps)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
}

func TestE2E_CcppCompdbGenerateSetsUpMeson(t *testing.T) {
	requireTools(t, "meson", "ninja")
	env := newE2EEnvOn(t, filepath.Join(generateFixtures, "meson"), nil, &fakePrompter{}, &langexttest.Installer{})
	env.open(t, "src/main.c")
	env.waitUnflagged(t, "src/main.c")

	out, err := runCcpp(t, env.repl, "compdb", "generate")
	require.NoError(t, err)
	assert.Contains(t, out, "Wrote build/compile_commands.json with 1 compile command. clangd restarted.")
	env.waitClean(t, "src/main.c")

	// build/ is set up now, which meson refuses to set up again.
	out, err = runCcpp(t, env.repl, "compdb", "generate")
	require.NoError(t, err)
	assert.Contains(t, out, "Wrote build/compile_commands.json with 1 compile command. clangd restarted.")
	runs := env.scheme.startedProcesses("meson")
	require.Len(t, runs, 2)
	assert.Equal(t, []string{"setup", "build"}, runs[0].args)
	assert.Equal(t, []string{"setup", "--reconfigure", "build"}, runs[1].args)
}

func TestE2E_CcppCompdbGenerateConfiguresCMake(t *testing.T) {
	requireTools(t, "cmake")
	env := newE2EEnvOn(t, "testdata/e2e", nil, &fakePrompter{}, &langexttest.Installer{})
	// The fixture ships its databases; generation must write its own.
	require.NoError(t, os.RemoveAll(filepath.Join(env.dir, "build")))
	require.NoError(t, os.RemoveAll(filepath.Join(env.dir, "out")))
	env.open(t, "src/main.cpp")

	out, err := runCcpp(t, env.repl, "compdb", "generate")
	require.NoError(t, err)
	assert.Contains(t, out, "Wrote build/compile_commands.json with 4 compile commands. clangd restarted.")
	files := map[string]bool{}
	for _, e := range readEntries(t, filepath.Join(env.dir, "build", compileCommandsName)) {
		files[filepath.Base(e["file"].(string))] = true
	}
	assert.Equal(t, map[string]bool{"main.cpp": true, "shape.cpp": true, "util.c": true, "flags.c": true}, files)
	env.waitReady(t)
}
