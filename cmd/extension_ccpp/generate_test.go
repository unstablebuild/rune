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

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testBear = "/data/ccpp/bin/bear"
	// twoEntries is a compilation database with two compile commands.
	twoEntries = `[{"directory":"/ws","file":"a.c","arguments":["cc","-c","a.c"]},` +
		`{"directory":"/ws","file":"b.c","arguments":["cc","-c","b.c"]}]`
)

func TestPlanCompdb(t *testing.T) {
	tests := []struct {
		name   string
		fs     *fakeFS
		want   string
		output string
	}{
		{"cmake", newFakeFS().addFile("/ws/CMakeLists.txt"),
			"cmake -S . -B build -DCMAKE_EXPORT_COMPILE_COMMANDS=ON", "build/compile_commands.json"},
		{"an in-source cmake build is still cmake", newFakeFS().addFile("/ws/CMakeLists.txt").addFile("/ws/Makefile"),
			"cmake -S . -B build -DCMAKE_EXPORT_COMPILE_COMMANDS=ON", "build/compile_commands.json"},
		{"meson", newFakeFS().addFile("/ws/meson.build"),
			"meson setup build", "build/compile_commands.json"},
		{"a configured meson build directory is reconfigured",
			newFakeFS().addFile("/ws/meson.build").addFile("/ws/build/meson-private/coredata.dat"),
			"meson setup --reconfigure build", "build/compile_commands.json"},
		{"autotools from a checkout", newFakeFS().addFile("/ws/configure.ac").addFile("/ws/Makefile.am"),
			"autoreconf -i && ./configure && bear --append -- make", "compile_commands.json"},
		{"autotools from a tarball", newFakeFS().addFile("/ws/configure").addFile("/ws/Makefile.in"),
			"./configure && bear --append -- make", "compile_commands.json"},
		{"autotools already configured",
			newFakeFS().addFile("/ws/configure.ac").addFile("/ws/configure").addFile("/ws/Makefile"),
			"bear --append -- make", "compile_commands.json"},
		{"make", newFakeFS().addFile("/ws/Makefile"), "make -nBw | bear parse-sh", "compile_commands.json"},
		{"GNU make", newFakeFS().addFile("/ws/GNUmakefile"), "make -nBw | bear parse-sh", "compile_commands.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, ok := planCompdb(tt.fs, "/ws", projectMarkers(tt.fs, "/ws"))
			require.True(t, ok)
			assert.Equal(t, tt.want, plan.String())
			assert.Equal(t, tt.output, plan.output)
			assert.NotEmpty(t, plan.emptyHint)
			for _, s := range plan.steps {
				assert.NotEmpty(t, s.purpose, "Rune shows why %s runs when it asks to run it", s)
				// ideauthorizer cuts a longer description short.
				assert.LessOrEqual(t, utf8.RuneCountInString(s.purpose), 200, s.String())
			}
		})
	}
	for name, fs := range map[string]*fakeFS{
		"bazel":                  newFakeFS().addFile("/ws/MODULE.bazel"),
		"no build system":        newFakeFS(),
		"only clangd's own file": newFakeFS().addFile("/ws/compile_flags.txt"),
	} {
		t.Run(name+" cannot be generated", func(t *testing.T) {
			_, ok := planCompdb(fs, "/ws", projectMarkers(fs, "/ws"))
			assert.False(t, ok)
		})
	}
}

// newTestGenerator is a generator over fakes, with make and cmake on the
// host's PATH and the packaged Bear; restarts counts the clangd
// restarts.
func newTestGenerator(fs *fakeFS) (*compdbGenerator, *fakeExecutor, *fakeNotifications, *int) {
	exec := newFakeExecutor().onPath("make", "/usr/bin/make").onPath("cmake", "/usr/bin/cmake")
	notify := newFakeNotifications()
	restarts := new(int)
	return &compdbGenerator{
		fs: fs, exec: exec, notify: notify, state: &serverState{},
		restart: func(context.Context) error { *restarts++; return nil },
		bear:    func(context.Context) string { return testBear },
	}, exec, notify, restarts
}

// makePlan is the plan of the Make project at /ws.
func makePlan(t *testing.T, fs *fakeFS) compdbPlan {
	t.Helper()
	plan, ok := planCompdb(fs, "/ws", []marker{{"Makefile", systemMake}})
	require.True(t, ok)
	return plan
}

