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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
)

type testOffer struct {
	*compdbOffer
	fs       *fakeFS
	exec     *fakeExecutor
	notify   *fakeNotifications
	prompter *fakePrompter
}

// newTestOffer is an offer over fakes for the workspace /ws whose user
// answers every prompt with keys. Bear writes two entries.
func newTestOffer(t *testing.T, fs *fakeFS, cfg config.Config, keys []term.Event) *testOffer {
	t.Helper()
	gen, exec, notify, _ := newTestGenerator(fs)
	exec.respond(testBear+" parse-sh", scriptedCmd{effect: func() {
		fs.addContent("/ws/compile_commands.json", []byte(twoEntries))
	}})
	uri, err := workspaceapi.ParseURI("file:///ws")
	require.NoError(t, err)
	o := &testOffer{fs: fs, exec: exec, notify: notify, prompter: &fakePrompter{events: keys}}
	o.compdbOffer = &compdbOffer{
		baseCtx: context.Background(), fs: fs, cfg: cfg, notify: notify, prompter: o.prompter,
		compdb: newCompdbSetting(storagestub.NewInMemoryService(), uri), gen: gen,
		wsDir: "/ws", considered: map[string]bool{},
	}
	return o
}

func TestCompdbOfferYesGeneratesOnce(t *testing.T) {
	o := newTestOffer(t, newFakeFS().addFile("/ws/Makefile"), nil, key('y'))
	o.consider(context.Background(), "/ws/src/main.c")

	asked := o.prompter.asked()
	require.Len(t, asked, 1)
	assert.Contains(t, asked[0], "The Make project at the workspace root has no compile_commands.json")
	assert.Contains(t, asked[0], "Rune runs `make -nBw | bear parse-sh` in the project")
	assert.Contains(t, asked[0], "`ccpp compdb auto off`")
	assert.Equal(t, []string{"/usr/bin/make -nBw", testBear + " parse-sh"}, o.exec.callsSnapshot())
	assert.True(t, o.notify.hasMessage("Wrote `compile_commands.json` with 2 compile commands"),
		"got %v", o.notify.notifMessages())
	auto, err := o.compdb.autoGenerate(context.Background())
	require.NoError(t, err)
	assert.False(t, auto, "Yes is for this project only")

	// The database now exists; even without it, a project is offered
	// once a session.
	o.consider(context.Background(), "/ws/src/util.c")
	assert.Len(t, o.prompter.asked(), 1)
}

func TestCompdbOfferAlwaysStopsAsking(t *testing.T) {
	fs := newFakeFS().addFile("/ws/app/Makefile").addFile("/ws/lib/CMakeLists.txt")
	o := newTestOffer(t, fs, nil, key('a'))
	o.exec.respond(testBear+" parse-sh", scriptedCmd{effect: func() {
		fs.addContent("/ws/app/compile_commands.json", []byte(twoEntries))
	}})
	o.exec.respond("/usr/bin/cmake -S . -B build -DCMAKE_EXPORT_COMPILE_COMMANDS=ON", scriptedCmd{effect: func() {
		fs.addContent("/ws/lib/build/compile_commands.json", []byte(twoEntries))
	}})
	o.consider(context.Background(), "/ws/app/main.c")
	require.Len(t, o.prompter.asked(), 1)
	assert.Contains(t, o.prompter.asked()[0], "The Make project at `app`")
	auto, err := o.compdb.autoGenerate(context.Background())
	require.NoError(t, err)
	assert.True(t, auto)

	// Another project of the workspace is generated without a question.
	o.consider(context.Background(), "/ws/lib/src/a.cpp")
	assert.Len(t, o.prompter.asked(), 1)
	assert.Contains(t, o.exec.callsSnapshot(), "/usr/bin/cmake -S . -B build -DCMAKE_EXPORT_COMPILE_COMMANDS=ON")
	started := o.exec.startedSnapshot()
	assert.Equal(t, "/ws/lib", started[len(started)-1].dir)
	assert.True(t, o.notify.hasMessage("Wrote `lib/build/compile_commands.json` with 2 compile commands"),
		"got %v", o.notify.notifMessages())
}

