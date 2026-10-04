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
	"fmt"
	"net"
	"os"
	"os/user"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/ide/networkshell"
	"unstable.build/rune/internal/runenet"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacerune"
)

// network holds the process-wide mesh node and the workspace server it
// exposes to peers. The stack is always fully assembled, whatever the
// configuration says: `network.auto_join` only decides whether boot
// joins the mesh, so the `network up` console command works later
// without a restart.
type network struct {
	node *runenet.Node
	gate *networkGate
	// gated is false only when the node joins with credentials it was
	// given directly, which is a debug-build escape hatch: that mesh
	// is the developer's own, not the paid one, so the account is
	// never consulted.
	gated    bool
	autoJoin bool
	dataDir  string
	// shellRCDir holds the dotfiles for peers' login shells; see
	// workspace.NewFileSchemeFunc.
	shellRCDir string

	mu     sync.Mutex
	server *runenet.WorkspaceServer
	closed bool
	// prompter is only there once the IDE exists, which is after the
	// node may already have started; a push that lands before then
	// has nowhere to be shown.
	prompter *networkPrompter
}

// newNetwork assembles the mesh node and its entitlement gate. A
// malformed `network` config section falls back to defaults with a
// logged error rather than crippling the stack: config validation is
// the user's mistake to surface, not a reason to run partially
// constructed.
func newNetwork(
	rootCfg config.Config, dataDir, shellRCDir string, gate *networkGate,
) *network {
	if gate == nil {
		panic("newNetwork: gate must not be nil")
	}
	cfg, err := networkConfig(rootCfg, dataDir)
	if err != nil {
		log.Errorf("network: %v (using defaults)", err)
		cfg, _ = runenet.FromConfig(config.NopConfig(), dataDir)
	}
	ret := &network{
		gate:       gate,
		autoJoin:   cfg.AutoJoin,
		dataDir:    dataDir,
		shellRCDir: shellRCDir,
		// A key configured out of band belongs to a debug build
		// driving a coordination server of its own, which the paid
		// mesh must not be mixed up with.
		gated: cfg.AuthKey == "",
	}
	if ret.gated {
		cfg.Credentials = gate.credentials
		cfg.OnNeedsLogin = ret.onNeedsLogin
	}
	ret.node = runenet.New(cfg)
	return ret
}

// needsLoginTimeout bounds the account round-trip a logout triggers.
const needsLoginTimeout = 30 * time.Second

// onNeedsLogin runs when the coordination server stops accepting this
// machine's node key. Two very different things look like that from
// here and only the account's machine list tells them apart: a node
// key that reached the end of its lifetime, which is re-minted without
// bothering anyone, and a machine the account removed on purpose,
// which the user has to be told about.
func (n *network) onNeedsLogin() {
	prompter := n.currentPrompter()
	if prompter == nil {
		return
	}
	go debug.CapturePanicReport(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(), needsLoginTimeout)
		defer cancel()
		st, err := n.node.Status(ctx)
		if err != nil || st.MachineID == "" {
			// A machine that never registered has no removal to
			// report and no key worth re-minting.
			return
		}
		n.handleNeedsLogin(ctx, prompter, st.MachineID)
	})
}

func (n *network) handleNeedsLogin(
	ctx context.Context, prompter *networkPrompter, machineID string,
) {
	machines, err := n.gate.machines(ctx)
	if err != nil {
		// Without the list the two cases are indistinguishable, and
		// claiming the machine was removed when the account server is
		// merely unreachable would be worse than saying nothing.
		log.Warnf("network: could not check this machine's registration: %v", err)
		return
	}
	if machineRemoved(machineID, machines) {
		prompter.prompt(runenet.ErrMachineRemoved)
		return
	}
	if err := n.node.Up(ctx); err != nil {
		log.Warnf("could not re-register machine on the network: %v", err)
	}
}

func (n *network) setPrompter(prompter *networkPrompter) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.prompter = prompter
}

func (n *network) currentPrompter() *networkPrompter {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.prompter
}

// startAutoJoin joins the mesh off the boot path when the user asked
// for it. Failures stay out of the UI on purpose: a signed-out or
// unentitled account simply does not join, and the prompt comes when
// the user asks for the network explicitly.
func (n *network) startAutoJoin() {
	if !n.autoJoin {
		return
	}
	go debug.CapturePanicReport(func() {
		if err := n.join(context.Background()); err != nil {
			if errors.Is(err, runenet.ErrNotAuthenticated) ||
				errors.Is(err, runenet.ErrSubscriptionRequired) {
				log.Infof("network: not joining: %v", err)
			} else {
				log.Errorf("could not join network: %v", err)
			}
		}
	})
}

// join brings the node online and serves this machine's workspaces to
// its peers. It is idempotent, so a machine that could not join at
// startup joins on the first `network up` or rune:// open that follows.
func (n *network) join(ctx context.Context) error {
	if err := n.node.Start(ctx); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed || n.server != nil {
		return nil
	}
	server, err := serveNetworkWorkspaces(n.node, n.dataDir, n.shellRCDir)
	if err != nil {
		return fmt.Errorf("could not serve workspaces on the network: %w", err)
	}
	n.server = server
	return nil
}

