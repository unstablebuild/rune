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

// Package networkconsole exposes the `network` REPL command, which
// manages this machine's membership in the user's private mesh and
// lists the machines that rune:// workspaces can be opened on.
package networkconsole

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/runenet"
)

// CommandName is the top-level REPL command exposed by this shell.
const CommandName = "network"

var commandManual = textapi.CommandManual{
	Name: CommandName,
	Summary: "Manage this machine's membership in your private network " +
		"of Rune instances. Machines on the network can be opened as " +
		"workspaces with `workspaceopen rune://<machine>/`.",
	Synopsis: "<command>",
	Commands: []textapi.CommandManual{
		{
			Name: "status",
			Summary: "Show this machine's network name, addresses, and " +
				"sign-in state, including the last error if the machine " +
				"could not join. While a sign-in is pending it prints the " +
				"URL to open in a browser to authorize this machine.",
		},
		{
			Name: "peers",
			Summary: "List the other machines connected to the network, with the " +
				"rune:// address each one is reachable at.",
		},
		{
			Name: "machines",
			Summary: "List every machine registered to your account, " +
				"whether or not it is on the network right now. The free " +
				"plan covers 2 of them.",
		},
		{
			Name: "remove",
			Summary: "Unregister a machine from your account, freeing the " +
				"slot it held. The machine rejoins by running `network up` " +
				"on it again.",
		},
		{
			Name: "up",
			Summary: "Join the network and wait until this machine is a " +
				"member. Runs automatically at startup when " +
				"`network.auto_join` is set.",
		},
		{
			Name: "down",
			Summary: "Leave the network. Open rune:// workspaces stop " +
				"working until you run `network up` again.",
		},
	},
}

// Manual returns the REPL command manual.
func Manual() textapi.CommandManual { return commandManual }

// Network is the mesh membership this command operates on. It is
// satisfied by [runenet.Node].
type Network interface {
	Status(ctx context.Context) (runenet.Status, error)
	Peers(ctx context.Context) ([]runenet.Peer, error)
	Up(ctx context.Context) error
	Down(ctx context.Context) error
	// Machines are the machines registered to the account, which is
	// what the plan's machine allowance is counted in. Unlike Peers
	// it is answered by the account server rather than the mesh, so
	// it works on a machine the allowance is keeping off the mesh.
	Machines(ctx context.Context) ([]Machine, error)
	// MachineNames are the names Remove accepts. It is the completion
	// path, so unlike Machines it must stay silent: a completion runs
	// on a keystroke, and a keystroke must not open a modal.
	MachineNames(ctx context.Context) ([]string, error)
	// Remove unregisters the machine named hostname.
	Remove(ctx context.Context, hostname string) error
}

// Machine is one of the account's machines on the network.
type Machine struct {
	// Hostname is the name used in rune://<hostname> URIs.
	Hostname string
	// LastSeen is when the machine last reached the network. Zero
	// for a machine that never did.
	LastSeen time.Time
	// Online is whether the machine is on the network right now.
	Online bool
	// ServeOnly is whether the machine only serves the account's other
	// machines and cannot open workspaces itself.
	ServeOnly bool
}

// Config configures a Handler.
type Config struct {
	// Network is the local machine's membership in the mesh.
	Network Network
}

// Handler implements the `network` command.
type Handler struct {
	network Network
}

var _ textapi.REPLHandler = (*Handler)(nil)

// New returns a Handler configured with cfg. It panics if Network is
// nil: the command is only registered once the node has been
// constructed, so a missing one is a programming error.
func New(cfg Config) *Handler {
	if cfg.Network == nil {
		panic("networkconsole: Config.Network must not be nil")
	}
	return &Handler{network: cfg.Network}
}

// HandleCommand satisfies repl.CommandHandler. It runs off the IDE
// event loop, which is what lets it block on the mesh control plane.
func (h *Handler) HandleCommand(
	ctx context.Context, cmd repl.Command, _ repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if len(cmd.Args) == 0 {
		return markdownOutput(usageMarkdown()), nil
	}
	switch sub := cmd.Args[0]; sub {
	case "status":
		return h.status(ctx)
	case "peers":
		return h.peers(ctx)
	case "machines":
		return h.machines(ctx)
	case "remove":
		return h.remove(ctx, cmd.Args[1:])
	case "up":
		return h.up(ctx)
	case "down":
		return h.down(ctx)
	case "help":
		return markdownOutput(usageMarkdown()), nil
	default:
		return nil, fmt.Errorf("unknown command: %s", sub)
	}
}

func (h *Handler) status(ctx context.Context) (
	iterator.Iterator[component.Responsive], error,
) {
	st, err := h.network.Status(ctx)
	if err != nil {
		return nil, err
	}
	return markdownOutput(statusMarkdown(st)), nil
}

func statusMarkdown(st runenet.Status) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Network\n\n")
	fmt.Fprintf(&b, "- **machine**: `%s`\n", st.Hostname)
	fmt.Fprintf(&b, "- **state**: %s\n", st.State)
	if st.LoginName != "" {
		fmt.Fprintf(&b, "- **account**: %s\n", st.LoginName)
	}
	controlURL := st.ControlURL
	if controlURL == "" {
		controlURL = "default"
	}
	fmt.Fprintf(&b, "- **coordination server**: %s\n", controlURL)
	if len(st.Addrs) > 0 {
		addrs := make([]string, 0, len(st.Addrs))
		for _, a := range st.Addrs {
			addrs = append(addrs, "`"+a.String()+"`")
		}
		fmt.Fprintf(&b, "- **addresses**: %s\n", strings.Join(addrs, ", "))
	}
	if st.LastError != "" {
		fmt.Fprintf(&b, "- **error**: %s\n", st.LastError)
	}
	if st.AuthURL != "" {
		fmt.Fprintf(&b,
			"\nOpen %s to authorize this machine.\n", st.AuthURL)
	}
	return b.String()
}

