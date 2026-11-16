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

package ide

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vteregistry"
	"unstable.build/rune/internal/term/vte/vtereservoir"
)

// terminalSessionsConfig is what terminalSessions needs of the
// workspace whose terminals it keeps.
type terminalSessionsConfig struct {
	// Ctx bounds the terminals. Once it is done the workspace is
	// closing, and a view that closes ends its terminal.
	Ctx context.Context
	// Publisher animates the views of the terminals that are starting.
	Publisher browser.EventPublisher
	// Notifications tells the user of the terminals that fail to start.
	Notifications browserapi.Notifications
	// Terminal is the scheme the shells a reservoir keeps warm run on.
	Terminal schemeapi.Terminal
	// VTE configures every terminal. Its CommandAndArgs is the shell a
	// reservoir of ReservoirCapacity keeps warm, none when zero. Its
	// ScheduleNextTick runs on the host event loop.
	VTE               vte.Config
	ReservoirCapacity int
}

// terminalSessions keeps the terminals of a workspace and the views
// that show them. A terminal outlives its views: closing the last one
// keeps a terminal that runs something, which the status inbox lists
// as detached until a view attaches it again or it ends.
//
// It is used on the host event loop. Whatever asks the host of a pty,
// which may be remote, runs off it and calls back on it.
type terminalSessions struct {
	cfg       terminalSessionsConfig
	vtes      *vteregistry.Registry
	reservoir *vtereservoir.Facility
	// tabs is the TabManager the terminals of vtes report to: it routes
	// the tab and status calls a terminal makes under its own URI to
	// the tabs of its views, and keeps the tabs of failed terminals
	// open.
	tabs failedTerminalKeeper
	// preview is the view the status inbox shows a detached terminal
	// with.
	preview vte.View
	// inflight tracks the goroutines that start terminals and that ask
	// what they run. Test-only synchronization point.
	inflight sync.WaitGroup
}

// newTerminalSessions keeps the terminals of vtes, which report to
// tabs. Closing it closes vtes.
func newTerminalSessions(
	vtes *vteregistry.Registry, tabs failedTerminalKeeper, cfg terminalSessionsConfig,
) *terminalSessions {
	s := &terminalSessions{cfg: cfg, vtes: vtes, tabs: tabs}
	if cfg.ReservoirCapacity != 0 {
		s.reservoir = s.newReservoir()
	}
	return s
}

func (s *terminalSessions) newReservoir() *vtereservoir.Facility {
	return vtereservoir.New(s.vtes, s.cfg.Terminal, s.cfg.VTE, s.cfg.ReservoirCapacity)
}

// resetReservoir replaces the warm shells, which run on the scheme the
// workspace used when they started.
func (s *terminalSessions) resetReservoir() {
	if s.reservoir == nil {
		return
	}
	_ = s.reservoir.Close()
	s.reservoir = s.newReservoir()
}

// resize sizes the shells kept warm for the next view.
func (s *terminalSessions) resize(width, height int) {
	if s.reservoir != nil {
		s.reservoir.Resize(width, height)
	}
}

// open returns a view of a new terminal that runs cmdAndArgs, the
// shell when it is empty. The reservoir serves the shell.
func (s *terminalSessions) open(cmdAndArgs []string) *asyncVTE {
	vtes, ctx, pool := s.vtes, s.cfg.Ctx, s.reservoir
	if !argsMatchEmulatorShell(cmdAndArgs, s.cfg.VTE.CommandAndArgs) {
		pool = nil
	}
	av := newAsyncVTE(s, func() (vte.View, error) {
		if pool != nil {
			s.log(log.TraceLevel, "getting vte instance from reservoir")
			return pool.Get()
		}
		return vtes.CreateVTE(ctx, vte.CreateOptions{
			CommandAndArgs: cmdAndArgs,
		})
	})
	av.pool = pool
	return av
}

// attach returns another view of the terminal id.
func (s *terminalSessions) attach(id vte.ID) *asyncVTE {
	vtes, ctx := s.vtes, s.cfg.Ctx
	av := newAsyncVTE(s, func() (vte.View, error) {
		return vtes.AttachVTE(ctx, id)
	})
	av.existing = true
	return av
}