// check reports whether this account may use the network.
func (n *network) check(ctx context.Context) error {
	if !n.gated {
		return nil
	}
	return n.gate.check(ctx)
}

func networkConfig(rootCfg config.Config, dataDir string) (runenet.Config, error) {
	cfg, err := rootCfg.GetConfig("network")
	if errors.Is(err, config.ErrNotFound) {
		cfg = config.NopConfig()
	} else if err != nil {
		return runenet.Config{}, fmt.Errorf("get 'network' section from config: %w", err)
	}
	return runenet.FromConfig(cfg, dataDir)
}

// serveNetworkWorkspaces exposes the whole filesystem to peers, rooted
// at "/", so a rune:// URI can name any path the user could open
// locally. Only machines owned by the same account get that far; see
// runenet.PeerAuthorizer.
//
// dataDir is advertised to peers as this instance's install root, so
// it is resolved to an absolute path here: the peer consumes it as a
// path on this host, not relative to its own process.
func serveNetworkWorkspaces(
	node *runenet.Node, dataDir, shellRCDir string,
) (*runenet.WorkspaceServer, error) {
	uri, err := workspaceapi.CurrentUserHostURI("/")
	if err != nil {
		return nil, fmt.Errorf("root workspace URI: %w", err)
	}
	dataDir, err = workspaceapi.ExpandPath(dataDir, user.Current, os.Getwd)
	if err != nil {
		return nil, fmt.Errorf("resolve data dir %s: %w", dataDir, err)
	}
	scheme, err := workspace.NewFileSchemeFunc(shellRCDir)(
		context.Background(), config.NopConfig(), uri)
	if err != nil {
		return nil, fmt.Errorf("root workspace scheme: %w", err)
	}
	server, err := runenet.ServeWorkspace(node, scheme, dataDir)
	if err != nil {
		_ = scheme.Close()
		return nil, err
	}
	return server, nil
}

// completerOption feeds mesh peers and their directories to
// `workspaceopen` completion. The completer deliberately never
// prompts — a completion runs on a keystroke, and a keystroke must not
// open a modal — so unlike the scheme it needs no IDE and is wired at
// IDE construction.
func (n *network) completerOption() ide.Option {
	return ide.WithWorkspaceOpenCompleter(workspacerune.Completer(gatedMesh{n}))
}

// register wires the network into a live IDE: the rune:// workspace
// scheme and the `network` console command. Both prompt the user to
// sign in or upgrade when the plan does not cover the network, which
// is why they are registered here rather than at IDE construction —
// the prompter cannot exist before the IDE does.
func (n *network) register(
	i *ide.IDE, scheduleNextTick func(func()) bool,
) error {
	prompter := newNetworkPrompter(i, scheduleNextTick)
	n.setPrompter(prompter)
	if err := i.RegisterScheme(
		workspacerune.Scheme, n.schemeFunc(prompter)); err != nil {
		return fmt.Errorf("register %s scheme: %w", workspacerune.Scheme, err)
	}
	// The mesh round-trips run off the editor's event loop, which is
	// why `network` is a console command rather than an ex-command.
	h := networkshell.New(networkshell.Config{
		Network: gatedNetwork{n: n, prompter: prompter},
	})
	if err := i.RegisterREPLCommand(networkshell.Manual(), h); err != nil {
		return fmt.Errorf("register '%s': %w", networkshell.CommandName, err)
	}
	return nil
}

// schemeFunc gates rune:// workspaces on the account's plan. The check
// reads cached claims only, because a workspace is opened on the event
// loop; a machine that has not joined yet does so in the background
// while the scheme's reconnect loop waits for it.
func (n *network) schemeFunc(prompter *networkPrompter) schemeapi.SchemeFunc {
	inner := workspacerune.New(n.node)
	return func(
		ctx context.Context, cfg config.Config, uri workspaceapi.URI,
	) (schemeapi.Scheme, error) {
		if err := n.check(ctx); err != nil {
			prompter.prompt(err)
			return nil, err
		}
		n.joinAsync()
		return inner(ctx, cfg, uri)
	}
}

// joinAsync joins the mesh off the event loop. Failures are logged
// rather than reported: the caller's reconnect loop is what tells the
// user whether the peer became reachable.
func (n *network) joinAsync() {
	go debug.CapturePanicReport(func() {
		if err := n.join(context.Background()); err != nil {
			log.Warnf("could not join network: %v", err)
		}
	})
}

// gatedMesh hides the mesh from an account whose plan does not cover
// the network: a completion runs on a keystroke, and a keystroke must
// not open a modal.
type gatedMesh struct {
	n *network
}

func (g gatedMesh) Peers(ctx context.Context) ([]runenet.Peer, error) {
	if err := g.n.check(ctx); err != nil {
		return nil, nil
	}
	return g.n.node.Peers(ctx)
}

