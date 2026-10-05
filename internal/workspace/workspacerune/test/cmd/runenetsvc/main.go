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

// Command runenetsvc is the peer side of the rune:// end-to-end tests:
// a second Rune instance, in its own process, joining the network and
// serving its workspaces exactly as cmd/rune does when
// `network.enabled` is set.
//
// It exists so the tests exercise two genuinely separate instances
// rather than two nodes sharing one process's runtime, without pulling
// in the CGO and GUI dependencies of the full rune binary.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/runenet"
	"unstable.build/rune/internal/workspace"
)

// Lines printed on stdout for the test harness. readyLine comes once
// the instance is reachable by peers, after the addr and login lines;
// the harness blocks on it instead of polling, so a slow join delays
// the test rather than flaking it.
const (
	readyLine    = "runenetsvc: ready"
	addrPrefix   = "runenetsvc: addr "
	loginPrefix  = "runenetsvc: login "
	acceptPrefix = "runenetsvc: accepted "
)

func main() {
	hostname := flag.String("hostname", "",
		"name this instance is known by on the network")
	controlURL := flag.String("control-url", "",
		"coordination server the instance registers with")
	authKey := flag.String("auth-key", "",
		"pre-authorization key, so no interactive sign-in is needed")
	dataDir := flag.String("datadir", "",
		"data directory holding this instance's network identity")
	probePort := flag.Int("probe-port", 0,
		"if set, also accept bare connections on this mesh port and "+
			"report each caller, so a test can tell whether packets arrive")
	flag.Parse()

	if *hostname == "" || *controlURL == "" || *dataDir == "" {
		fmt.Fprintln(os.Stderr,
			"runenetsvc: -hostname, -control-url and -datadir are required")
		os.Exit(2)
	}

	node := runenet.New(runenet.Config{
		Hostname:   *hostname,
		ControlURL: *controlURL,
		AuthKey:    *authKey,
		Port:       runenet.DefaultPort,
		Dir:        *dataDir,
	})
	if err := node.Start(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "runenetsvc: start network:", err)
		os.Exit(3)
	}
	defer node.Close()

	// Serve the whole filesystem, as cmd/rune does: a rune:// URI can
	// name any path the user could open locally.
	uri, err := workspaceapi.CurrentUserHostURI("/")
	if err != nil {
		fmt.Fprintln(os.Stderr, "runenetsvc: root uri:", err)
		os.Exit(4)
	}
	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri)
	if err != nil {
		fmt.Fprintln(os.Stderr, "runenetsvc: root scheme:", err)
		os.Exit(5)
	}
	defer scheme.Close()

	server, err := runenet.ServeWorkspace(node, scheme, *dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "runenetsvc: serve workspaces:", err)
		os.Exit(6)
	}
	defer server.Close()

	if *probePort != 0 {
		lis, err := node.ListenPort(*probePort)
		if err != nil {
			fmt.Fprintln(os.Stderr, "runenetsvc: probe listener:", err)
			os.Exit(8)
		}
		defer lis.Close()
		go debug.CapturePanicReport(func() { reportAccepts(lis) })
	}

	st, err := waitRunning(node)
	if err != nil {
		fmt.Fprintln(os.Stderr, "runenetsvc:", err)
		os.Exit(7)
	}
	fmt.Println(addrPrefix + st.Addrs[0].String())
	fmt.Println(loginPrefix + st.LoginName)
	fmt.Println(readyLine)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
}

func reportAccepts(lis net.Listener) {
	for {
		conn, err := lis.Accept()
		if err != nil {
			return
		}
		host, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
		fmt.Println(acceptPrefix + host)
		_ = conn.Close()
	}
}

func waitRunning(node *runenet.Node) (runenet.Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	for {
		st, err := node.Status(ctx)
		if err == nil && st.State == runenet.StateRunning && len(st.Addrs) > 0 {
			return st, nil
		}
		select {
		case <-ctx.Done():
			return runenet.Status{}, fmt.Errorf("did not join the network in time")
		case <-time.After(200 * time.Millisecond):
		}
	}
}
