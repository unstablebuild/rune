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
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"unstable.build/rune/auth"
	"unstable.build/rune/cmd/rune/ide/apiclient"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/runenet"
	"unstable.build/rune/internal/workspace"
)

// headlessClient is the subset of *apiclient.Client a headless node
// needs to get itself signed in.
type headlessClient interface {
	Login(ctx context.Context) apiclient.LoginSession
	AccountStatus(ctx context.Context) (auth.RPCUser, bool, error)
}

// runHeadless serves this machine's workspaces on the Rune network with
// no editor at all: no IDE, no terminal UI and no window. Everything the
// operator would otherwise read out of the editor — the OAuth URL, the
// account, the node's mesh status — goes to stdout, and the editor log
// is teed there too so the process is usable under a service manager.
func runHeadless(ctx context.Context) int {
	rootCfg, err := ide.Config(*flagConfigPath, runeDefaultConfig())
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %s\n", err)
		return 1
	}

	closeLog, err := startHeadlessLogging(rootCfg, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}
	defer closeLog()

	netCfg, err := networkConfig(rootCfg, *flagDataPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}
	// A headless node exists to be on the network and has no console to
	// bring it up later, so auto_join being off leaves nothing to run.
	if !netCfg.AutoJoin {
		fmt.Fprintf(os.Stderr,
			"network.auto_join is off in %s, so --headless has nothing to "+
				"serve; set it to true to run a headless node\n",
			*flagConfigPath)
		return 1
	}

	storage := newRuneStorage(*flagDataPath)
	client, _ := newAPIClient(storage, os.TempDir(), rootCfg,
		func(cfg *apiclient.Config) { cfg.OpenBrowser = headlessOpenBrowser })
	defer client.Close()

	if err := headlessLogin(ctx, client, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "login: %s\n", err)
		return 1
	}

	net := newNetwork(rootCfg, *flagDataPath, newNetworkGate(client))
	defer func() {
		_ = net.Close()
	}()

	if err := net.join(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "could not join the network: %s\n", err)
		return 1
	}

	st, err := net.node.Status(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "network status: %s\n", err)
		return 1
	}
	fmt.Fprint(os.Stdout, formatNodeStatus(st))

	waitForShutdownSignal()
	return 0
}

// headlessOpenBrowser keeps the OAuth flow from shelling out to a
// browser that a headless machine does not have. The authorization URL
// is printed on stdout by headlessLogin instead.
func headlessOpenBrowser(*url.URL) error { return nil }

// startHeadlessLogging points the editor log at its configured file and
// tees it to extra, so an operator watching the process sees what the
// log file records. Without a configured log_path the log goes to extra
// only.
func startHeadlessLogging(
	rootCfg config.Config, extra io.Writer,
) (func(), error) {
	level := headlessLogLevel(rootCfg)
	log.SetLevel(level)
	log.SetFormatter(logging.LogrusLogdFormatter{})

	logPath, err := rootCfg.GetString("log_path")
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return nil, fmt.Errorf("read log_path from config: %w", err)
	}
	if logPath == "" {
		log.SetOutput(extra)
		slog.SetDefault(slog.New(slog.NewTextHandler(extra, nil)))
		return func() {}, nil
	}

	f, err := workspace.OpenFile(logPath,
		os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		return nil, fmt.Errorf("open log file %q: %w", logPath, err)
	}
	out := io.MultiWriter(f, extra)
	log.SetOutput(out)
	slog.SetDefault(slog.New(slog.NewTextHandler(out, nil)))
	return func() {
		_ = f.Close()
	}, nil
}

func headlessLogLevel(rootCfg config.Config) log.Level {
	levelStr, err := rootCfg.GetString("log_level")
	if err != nil {
		return log.InfoLevel
	}
	level, err := log.ParseLevel(levelStr)
	if err != nil {
		return log.InfoLevel
	}
	return level
}

// headlessLogin signs the machine in when it is not already, printing
// the authorization URL to out and blocking until the browser flow on
// whatever machine the operator is sitting at completes.
func headlessLogin(
	ctx context.Context, client headlessClient, out io.Writer,
) error {
	user, ok, err := client.AccountStatus(ctx)
	if err != nil {
		return err
	}
	if ok {
		fmt.Fprint(out, formatHeadlessAccount(user))
		return nil
	}

	session := client.Login(ctx)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case u, ok := <-session.URL:
		if ok && u != nil {
			fmt.Fprintf(out,
				"Open this URL in your browser to complete login:\n\n%s\n\n",
				u)
		}
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-session.Done:
		if err != nil {
			return err
		}
	}

	user, ok, err = client.AccountStatus(ctx)
	if err != nil || !ok {
		fmt.Fprint(out, "Login successful.\n")
		return nil
	}
	fmt.Fprint(out, formatHeadlessAccount(user))
	return nil
}

func formatHeadlessAccount(u auth.RPCUser) string {
	var b strings.Builder
	b.WriteString("Signed in")
	if u.Email != "" {
		fmt.Fprintf(&b, " as %s", u.Email)
	}
	fmt.Fprintf(&b, " (%s)\n", headlessPlanLabel(u.Role))
	return b.String()
}

func headlessPlanLabel(role auth.Role) string {
	switch {
	case role == auth.RoleOneOff:
		return "One-off"
	case role >= auth.RolePaid:
		return "Rune Pro"
	default:
		return "Rune"
	}
}

func formatNodeStatus(st runenet.Status) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Network node %s is %s\n", st.Hostname, st.State)
	for _, addr := range st.Addrs {
		fmt.Fprintf(&b, "  address: %s\n", addr)
	}
	if st.LastError != "" {
		fmt.Fprintf(&b, "  last error: %s\n", st.LastError)
	}
	return b.String()
}

// waitForShutdownSignal blocks until the service manager (or the
// operator) asks the node to stop.
func waitForShutdownSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(ch)
	sig := <-ch
	log.Infof("Received %v signal: cleaning up...", sig)
}