// info describes the terminal id, which is false when there is none
// such.
func (s *terminalSessions) info(id vte.ID) (vte.Info, bool) {
	infos, err := s.vtes.ListVTEs(s.cfg.Ctx)
	if err != nil {
		return vte.Info{}, false
	}
	for _, info := range infos {
		if info.ID == id {
			return info, true
		}
	}
	return vte.Info{}, false
}

// list returns the terminals users can reach, detached ones first.
// Terminals that ended, and the pooled ones no user has seen, are not
// listed.
func (s *terminalSessions) list() []vte.Info {
	infos, err := s.vtes.ListVTEs(s.cfg.Ctx)
	if err != nil {
		return nil
	}
	infos = slices.DeleteFunc(infos, func(info vte.Info) bool {
		return info.Complete || s.reservoir != nil && s.reservoir.Pooled(info.ID)
	})
	sort.SliceStable(infos, func(i, j int) bool {
		return infos[i].Attachments == 0 && infos[j].Attachments != 0
	})
	return infos
}

// terminalIDLength is how much of a terminal's id its candidates show,
// which is enough to tell apart the terminals of a workspace.
const terminalIDLength = 8

// lookup returns the listed terminal whose id the first word of args
// prefixes.
func (s *terminalSessions) lookup(args []string) (vte.Info, error) {
	return matchTerminal(s.list(), args)
}

// matchTerminal returns the terminal of infos whose id the first word
// of args prefixes.
func matchTerminal(infos []vte.Info, args []string) (vte.Info, error) {
	fields := strings.Fields(strings.Join(args, " "))
	if len(fields) == 0 {
		return vte.Info{}, errors.New("expected terminal id")
	}
	var found []vte.Info
	for _, info := range infos {
		if strings.HasPrefix(string(info.ID), fields[0]) {
			found = append(found, info)
		}
	}
	switch len(found) {
	case 0:
		return vte.Info{}, fmt.Errorf("terminal %q: %w", fields[0], vte.ErrNotFound)
	case 1:
		return found[0], nil
	}
	return vte.Info{}, fmt.Errorf("terminal id %q is ambiguous", fields[0])
}

// detached returns the listed terminal no view attaches that reports
// under uri.
func (s *terminalSessions) detached(uri workspaceapi.URI) (vte.Info, bool) {
	for _, info := range s.list() {
		if info.Attachments == 0 && info.URI.Equal(uri) {
			return info, true
		}
	}
	return vte.Info{}, false
}

// liveness tells then, on the event loop, whether the terminal id runs
// something for the user: unknown when that cannot be told.
func (s *terminalSessions) liveness(id vte.ID, then func(vte.Liveness)) {
	vtes, ctx := s.vtes, s.cfg.Ctx
	s.offLoop(func() func() {
		l, err := vtes.VTELiveness(ctx, id)
		if err != nil {
			l = vte.LivenessUnknown
		}
		return func() { then(l) }
	})
}

// runningDetached tells then, on the event loop, how many listed
// terminals no view attaches run something. Without any such
// terminal, it tells it at once.
func (s *terminalSessions) runningDetached(then func(int)) {
	var ids []vte.ID
	for _, info := range s.list() {
		if info.Attachments == 0 {
			ids = append(ids, info.ID)
		}
	}
	if len(ids) == 0 {
		then(0)
		return
	}
	vtes, ctx := s.vtes, s.cfg.Ctx
	s.offLoop(func() func() {
		n := 0
		for _, id := range ids {
			if l, err := vtes.VTELiveness(ctx, id); err == nil && l == vte.LivenessRunning {
				n++
			}
		}
		return func() { then(n) }
	})
}

// candidates names the listed terminals as terminalCandidate does. The
// prompt reads them off the event loop, as telling what a terminal
// runs may wait on the host of its pty.
func (s *terminalSessions) candidates(ctx context.Context) iterator.Iterator[string] {
	vtes := s.vtes
	return iterator.Map(iterator.FromSlice(s.list()),
		func(info vte.Info) string {
			l, err := vtes.VTELiveness(ctx, info.ID)
			if err != nil {
				l = vte.LivenessUnknown
			}
			return terminalCandidate(info, l)
		})
}