func (g gatedMesh) Dial(ctx context.Context, peer string) (net.Conn, error) {
	if err := g.n.check(ctx); err != nil {
		return nil, err
	}
	return g.n.node.Dial(ctx, peer)
}

// gatedNetwork is what the `network` command operates on: the two
// subcommands that need the mesh check the plan first, so a user
// without one is told how to get it instead of being told the node is
// not running.
type gatedNetwork struct {
	n        *network
	prompter *networkPrompter
}

func (g gatedNetwork) Status(ctx context.Context) (runenet.Status, error) {
	if err := g.n.check(ctx); err != nil {
		g.prompter.prompt(err)
		return runenet.Status{}, err
	}
	st, err := g.n.node.Status(ctx)
	if err != nil {
		return runenet.Status{}, err
	}
	if g.removed(ctx, st) {
		g.prompter.prompt(runenet.ErrMachineRemoved)
	}
	return st, nil
}

// removed reports whether the account no longer lists this machine
// even though the backend still believes it is a member. That is what
// a machine removed while it was asleep looks like: it wakes holding a
// netmap the coordination server has already forgotten, so the logout
// that would have told it never arrives.
func (g gatedNetwork) removed(ctx context.Context, st runenet.Status) bool {
	if !g.n.gated || st.State != runenet.StateRunning {
		return false
	}
	machines, err := g.n.gate.machines(ctx)
	if err != nil {
		return false
	}
	return machineRemoved(st.MachineID, machines)
}

// joinWaitTimeout bounds how long `network up` waits for membership.
// The backend keeps retrying past it, so on expiry the user is pointed
// at `network status` rather than left hanging on a console command.
const joinWaitTimeout = 30 * time.Second

func (g gatedNetwork) Up(ctx context.Context) error {
	if err := g.n.join(ctx); err != nil {
		g.prompter.prompt(err)
		return err
	}
	upCtx, cancel := context.WithTimeout(ctx, joinWaitTimeout)
	defer cancel()
	// `network up` is what the removal prompt tells the user to run,
	// so it has to work for the machine that slept through its own
	// removal: that node is Running on a netmap nobody else has, and
	// only an unconditional re-registration gets it back.
	if st, err := g.n.node.Status(upCtx); err == nil && g.removed(upCtx, st) {
		if err := g.n.node.Rejoin(upCtx); err != nil {
			g.prompter.prompt(err)
			return err
		}
		return nil
	}
	if err := g.n.node.Up(upCtx); err != nil {
		if upCtx.Err() != nil && ctx.Err() == nil {
			return fmt.Errorf("not a member after %s; joining continues "+
				"in the background — run `network status`", joinWaitTimeout)
		}
		return err
	}
	return nil
}

func (g gatedNetwork) Peers(ctx context.Context) ([]runenet.Peer, error) {
	return g.n.node.Peers(ctx)
}

func (g gatedNetwork) Down(ctx context.Context) error {
	return g.n.node.Down(ctx)
}

// Machines and Remove go to the account server rather than the mesh:
// a machine kept off the network by the account's machine allowance is
// not there to be asked, and removing it is how the user makes room.
func (g gatedNetwork) Machines(
	ctx context.Context,
) ([]networkshell.Machine, error) {
	machines, err := g.n.gate.machines(ctx)
	if err != nil {
		g.prompter.prompt(err)
		return nil, err
	}
	ret := make([]networkshell.Machine, 0, len(machines))
	for _, m := range machines {
		ret = append(ret, networkshell.Machine{
			Hostname:  m.Hostname,
			LastSeen:  m.LastSeen,
			Online:    m.Online,
			ServeOnly: m.ServeOnly,
		})
	}
	return ret, nil
}

func (g gatedNetwork) Remove(ctx context.Context, hostname string) error {
	if err := g.n.gate.remove(ctx, hostname); err != nil {
		g.prompter.prompt(err)
		return err
	}
	return nil
}

// MachineNames feeds `network remove` completion. It never prompts,
// unlike Machines: a completion runs on a keystroke, and a keystroke
// must not open a modal. A signed-out account simply offers nothing.
func (g gatedNetwork) MachineNames(ctx context.Context) ([]string, error) {
	machines, err := g.n.gate.machines(ctx)
	if err != nil {
		return nil, err
	}
	ret := make([]string, 0, len(machines))
	for _, m := range machines {
		ret = append(ret, m.Hostname)
	}
	return ret, nil
}

// Close leaves the mesh and stops serving workspaces to peers.
func (n *network) Close() error {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	n.closed = true
	server := n.server
	n.server = nil
	n.mu.Unlock()

	var errs []error
	if server != nil {
		errs = append(errs, server.Close())
	}
	// The node outlives Close so a rune:// open racing shutdown reads
	// a closed node rather than a nil one.
	errs = append(errs, n.node.Close())
	return errors.Join(errs...)
}
