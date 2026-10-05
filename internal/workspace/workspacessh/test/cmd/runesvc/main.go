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

// Command runesvc is a stripped-down stand-in for the `rune` workspace
// server (see cmd/rune/main.go's --workspace-server / -x flag).
//
// It exists solely to give the SSH e2e tests a Linux binary they can
// install as `rune` inside the test container, exercising the full
// connectScheme path (whichCommand + workspaceExists + StartSchemeServer)
// without having to cross-compile the full rune binary (which depends on
// CGO and GUI libraries that don't cross-compile cleanly from a dev
// laptop).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacerpc"
	"unstable.build/rune/internal/workspace/workspacessh"
)

func main() {
	workspacePath := flag.String("x", "",
		"local workspace path to expose over the workspace gRPC server")
	dataDir := flag.String("datadir", "",
		"data directory used by the workspace server")
	flag.Parse()

	if *workspacePath == "" {
		fmt.Fprintln(os.Stderr, "runesvc: -x <workspace-path> is required")
		os.Exit(2)
	}

	uri, err := workspaceapi.ParseURI("file://" + *workspacePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "runesvc: parse uri:", err)
		os.Exit(3)
	}
	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri)
	if err != nil {
		fmt.Fprintln(os.Stderr, "runesvc: new file scheme:", err)
		os.Exit(4)
	}
	defer scheme.Close()

	server := workspacerpc.NewServer(scheme,
		workspacerpc.CommandAuthorizerFunc(
			func(context.Context, workspaceapi.Cmd) error { return nil }))
	defer func() { _ = server.Stop() }()

	// Stand in for a slow pre-serving phase (e.g. a slow config load). The
	// delay happens before StartSchemeServer emits the ServerReady sentinel,
	// so an e2e test can prove connectScheme waits for readiness and does not
	// hand back a client while stdout carries no gRPC server yet.
	sleepServeDelay(*dataDir)

	grpcServer := workspacessh.NewSchemeServer()
	if err := servePackages(grpcServer, *dataDir); err != nil {
		fmt.Fprintln(os.Stderr, "runesvc: serve packages:", err)
		os.Exit(6)
	}
	if err := workspacessh.StartSchemeServer(
		log.New(), server, grpcServer); err != nil {
		fmt.Fprintln(os.Stderr, "runesvc: start scheme server:", err)
		os.Exit(5)
	}
}

// sleepServeDelay blocks for the duration recorded in the remote data
// directory's serve_delay file, if present. It lets an e2e test inject a
// deterministic pre-serving delay without a test-only flag on the production
// connectScheme launch. A missing or malformed file is a no-op.
func sleepServeDelay(dataDir string) {
	if dataDir == "" {
		dataDir = defaultDataDir()
	}
	if dataDir == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(dataDir, "serve_delay"))
	if err != nil {
		return
	}
	d, err := time.ParseDuration(strings.TrimSpace(string(data)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "runesvc: parse serve_delay: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "runesvc: delaying serve by %s\n", d)
	time.Sleep(d)
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		if u, uerr := user.Current(); uerr == nil {
			home = u.HomeDir
		}
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".rune")
}