// remove terminates the terminal id and every view of it. A terminal
// that is gone already is no error.
func (s *terminalSessions) remove(id vte.ID) error {
	err := s.vtes.DeleteVTE(s.cfg.Ctx, id)
	if errors.Is(err, vte.ErrNotFound) {
		return nil
	}
	return err
}

// end terminates the terminal v shows, and closes v. A v of no
// terminal s keeps is only closed.
func (s *terminalSessions) end(v vte.View) error {
	return errors.Join(s.remove(v.ID()), v.Close())
}

// closeView drops av, a view of one of the terminals. Unless av's
// closeMode says otherwise, the terminal lives on while it may run
// something for the user or another view shows it; any other goes back
// to the reservoir it came from, or ends. Telling what it runs asks the
// host of its pty off the event loop, so av may outlive the call.
func (s *terminalSessions) closeView(av *asyncVTE) error {
	switch {
	case av.closeMode == vteCloseDetach:
		return s.detachView(av)
	case av.closeMode != vteCloseAuto || s.closing():
		return s.endView(av)
	}
	id := av.real.ID()
	info, ok := s.info(id)
	if !ok {
		return s.endView(av)
	}
	if info.Attachments > 1 {
		return s.detachView(av)
	}
	s.liveness(id, func(l vte.Liveness) {
		var err error
		// Another view may have attached it, or something deleted it,
		// while it was asked.
		if info, ok := s.info(id); ok && !s.closing() &&
			(info.Attachments > 1 || l != vte.LivenessIdle) {
			err = s.detachView(av)
		} else {
			err = s.endView(av)
		}
		if err != nil {
			s.log(log.WarnLevel, "close terminal: %v", err)
		}
	})
	return nil
}

// detachView drops av and leaves its terminal running. What the
// terminal reports goes to the views left or, without any, to the
// terminal itself.
func (s *terminalSessions) detachView(av *asyncVTE) error {
	canonical := av.real.URI()
	leaves := s.tabs.resolve(av.uri)
	s.forgetView(av.uri)
	for _, leaf := range leaves {
		s.tabs.handBack(leaf, canonical)
	}
	return av.real.Close()
}

// endView drops av and terminates its terminal and every view of it,
// unless the reservoir it came from takes it back.
func (s *terminalSessions) endView(av *asyncVTE) error {
	s.tabs.discardStatus(av.real.URI())
	s.forgetView(av.uri)
	if av.closeMode == vteCloseDelete || s.closing() {
		return s.end(av.real)
	}
	s.release(av.real, av.pool)
	return nil
}

// abandonView disposes of v, which the factory of av returned after av
// closed: a view of a terminal that ran before is closed, and a
// terminal av started ends.
func (s *terminalSessions) abandonView(av *asyncVTE, v vte.View) {
	if av.existing {
		_ = v.Close()
		return
	}
	s.tabs.discardStatus(v.URI())
	s.release(v, av.pool)
}

// release ends v, a terminal a view started, handing it back to pool,
// the reservoir it came from, when there is one.
func (s *terminalSessions) release(v vte.View, pool *vtereservoir.Facility) {
	if pool != nil {
		pool.Release(v)
		return
	}
	if err := s.end(v); err != nil {
		s.log(log.WarnLevel, "delete terminal: %v", err)
	}
}

// forgetView drops the aliases of the view uri both ways: from the
// terminal it shows, and to the tab that shows it.
func (s *terminalSessions) forgetView(uri workspaceapi.URI) {
	s.tabs.removeAliasesTo(uri)
	s.tabs.removeAlias(uri)
}

// previewOf returns the view the status inbox previews the detached
// terminal id with. The inbox asks for it on every draw, so the view is
// kept until another terminal is previewed or closePreview drops it.
func (s *terminalSessions) previewOf(id vte.ID) (vte.View, error) {
	if s.preview != nil {
		if s.preview.ID() == id && !s.preview.IsComplete() {
			return s.preview, nil
		}
		s.closePreview()
	}
	v, err := s.vtes.PreviewVTE(s.cfg.Ctx, id)
	if err != nil {
		return nil, err
	}
	s.preview = v
	return v, nil
}

