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

// Command tailscalesvc is the Rune side of the tailscale:// end-to-end
// tests. It runs inside the test's containers, next to a real Tailscale
// client, and does what cmd/rune does on such a machine, without the
// CGO and GUI dependencies of the full rune binary:
//
//	tailscalesvc serve -datadir DIR
//	    serves the machine's workspaces through its Tailscale client.
//	tailscalesvc headless -control-url URL -hostname NAME -datadir DIR
//	    joins the tailnet as a machine of its own and serves there, as
//	    `rune --headless --control-url` does.
//	tailscalesvc open -uri tailscale://PEER/DIR [-read FILE]
//	    opens a workspace and prints FILE from it, or its entries.
//	tailscalesvc peers
//	    prints the machines `workspaceopen tailscale://` offers.
//	tailscalesvc terminal create -uri URI [-cmd CMD] [-line TEXT] [-wait TEXT]
//	    creates a terminal in the workspace on the peer, which owns it,
//	    types TEXT and a return into it, waits until its screen shows
//	    WAIT, prints its id and screen, and exits, leaving it detached.
//	tailscalesvc terminal attach -uri URI -id ID [-line TEXT] [-wait TEXT]
//	    attaches to the terminal ID as create does, and detaches again.
//	tailscalesvc terminal list -uri URI
//	    prints the workspace's terminals: id, attachments and liveness.
//	tailscalesvc terminal delete -uri URI -id ID
//	    ends the terminal ID on the peer.
//
// open, peers and terminal report a failure on stdout as an errorPrefix
// line with the gRPC code, so a test can tell a refusal from a broken
// network.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/runenet"
	"unstable.build/rune/internal/tailnet"
	"unstable.build/rune/internal/term/programstatus"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vteclient"
	"unstable.build/rune/internal/workspace"
	tworkspacerpc "unstable.build/rune/internal/workspace/workspacerpc"
	"unstable.build/rune/internal/workspace/workspacerune"
)

// Lines printed on stdout for the test harness.
const (
	readyLine      = "tailscalesvc: ready"
	signInPrefix   = "tailscalesvc: sign in at "
	errorPrefix    = "tailscalesvc: error "
	terminalPrefix = "tailscalesvc: terminal "
)

func main() {
	if len(os.Args) < 2 {
		fail(2, "usage: tailscalesvc serve|headless|open|peers|terminal [flags]")
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "serve":
		serve(args)
	case "headless":
		headless(args)
	case "open":
		open(args)
	case "peers":
		peers(args)
	case "terminal":
		terminal(args)
	default:
		fail(2, "unknown command %q", cmd)
	}
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dataDir := fs.String("datadir", "", "Rune data directory")
	_ = fs.Parse(args)
	scheme := rootScheme(*dataDir)
	server := tailnet.Serve(tailnet.New(tailnet.Config{}), scheme, *dataDir)
	defer server.Close()
	fmt.Println(readyLine)
	waitForSignal()
}

