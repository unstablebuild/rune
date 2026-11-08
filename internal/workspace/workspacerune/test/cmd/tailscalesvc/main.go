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
//
// open and peers report a failure on stdout as an errorPrefix line with
// the gRPC code, so a test can tell a refusal from a broken network.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"google.golang.org/grpc/status"
	"unstable.build/rune/internal/runenet"
	"unstable.build/rune/internal/tailnet"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacerune"
)

// Lines printed on stdout for the test harness.
const (
	readyLine    = "tailscalesvc: ready"
	signInPrefix = "tailscalesvc: sign in at "
	errorPrefix  = "tailscalesvc: error "
)

func main() {
	if len(os.Args) < 2 {
		fail(2, "usage: tailscalesvc serve|headless|open|peers [flags]")
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
	uri, err := workspaceapi.ParseURI(*rawURI)
	if err != nil {
		fail(2, "%v", err)
	}
	client := tailnet.New(tailnet.Config{})
	scheme, err := workspacerune.New(workspace.TailscaleScheme, client)(
		context.Background(), config.NopConfig(), uri)
	if err != nil {
		report(err)
	}
	defer scheme.Close()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	// A refusal ends the wait too, and the next call reports it.
	_ = scheme.(workspace.RemoteScheme).WaitConnected(ctx)
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
