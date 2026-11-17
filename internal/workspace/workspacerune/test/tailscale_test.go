//go:build e2e

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

package runetest

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// allowAll lets every machine of the tailnet reach every other, which
// is the policy a tailnet starts with.
const allowAll = `"acls": [{"action": "accept", "src": ["*"], "dst": ["*:*"]}]`

// workspaceFile writes a file into a workspace directory on m and
// returns the tailscale:// URI of the directory as seen from peer.
func workspaceFile(t *testing.T, m *DockerMachine, peer, content string) string {
	t.Helper()
	path := m.WriteFile(t, "work/hello.txt", content)
	return "tailscale://" + peer + filepath.Dir(path)
}

// awaitOpen opens uri from m until what it came to satisfies want: a
// change of the tailnet's policy takes a moment to reach the machines.
func awaitOpen(
	t *testing.T, m *DockerMachine, uri string, want func(OpenResult) bool,
) OpenResult {
	t.Helper()
	deadline := time.Now().Add(tailnetTimeout)
	for {
		got := m.Open(t, uri, "hello.txt")
		if want(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("opening %s from %s never came to what was expected: %+v",
				uri, m.Hostname, got)
		}
		time.Sleep(2 * time.Second)
	}
}

func opened(content string) func(OpenResult) bool {
	return func(r OpenResult) bool { return r.Code == "" && r.Output == content }
}

func refused(message string) func(OpenResult) bool {
	return func(r OpenResult) bool {
		return r.Code == "PermissionDenied" && strings.Contains(r.Message, message)
	}
}

// terminalTick prints a tick every 200ms, for two minutes.
const terminalTick = "sh -c 'i=0; while [ $i -lt 600 ]; do i=$((i+1)); echo tick $i; sleep 0.2; done'"

// awaitTerminal lists the terminals of uri from m until what it lists
// of id satisfies want, and returns it. A nil want waits for id to be
// gone.
func awaitTerminal(
	t *testing.T, m *DockerMachine, uri, id string, want func(Terminal) bool,
) Terminal {
	t.Helper()
	deadline := time.Now().Add(tailnetTimeout)
	for {
		var found *Terminal
		for _, tm := range m.ListTerminals(t, uri) {
			if tm.ID == id {
				found = &tm
			}
		}
		switch {
		case want == nil && found == nil:
			return Terminal{}
		case want != nil && found != nil && want(*found):
			return *found
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal %s of %s never came to what was expected on %s: %+v",
				id, uri, m.Hostname, found)
		}
		time.Sleep(time.Second)
	}
}

func detachedAnd(liveness string) func(Terminal) bool {
	return func(tm Terminal) bool { return tm.Attachments == 0 && tm.Liveness == liveness }
}

// testTerminals opens terminals in the workspace uri from laptop, whose
// peer owns them: they outlive the tabs that opened them and the
// connection, and the peer reports what they run.
func testTerminals(t *testing.T, laptop *DockerMachine, uri string) {
	var id string
	t.Run("a terminal outlives the tab that opened it", func(t *testing.T) {
		var screen string
		id, screen = laptop.CreateTerminal(t, uri, TerminalOptions{Cmd: terminalTick, Wait: "tick 3"})
		assert.GreaterOrEqual(t, lastTick(screen), 3, "the program's output reached the tab:\n%s", screen)
		awaitTerminal(t, laptop, uri, id, detachedAnd("running"))
	})

	t.Run("it survives its machine leaving the tailnet", func(t *testing.T) {
		if id == "" {
			t.Skip("no terminal")
		}
		laptop.Reconnect(t)
		screen := laptop.AttachTerminal(t, uri, id, TerminalOptions{Wait: "tick 10"})
		assert.GreaterOrEqual(t, lastTick(screen), 10, "the program kept running:\n%s", screen)
		awaitTerminal(t, laptop, uri, id, detachedAnd("running"))
	})

	t.Run("ending it ends the program on the peer", func(t *testing.T) {
		if id == "" {
			t.Skip("no terminal")
		}
		laptop.DeleteTerminal(t, uri, id)
		awaitTerminal(t, laptop, uri, id, nil)
	})

	t.Run("a detached shell reports its job done", func(t *testing.T) {
		id, _ := laptop.CreateTerminal(t, uri, TerminalOptions{
			Line: "sleep 20; echo slept", Wait: "sleep 20",
		})
		awaitTerminal(t, laptop, uri, id, detachedAnd("running"))
		awaitTerminal(t, laptop, uri, id, detachedAnd("idle"))
		screen := laptop.AttachTerminal(t, uri, id, TerminalOptions{})
		assert.Contains(t, screen, "slept", "the output waited in the terminal")
		laptop.DeleteTerminal(t, uri, id)
	})
}