func headless(args []string) {
	fs := flag.NewFlagSet("headless", flag.ExitOnError)
	controlURL := fs.String("control-url", "", "coordination server")
	hostname := fs.String("hostname", "", "machine name")
	dataDir := fs.String("datadir", "", "Rune data directory")
	_ = fs.Parse(args)
	url, err := runenet.ParseControlURL(*controlURL)
	if err != nil {
		fail(2, "%v", err)
	}
	node := runenet.New(runenet.Config{
		Hostname:      *hostname,
		ControlURL:    url,
		CustomControl: true,
		Port:          runenet.DefaultPort,
		Dir:           *dataDir,
		OnAuthURL:     func(authURL string) { fmt.Println(signInPrefix + authURL) },
	})
	defer node.Close()
	scheme := rootScheme(*dataDir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	server, err := tailnet.Join(ctx, node, scheme, *dataDir)
	if err != nil || server == nil {
		fail(3, "join: %v", err)
	}
	defer server.Close()
	fmt.Println(readyLine)
	waitForSignal()
}

func open(args []string) {
	fs := flag.NewFlagSet("open", flag.ExitOnError)
	rawURI := fs.String("uri", "", "tailscale:// workspace URI")
	read := fs.String("read", "", "file to print")
	timeout := fs.Duration("timeout", 30*time.Second, "how long to try")
	_ = fs.Parse(args)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	scheme, _ := openWorkspace(ctx, *rawURI)
	defer scheme.Close()
	if *read == "" {
		entries, err := scheme.ReadDir(".")
		if err != nil {
			report(err)
		}
		for _, e := range entries {
			fmt.Println(e.Name())
		}
		return
	}
	data, err := readFile(scheme, *read)
	if err != nil {
		report(err)
	}
	fmt.Print(data)
}

// openWorkspace opens rawURI through this machine's Tailscale client
// and waits for the connection under ctx. A refusal ends the wait too,
// and the caller's first call reports it.
func openWorkspace(ctx context.Context, rawURI string) (schemeapi.Scheme, workspaceapi.URI) {
	uri, err := workspaceapi.ParseURI(rawURI)
	if err != nil {
		fail(2, "%v", err)
	}
	client := tailnet.New(tailnet.Config{})
	scheme, err := workspacerune.New(workspace.TailscaleScheme, client)(
		context.Background(), config.NopConfig(), uri)
	if err != nil {
		report(err)
	}
	_ = scheme.(workspace.RemoteScheme).WaitConnected(ctx)
	return scheme, uri
}

func readFile(scheme schemeapi.Scheme, name string) (string, error) {
	f, err := scheme.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	return string(data), err
}

func peers(args []string) {
	fs := flag.NewFlagSet("peers", flag.ExitOnError)
	_ = fs.Parse(args)
	found, err := tailnet.New(tailnet.Config{}).Peers(context.Background())
	if err != nil {
		report(err)
	}
	for _, p := range found {
		fmt.Println(p.Hostname)
	}
}

func terminal(args []string) {
	if len(args) == 0 {
		fail(2, "usage: tailscalesvc terminal create|attach|list|delete [flags]")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("terminal "+sub, flag.ExitOnError)
	rawURI := fs.String("uri", "", "tailscale:// workspace URI")
	id := fs.String("id", "", "terminal id")
	cmd := fs.String("cmd", "", "program to run, the login shell when empty")
	line := fs.String("line", "", "text typed into the terminal, followed by a return")
	wait := fs.String("wait", "", "text to wait for on the screen before printing it")
	timeout := fs.Duration("timeout", 30*time.Second, "how long to try")
	_ = fs.Parse(args)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	scheme, uri := openWorkspace(ctx, *rawURI)
	defer scheme.Close()
	peer, ok := scheme.(workspace.PeerScheme).Peer()
	if !ok {
		fail(4, "no peer serves the workspace")
	}
	root, err := peer.Root(ctx)
	if err != nil {
		report(err)
	}
	host := tworkspacerpc.NewVTEClient(peer.Conn(), root)

	switch sub {
	case "list":
		infos, err := host.ListVTEs(ctx)
		if err != nil {
			report(err)
		}
		for _, info := range infos {
			l, err := host.VTELiveness(ctx, info.ID)
			if err != nil {
				report(err)
			}
			fmt.Printf("%s%s attachments=%d liveness=%s\n",
				terminalPrefix, info.ID, info.Attachments, l)
		}
	case "delete":
		if err := host.DeleteVTE(ctx, vte.ID(*id)); err != nil {
			report(err)
		}
	case "create", "attach":
		loop := newLoop()
		defer loop.stop()
		cfg := vte.DefaultConfig()
		cfg.ScheduleNextTick = loop.schedule
		owner := vteclient.New(peer, nopHost{}, nopHost{}, nopHost{},
			vteclient.Config{VTE: cfg, Machine: uri.Host()})
		defer owner.Close()
		var (
			v   vte.View
			err error
		)
		if sub == "create" {
			opts := vte.CreateOptions{Width: 80, Height: 24}
			if *cmd != "" {
				opts.CommandAndArgs = []string{*cmd}
			}
			v, err = owner.CreateVTE(ctx, opts)
		} else {
			v, err = owner.AttachVTE(ctx, vte.ID(*id))
		}
		if err != nil {
			report(err)
		}
		defer v.Close()
		if *line != "" {
			if err := v.(typer).Component().WriteToPty([]byte(*line + "\r")); err != nil {
				report(err)
			}
		}
		screen := screenOf(v)
		for *wait != "" && !strings.Contains(screen, *wait) {
			if ctx.Err() != nil {
				report(status.Errorf(codes.DeadlineExceeded,
					"the screen never showed %q:\n%s", *wait, screen))
			}
			time.Sleep(50 * time.Millisecond)
			screen = screenOf(v)
		}
		fmt.Println(terminalPrefix + string(v.ID()))
		fmt.Print(screen)
	default:
		fail(2, "unknown terminal command %q", sub)
	}
}

// typer is the replica's way to the pty on the peer.
type typer interface {
	Component() *vte.Component
}

// screenOf renders v's active screen without its trailing blanks.
func screenOf(v vte.View) string {
	snap, err := v.Snapshot()
	if err != nil {
		report(err)
	}
	var lines []string
	for _, row := range snap.ActiveCells() {
		var b strings.Builder
		for _, c := range row {
			if c.Ch == 0 {
				b.WriteByte(' ')
				continue
			}
			b.WriteRune(c.Ch)
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// loop runs what the replica schedules on one goroutine, in order, as
// the editor's event loop would.
type loop struct {
	mu    sync.Mutex
	queue []func()
	wake  chan struct{}
	done  chan struct{}
}

func newLoop() *loop {
	l := &loop{wake: make(chan struct{}, 1), done: make(chan struct{})}
	go debug.CapturePanicReport(func() {
		for {
			select {
			case <-l.wake:
			case <-l.done:
				return
			}
			l.mu.Lock()
			fns := l.queue
			l.queue = nil
			l.mu.Unlock()
			for _, fn := range fns {
				fn()
			}
		}
	})
	return l
}

func (l *loop) schedule(fn func()) bool {
	l.mu.Lock()
	l.queue = append(l.queue, fn)
	l.mu.Unlock()
	select {
	case l.wake <- struct{}{}:
	default:
	}
	return true
}

func (l *loop) stop() { close(l.done) }

// nopHost is the replica's browser: nothing shows it.
type nopHost struct{}

func (nopHost) PublishEvent(term.Event) error { return nil }

func (nopHost) Notify(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

func (nopHost) NotifyOnce(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

func (nopHost) UpdateNotificationProgress(string, string, int64, int64) error { return nil }

func (nopHost) Tab(workspaceapi.URI, rune, string, browserapi.Handler) (browserapi.Handler, error) {
	return nil, nil
}

func (nopHost) SetTabName(workspaceapi.URI, string, term.Attributes) error { return nil }

func (nopHost) OnTabExit(workspaceapi.URI) bool { return false }

func (nopHost) SetTabStatus(workspaceapi.URI, programstatus.Report) error { return nil }

func (nopHost) EndTabStatus(workspaceapi.URI) error { return nil }

// rootScheme is the whole filesystem, as cmd/rune serves it.
func rootScheme(dataDir string) schemeapi.Scheme {
	uri, err := workspaceapi.CurrentUserHostURI("/")
	if err != nil {
		fail(4, "root uri: %v", err)
	}
	scheme, err := workspace.NewFileSchemeFunc(dataDir, "")(
		context.Background(), config.NopConfig(), uri)
	if err != nil {
		fail(4, "root scheme: %v", err)
	}
	return scheme
}

// report prints err as the harness reads it and exits.
func report(err error) {
	for next := errors.Unwrap(err); next != nil; next = errors.Unwrap(err) {
		err = next
	}
	st := status.Convert(err)
	fmt.Printf("%s%s: %s\n", errorPrefix, st.Code(), st.Message())
	os.Exit(1)
}

func fail(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "tailscalesvc: "+format+"\n", args...)
	os.Exit(code)
}

func waitForSignal() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
}