func (s *terminalSessions) closePreview() {
	if s.preview != nil {
		_ = s.preview.Close()
		s.preview = nil
	}
}

// detachedSnapshots snapshots the listed terminals no view attaches,
// titled as they are listed. They stay detached: attaching one would
// withdraw what it reports.
func (s *terminalSessions) detachedSnapshots() []vte.Snapshot {
	var ret []vte.Snapshot
	for _, info := range s.list() {
		if info.Attachments != 0 {
			continue
		}
		v, err := s.vtes.PreviewVTE(s.cfg.Ctx, info.ID)
		if err != nil {
			continue
		}
		snap, err := v.Snapshot()
		_ = v.Close()
		if err != nil {
			continue
		}
		if snap.Title == "" {
			snap.Title = info.Title
		}
		ret = append(ret, snap)
	}
	return ret
}

// aliasTab routes the tab and status calls made under uri to the tab
// keyed by tab too.
func (s *terminalSessions) aliasTab(uri, tab workspaceapi.URI) {
	s.tabs.addAlias(uri, tab)
}

// closing tells whether the workspace is closing, which ends every
// terminal anyway.
func (s *terminalSessions) closing() bool {
	return s.cfg.Ctx.Err() != nil
}

// offLoop runs work off the event loop, and then on the event loop what
// work returns.
func (s *terminalSessions) offLoop(work func() func()) {
	s.inflight.Add(1)
	go debug.CapturePanicReport(func() {
		defer s.inflight.Done()
		s.cfg.VTE.ScheduleNextTick(work())
	})
}

// wait blocks until every goroutine s started has scheduled what it
// calls back with. Test-only synchronization point.
func (s *terminalSessions) wait() {
	s.inflight.Wait()
}

// close terminates every terminal.
func (s *terminalSessions) close() error {
	s.closePreview()
	var err error
	if s.reservoir != nil {
		err = s.reservoir.Close()
	}
	return errors.Join(err, s.vtes.Close())
}

func (s *terminalSessions) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "ide.terminalSessions").Logf(level, msg, args...)
}

// argsMatchEmulatorShell reports whether the requested
// terminal command is satisfied by the configured shell, in which
// case the warm reservoir can serve it. An empty argument list
// always falls back to the default shell so it matches.
func argsMatchEmulatorShell(cmdAndArgs, configured []string) bool {
	if len(cmdAndArgs) == 0 {
		return true
	}
	return slices.Equal(cmdAndArgs, configured)
}

// terminalCandidate names info by the prefix of its id lookup takes,
// followed by what tells it apart for the user.
func terminalCandidate(info vte.Info, l vte.Liveness) string {
	id := string(info.ID)
	if len(id) > terminalIDLength {
		id = id[:terminalIDLength]
	}
	state := l.String()
	if info.Attachments == 0 {
		state = "detached, " + state
	}
	title := info.Title
	if title == "" {
		title = "terminal"
	}
	return command.ShellQuote(fmt.Sprintf("%s (%s) %s", id, state, title))
}

// terminalCloseWarning asks whether to end the terminal info describes,
// whose liveness is l, when that ends its program or takes it from tabs
// other than the own of the caller. It is empty when there is nothing
// to warn of.
func terminalCloseWarning(info vte.Info, own int, l vte.Liveness) string {
	var reasons []string
	if l == vte.LivenessRunning {
		reasons = append(reasons, "is running a program")
	}
	if others := info.Attachments - own; others == 1 {
		reasons = append(reasons, "is shown in another tab")
	} else if others > 1 {
		reasons = append(reasons, fmt.Sprintf("is shown in %d other tabs", others))
	}
	if len(reasons) == 0 {
		return ""
	}
	title := info.Title
	if title == "" {
		title = "terminal"
	}
	return fmt.Sprintf("Terminal '%s' %s. Close it?", title, strings.Join(reasons, " and "))
}