func TestCompdbGeneratorPipesTheDryRunIntoBear(t *testing.T) {
	fs := newFakeFS().addFile("/ws/Makefile")
	gen, exec, notify, restarts := newTestGenerator(fs)
	exec.respond("/usr/bin/make -nBw", scriptedCmd{stdout: "make: Entering directory '/ws'\ncc -c a.c\ncc -c b.c\n"})
	exec.respond(testBear+" parse-sh", scriptedCmd{effect: func() {
		fs.addContent("/ws/compile_commands.json", []byte(twoEntries))
	}})

	res, err := gen.generate(context.Background(), "/ws", makePlan(t, fs), ".")
	require.NoError(t, err)
	assert.Equal(t, compdbResult{output: "/ws/compile_commands.json", entries: 2, emptyHint: emptyRecordHint}, res)
	assert.Zero(t, *restarts, "a clangd that never started reads the database when it starts")
	assert.Equal(t, "Wrote `compile_commands.json` with 2 compile commands; clangd reads it when it starts.",
		res.summary(func(p string) string { return wsRel("/ws", p) }))

	started := exec.startedSnapshot()
	require.Len(t, started, 2)
	assert.Equal(t, startedCmd{key: "/usr/bin/make -nBw", dir: "/ws", description: purposeDryRun}, started[0])
	assert.Equal(t, startedCmd{
		key: testBear + " parse-sh", dir: "/ws", description: purposeBear,
		stdin: "make: Entering directory '/ws'\ncc -c a.c\ncc -c b.c\n",
	}, started[1])
	assert.Equal(t, []string{"Generating compile_commands.json for ."}, notify.notifMessages())
	assert.Equal(t, []string{
		"0/3 Running make", "1/3 Running bear", "3/3 compile_commands.json ready",
	}, notify.progressMessages())
}

func TestCompdbGeneratorRestartsARunningClangd(t *testing.T) {
	fs := newFakeFS().addFile("/ws/CMakeLists.txt")
	gen, exec, notify, restarts := newTestGenerator(fs)
	gen.state.running(clangdOptions{bin: testClangd})
	exec.respond("/usr/bin/cmake -S . -B build -DCMAKE_EXPORT_COMPILE_COMMANDS=ON", scriptedCmd{effect: func() {
		fs.addContent("/ws/build/compile_commands.json", []byte(twoEntries))
	}})
	plan, ok := planCompdb(fs, "/ws", projectMarkers(fs, "/ws"))
	require.True(t, ok)

	res, err := gen.generate(context.Background(), "/ws", plan, ".")
	require.NoError(t, err)
	assert.True(t, res.restarted)
	assert.Equal(t, 1, *restarts)
	assert.Equal(t, "Wrote `build/compile_commands.json` with 2 compile commands. clangd restarted.",
		res.summary(func(p string) string { return wsRel("/ws", p) }))
	assert.Equal(t, []string{
		"0/2 Running cmake", "1/2 Restarting clangd", "2/2 compile_commands.json ready",
	}, notify.progressMessages())
}

func TestCompdbGeneratorRunsConfigureFromTheProject(t *testing.T) {
	fs := newFakeFS().addFile("/ws/configure").addFile("/ws/Makefile.in")
	gen, exec, _, _ := newTestGenerator(fs)
	exec.respond(testBear+" --append -- make", scriptedCmd{effect: func() {
		fs.addContent("/ws/compile_commands.json", []byte(twoEntries))
	}})
	plan, ok := planCompdb(fs, "/ws", projectMarkers(fs, "/ws"))
	require.True(t, ok)

	res, err := gen.generate(context.Background(), "/ws", plan, ".")
	require.NoError(t, err)
	assert.Equal(t, 2, res.entries)
	assert.Equal(t, []string{"/ws/configure", testBear + " --append -- make"}, exec.callsSnapshot(),
		"an Autotools build is recorded, not dry-run")
	assert.Equal(t, emptyBuildHint, res.emptyHint)
}

func TestCompdbGeneratorResolvesEveryToolBeforeRunningAny(t *testing.T) {
	t.Run("a tool missing from PATH", func(t *testing.T) {
		fs := newFakeFS().addFile("/ws/meson.build")
		gen, exec, notify, _ := newTestGenerator(fs)
		plan, ok := planCompdb(fs, "/ws", projectMarkers(fs, "/ws"))
		require.True(t, ok)
		_, err := gen.generate(context.Background(), "/ws", plan, ".")
		require.EqualError(t, err, "`meson` is not on the workspace host's PATH")
		assert.Empty(t, exec.callsSnapshot())
		assert.Empty(t, notify.notifMessages())
	})
	t.Run("no bear", func(t *testing.T) {
		fs := newFakeFS().addFile("/ws/Makefile")
		gen, exec, _, _ := newTestGenerator(fs)
		gen.bear = func(context.Context) string { return "" }
		_, err := gen.generate(context.Background(), "/ws", makePlan(t, fs), ".")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pkg install ccpp")
		assert.Empty(t, exec.callsSnapshot(), "make must not run when bear cannot")
	})
	t.Run("no make for bear to record", func(t *testing.T) {
		fs := newFakeFS().addFile("/ws/configure").addFile("/ws/Makefile.in")
		gen, _, _, _ := newTestGenerator(fs)
		gen.exec = newFakeExecutor()
		exec := gen.exec.(*fakeExecutor)
		plan, ok := planCompdb(fs, "/ws", projectMarkers(fs, "/ws"))
		require.True(t, ok)
		_, err := gen.generate(context.Background(), "/ws", plan, ".")
		require.EqualError(t, err, "`make` is not on the workspace host's PATH")
		assert.Empty(t, exec.callsSnapshot(), "configure must not run when the build cannot")
	})
	t.Run("a host that cannot look tools up starts them by name", func(t *testing.T) {
		bin, err := hostTool(context.Background(), noLookPath{newFakeExecutor()}, "make")
		require.NoError(t, err)
		assert.Equal(t, "make", bin)
	})
}