// TestTailscaleUsersMachines runs Rune next to the real Tailscale
// client on machines owned by users, as the desktop app does.
func TestTailscaleUsersMachines(t *testing.T) {
	tailnet := StartDockerTailnet(t, `{
  "tagOwners": {"tag:ci": ["alice@"]},
  `+allowAll+`
}`)
	alice := tailnet.Control.NewAccount(t, "alice")
	bob := tailnet.Control.NewAccount(t, "bob")
	laptop := tailnet.Join(t, "laptop", alice.UserKey(t))
	desktop := tailnet.Join(t, "desktop", alice.UserKey(t))
	bobs := tailnet.Join(t, "bobs-laptop", bob.UserKey(t))
	ci := tailnet.Join(t, "ci", alice.TagKey(t, "tag:ci"))
	desktop.Serve(t)
	bobs.Serve(t)
	uri := workspaceFile(t, desktop, "desktop", "served by desktop\n")

	t.Run("the user's other machine opens a workspace", func(t *testing.T) {
		awaitOpen(t, laptop, uri, opened("served by desktop\n"))
	})

	t.Run("completion offers the machines that would let the user in", func(t *testing.T) {
		// Bob's laptop serves too, but would never let alice in.
		assert.Equal(t, []string{"desktop"}, laptop.Peers(t))
	})

	t.Run("another user's machine is refused", func(t *testing.T) {
		got := bobs.Open(t, uri, "hello.txt")
		assert.True(t, refused("belongs to bob")(got), "%+v", got)
	})

	t.Run("a tagged machine is refused", func(t *testing.T) {
		got := ci.Open(t, uri, "hello.txt")
		assert.True(t, refused("is a tagged machine")(got), "%+v", got)
	})

	t.Run("the machine's own address is refused", func(t *testing.T) {
		self := strings.Replace(uri, "//desktop/", "//"+desktop.Addr.String()+"/", 1)
		got := desktop.Open(t, self, "hello.txt")
		assert.True(t, refused("own tailnet address")(got), "%+v", got)
	})

	t.Run("keeps serving across a reconnect", func(t *testing.T) {
		desktop.Reconnect(t)
		awaitOpen(t, laptop, uri, opened("served by desktop\n"))
	})

	t.Run("a name the tailnet does not know", func(t *testing.T) {
		got := laptop.Open(t, "tailscale://nope/home", "hello.txt")
		require.NotEmpty(t, got.Code, "%+v", got)
		assert.Contains(t, got.Message, "not found")
	})

	t.Run("terminals", func(t *testing.T) { testTerminals(t, laptop, uri) })
}

// TestTailscaleTaggedServer runs Rune next to the real Tailscale client
// on a tagged machine, which only the tailnet's policy lets anyone
// into, and changes the policy under it.
func TestTailscaleTaggedServer(t *testing.T) {
	const tagOwners = `"tagOwners": {"tag:server": ["alice@"]}`
	tailnet := StartDockerTailnet(t, `{
  `+tagOwners+`,
  `+allowAll+`
}`)
	alice := tailnet.Control.NewAccount(t, "alice")
	bob := tailnet.Control.NewAccount(t, "bob")
	server := tailnet.Join(t, "server", alice.TagKey(t, "tag:server"))
	laptop := tailnet.Join(t, "laptop", alice.UserKey(t))
	bobs := tailnet.Join(t, "bobs-laptop", bob.UserKey(t))
	server.Serve(t)
	uri := workspaceFile(t, server, "server", "served by server\n")

	t.Run("without a rule no one gets in", func(t *testing.T) {
		awaitOpen(t, laptop, uri, refused("may not open workspaces here"))
	})

	t.Run("an SSH accept rule lets its users in as the user Rune runs as", func(t *testing.T) {
		tailnet.Control.SetPolicy(t, `{
  `+tagOwners+`,
  `+allowAll+`,
  "ssh": [{"action": "accept", "src": ["alice@"], "dst": ["tag:server"], "users": ["`+runeUser+`"]}]
}`)
		awaitOpen(t, laptop, uri, opened("served by server\n"))
		got := bobs.Open(t, uri, "hello.txt")
		assert.True(t, refused("may not open workspaces here")(got), "%+v", got)
	})

	t.Run("an SSH check rule lets no one in", func(t *testing.T) {
		tailnet.Control.SetPolicy(t, `{
  `+tagOwners+`,
  `+allowAll+`,
  "ssh": [{"action": "check", "src": ["alice@"], "dst": ["tag:server"], "users": ["`+runeUser+`"]}]
}`)
		awaitOpen(t, laptop, uri, refused("is a check rule"))
	})

	t.Run("a grant of the workspace capability lets its machines in", func(t *testing.T) {
		tailnet.Control.SetPolicy(t, `{
  `+tagOwners+`,
  "grants": [
    {"src": ["*"], "dst": ["*"], "ip": ["*"]},
    {"src": ["alice@"], "dst": ["tag:server"], "app": {"rune.build/cap/workspace": [{}]}}
  ],
  "ssh": [{"action": "check", "src": ["alice@"], "dst": ["tag:server"], "users": ["`+runeUser+`"]}]
}`)
		awaitOpen(t, laptop, uri, opened("served by server\n"))
		got := bobs.Open(t, uri, "hello.txt")
		assert.True(t, refused("may not open workspaces here")(got), "%+v", got)
	})
}

// TestTailscaleHeadless runs Rune as `rune --headless --control-url`
// does, on a machine without a Tailscale client: Rune joins the tailnet
// as a machine of its own, signed in by URL.
func TestTailscaleHeadless(t *testing.T) {
	tailnet := StartDockerTailnet(t, `{
  `+allowAll+`
}`)
	alice := tailnet.Control.NewAccount(t, "alice")
	bob := tailnet.Control.NewAccount(t, "bob")
	builder := tailnet.JoinHeadless(t, "builder", alice)
	laptop := tailnet.Join(t, "laptop", alice.UserKey(t))
	bobs := tailnet.Join(t, "bobs-laptop", bob.UserKey(t))
	uri := workspaceFile(t, builder, "builder", "served by builder\n")

	t.Run("the user who signed it in opens a workspace", func(t *testing.T) {
		awaitOpen(t, laptop, uri, opened("served by builder\n"))
	})

	t.Run("completion offers it", func(t *testing.T) {
		assert.Contains(t, laptop.Peers(t), "builder")
	})

	t.Run("another user is refused", func(t *testing.T) {
		got := bobs.Open(t, uri, "hello.txt")
		assert.True(t, refused("belongs to bob")(got), "%+v", got)
	})

	t.Run("terminals", func(t *testing.T) { testTerminals(t, laptop, uri) })
}