func TestCompdbOfferNoOrDismissLeavesTheProjectAlone(t *testing.T) {
	for name, keys := range map[string][]term.Event{
		"no":      key('n'),
		"dismiss": {{Type: term.EventKey, Key: term.KeyEsc}},
	} {
		t.Run(name, func(t *testing.T) {
			o := newTestOffer(t, newFakeFS().addFile("/ws/Makefile"), nil, keys)
			o.consider(context.Background(), "/ws/main.c")
			assert.Len(t, o.prompter.asked(), 1)
			assert.Empty(t, o.exec.callsSnapshot())
			assert.Empty(t, o.notify.notifMessages())
			auto, err := o.compdb.autoGenerate(context.Background())
			require.NoError(t, err)
			assert.False(t, auto)

			o.consider(context.Background(), "/ws/other.c")
			assert.Len(t, o.prompter.asked(), 1, "the project is not asked about again this session")
		})
	}
}

func TestCompdbOfferReportsAFailedGeneration(t *testing.T) {
	o := newTestOffer(t, newFakeFS().addFile("/ws/Makefile"), nil, key('y'))
	o.exec.respond("/usr/bin/make -nBw", scriptedCmd{stderr: "make: *** missing separator.  Stop.\n", err: assertErr})
	o.consider(context.Background(), "/ws/main.c")
	msgs := o.notify.notifMessages()
	require.NotEmpty(t, msgs)
	last := msgs[len(msgs)-1]
	assert.Contains(t, last, "Generating compile_commands.json for . failed")
	assert.Contains(t, last, "missing separator")
	assert.Contains(t, last, "Run `ccpp compdb generate` to try again.")
}

func TestCompdbOfferNothingToOffer(t *testing.T) {
	tests := []struct {
		name string
		fs   *fakeFS
		cfg  config.Config
		file string
	}{
		{"a database clangd finds", newFakeFS().addFile("/ws/Makefile").addFile("/ws/build/compile_commands.json"),
			nil, "/ws/src/a.c"},
		{"a flags file", newFakeFS().addFile("/ws/Makefile").addFile("/ws/compile_flags.txt"), nil, "/ws/a.c"},
		{"a database clangd misses is for ccpp compdb use",
			newFakeFS().addFile("/ws/CMakeLists.txt").addFile("/ws/out/Debug/compile_commands.json").
				addReadDir("/ws", fakeDirEntry{name: "out", dir: true}).
				addReadDir("/ws/out", fakeDirEntry{name: "Debug", dir: true}),
			nil, "/ws/a.c"},
		{"a configured database", newFakeFS().addFile("/ws/Makefile"),
			newStubConfig(map[string]string{"compile_commands_dir": "out"}), "/ws/a.c"},
		{"bazel", newFakeFS().addFile("/ws/MODULE.bazel"), nil, "/ws/a.cc"},
		{"no build system", newFakeFS(), nil, "/ws/a.c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := newTestOffer(t, tt.fs, tt.cfg, key('y'))
			o.consider(context.Background(), tt.file)
			assert.Empty(t, o.prompter.asked())
			assert.Empty(t, o.exec.callsSnapshot())
		})
	}
	t.Run("a pinned database", func(t *testing.T) {
		o := newTestOffer(t, newFakeFS().addFile("/ws/Makefile"), nil, key('y'))
		require.NoError(t, o.compdb.set(context.Background(), "/ws/out"))
		o.consider(context.Background(), "/ws/a.c")
		assert.Empty(t, o.prompter.asked())
	})
}

func TestCompdbOfferHandlesWorkspaceCcppOpens(t *testing.T) {
	o := newTestOffer(t, newFakeFS().addFile("/ws/Makefile"), nil, key('n'))
	event := func(typ textapi.EventType, path string) textapi.Event {
		uri, err := workspaceapi.ParseURI("file://" + path)
		require.NoError(t, err)
		return textapi.Event{Type: typ, URI: uri}
	}
	for _, ev := range []textapi.Event{
		event(textapi.EventTypeOpen, "/ws/README.md"),
		event(textapi.EventTypeOpen, "/elsewhere/a.c"),
		event(textapi.EventTypeFocus, "/ws/a.c"),
	} {
		assert.False(t, o.Handle(context.Background(), ev))
	}
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, o.prompter.asked())

	assert.False(t, o.Handle(context.Background(), event(textapi.EventTypeOpen, "/ws/a.c")))
	require.Eventually(t, func() bool { return len(o.prompter.asked()) == 1 }, 5*time.Second, 10*time.Millisecond)
}