func (h *Handler) peers(ctx context.Context) (
	iterator.Iterator[component.Responsive], error,
) {
	peers, err := h.network.Peers(ctx)
	if err != nil {
		return nil, err
	}
	if len(peers) == 0 {
		return markdownOutput(
			"No other machines connected to the network yet. Run " +
				"`network status` on another machine to add it."), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Machines\n\n")
	fmt.Fprintf(&b, "| machine | workspace | os | state |\n")
	fmt.Fprintf(&b, "| --- | --- | --- | --- |\n")
	for _, p := range peers {
		state := "offline"
		if p.Online {
			state = "online"
		}
		fmt.Fprintf(&b, "| %s | `rune://%s/` | %s | %s |\n",
			p.Hostname, p.Hostname, p.OS, state)
	}
	return markdownOutput(b.String()), nil
}

func (h *Handler) machines(ctx context.Context) (
	iterator.Iterator[component.Responsive], error,
) {
	machines, err := h.network.Machines(ctx)
	if err != nil {
		return nil, err
	}
	if len(machines) == 0 {
		return markdownOutput("No machines registered to your account yet. " +
			"Run `network up` to add this one."), nil
	}
	// Which machine this is matters before removing one, but a node
	// that never joined has no name to report, so it is best effort.
	var self string
	if st, err := h.network.Status(ctx); err == nil {
		self = st.Hostname
	}
	return markdownOutput(machinesMarkdown(machines, self)), nil
}

func machinesMarkdown(machines []Machine, self string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Your machines\n\n")
	fmt.Fprintf(&b, "| machine | state | last seen |\n")
	fmt.Fprintf(&b, "| --- | --- | --- |\n")
	for _, m := range machines {
		name := m.Hostname
		if name == self {
			name += " (this machine)"
		}
		if m.ServeOnly {
			name += " (serve-only)"
		}
		state, lastSeen := "offline", "never"
		if m.Online {
			state = "online"
		}
		if !m.LastSeen.IsZero() {
			lastSeen = m.LastSeen.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", name, state, lastSeen)
	}
	fmt.Fprintf(&b,
		"\nRemove one with `network remove <machine>`.\n")
	return b.String()
}

func (h *Handler) remove(ctx context.Context, args []string) (
	iterator.Iterator[component.Responsive], error,
) {
	if len(args) != 1 {
		return nil, fmt.Errorf("usage: %s remove <machine>", CommandName)
	}
	if err := h.network.Remove(ctx, args[0]); err != nil {
		return nil, err
	}
	return markdownOutput(fmt.Sprintf(
		"Removed `%s`. Run `network up` on it to register it again.",
		args[0])), nil
}

func (h *Handler) up(ctx context.Context) (
	iterator.Iterator[component.Responsive], error,
) {
	if err := h.network.Up(ctx); err != nil {
		return nil, err
	}
	return h.status(ctx)
}

func (h *Handler) down(ctx context.Context) (
	iterator.Iterator[component.Responsive], error,
) {
	if err := h.network.Down(ctx); err != nil {
		return nil, err
	}
	return markdownOutput("Left the network."), nil
}

// Complete satisfies repl.CommandHandler.
func (h *Handler) Complete(
	ctx context.Context, _ string, args []string,
) (iterator.Iterator[string], error) {
	if len(args) == 2 && args[0] == "remove" {
		return h.completeMachine(ctx, args[1])
	}
	if len(args) > 1 {
		return iterator.FromSlice[string](nil), nil
	}
	var filter string
	if len(args) == 1 {
		filter = args[0]
	}
	var ret []string
	for _, c := range commandManual.Commands {
		if strings.HasPrefix(c.Name, filter) {
			ret = append(ret, c.Name)
		}
	}
	return iterator.FromSlice(ret), nil
}

// completeMachine offers the names `remove` takes. They come from the
// account rather than the mesh, so the machine the plan's allowance is
// keeping off the network — the one most likely to be removed — is
// offered too.
func (h *Handler) completeMachine(
	ctx context.Context, filter string,
) (iterator.Iterator[string], error) {
	names, err := h.network.MachineNames(ctx)
	if err != nil {
		return iterator.FromSlice[string](nil), nil
	}
	var ret []string
	for _, name := range names {
		if strings.HasPrefix(name, filter) {
			ret = append(ret, name)
		}
	}
	return iterator.FromSlice(ret), nil
}

// Help satisfies textapi.REPLHandler.
func (h *Handler) Help(
	_ context.Context, _ []string,
) (iterator.Iterator[component.Responsive], error) {
	return markdownOutput(usageMarkdown()), nil
}

func usageMarkdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## `%s`\n\n%s\n\n", commandManual.Name, commandManual.Summary)
	for _, c := range commandManual.Commands {
		fmt.Fprintf(&b, "- `%s %s` — %s\n", commandManual.Name, c.Name, c.Summary)
	}
	return b.String()
}

// markdownOutput wraps a markdown string into a single-shot iterator
// suitable for returning from HandleCommand. Falls back to a plain
// responsive string when the markdown parser rejects the content.
func markdownOutput(content string) iterator.Iterator[component.Responsive] {
	md, err := markdown.New(content)
	if err != nil {
		r := component.NewResponsiveString(content, component.StringResponsiveConfig{})
		return iterator.FromSlice([]component.Responsive{r})
	}
	return iterator.FromSlice([]component.Responsive{md})
}