// noLookPath is an executor on a workspace host that cannot tell where
// a program is.
type noLookPath struct{ *fakeExecutor }

func (noLookPath) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}

func TestCompdbGeneratorReportsAFailedStep(t *testing.T) {
	fs := newFakeFS().addFile("/ws/Makefile")
	gen, exec, notify, restarts := newTestGenerator(fs)
	gen.state.running(clangdOptions{bin: testClangd})
	var stderr strings.Builder
	for i := range 30 {
		stderr.WriteString("noise " + string(rune('a'+i%26)) + "\n")
	}
	stderr.WriteString("make: *** No rule to make target 'gen.h'.  Stop.\n")
	exec.respond("/usr/bin/make -nBw", scriptedCmd{stderr: stderr.String(), err: assertErr})

	_, err := gen.generate(context.Background(), "/ws", makePlan(t, fs), ".")
	require.Error(t, err)
	assert.True(t, errors.Is(err, assertErr))
	assert.Contains(t, err.Error(), "`make -nBw` failed")
	assert.Contains(t, err.Error(), "No rule to make target 'gen.h'")
	assert.Equal(t, transcriptLines+1, strings.Count(err.Error(), "\n")+1, "the message and the last lines")
	assert.Equal(t, []string{"/usr/bin/make -nBw"}, exec.callsSnapshot())
	assert.Zero(t, *restarts)
	progress := notify.progressMessages()
	assert.Equal(t, "3/3 Generating compile_commands.json failed", progress[len(progress)-1])
}

func TestCompdbGeneratorOutcomes(t *testing.T) {
	t.Run("an empty database is explained and keeps clangd as is", func(t *testing.T) {
		fs := newFakeFS().addFile("/ws/Makefile")
		gen, exec, _, restarts := newTestGenerator(fs)
		gen.state.running(clangdOptions{bin: testClangd})
		exec.respond(testBear+" parse-sh", scriptedCmd{effect: func() {
			fs.addContent("/ws/compile_commands.json", []byte("[]"))
		}})
		res, err := gen.generate(context.Background(), "/ws", makePlan(t, fs), ".")
		require.NoError(t, err)
		assert.Zero(t, res.entries)
		assert.Zero(t, *restarts)
		assert.Equal(t, "`compile_commands.json` lists no compile commands. "+emptyRecordHint,
			res.summary(func(p string) string { return wsRel("/ws", p) }))
	})
	t.Run("no database written", func(t *testing.T) {
		fs := newFakeFS().addFile("/ws/Makefile")
		gen, _, _, _ := newTestGenerator(fs)
		_, err := gen.generate(context.Background(), "/ws", makePlan(t, fs), ".")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "`make -nBw | bear parse-sh` finished without a readable compile_commands.json")
	})
	t.Run("a failed restart surfaces", func(t *testing.T) {
		fs := newFakeFS().addFile("/ws/Makefile")
		gen, exec, _, _ := newTestGenerator(fs)
		gen.state.running(clangdOptions{bin: testClangd})
		gen.restart = func(context.Context) error { return errors.New("boom") }
		exec.respond(testBear+" parse-sh", scriptedCmd{effect: func() {
			fs.addContent("/ws/compile_commands.json", []byte(twoEntries))
		}})
		_, err := gen.generate(context.Background(), "/ws", makePlan(t, fs), ".")
		require.EqualError(t, err, "wrote compile_commands.json, but restarting clangd failed: boom")
	})
}

func TestTail(t *testing.T) {
	assert.Equal(t, "", tail("", 3))
	assert.Equal(t, "", tail("  \n", 3))
	assert.Equal(t, "\na\nb", tail("a\nb\n", 3))
	assert.Equal(t, "\nc\nd", tail("a\nb\nc\nd", 2))
}
