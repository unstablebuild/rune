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

package workspacerune

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi/workspacerpc"
	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/runenet"
)

// prefix is what a user must type before peer names are offered.
const prefix = Scheme + "://"

// peerDirTimeout bounds the round-trip a directory completion makes to
// a peer, so completing against a machine that is asleep or gone does
// not hold a mesh connection open until the prompt closes.
const peerDirTimeout = 5 * time.Second

// PeerLister enumerates the machines in the mesh. It is satisfied by
// [runenet.Node].
type PeerLister interface {
	Peers(ctx context.Context) ([]runenet.Peer, error)
}

// Mesh is what completing a rune:// URI needs: the machines to offer
// while a hostname is being typed, and a connection to the chosen one
// to list its directories. It is satisfied by [runenet.Node].
type Mesh interface {
	PeerLister
	Dialer
}

// Completer completes rune:// workspace URIs: first with the machines
// currently connected to the mesh, so opening a workspace elsewhere is
// a pick rather than a hostname the user has to remember, then with
// the directories of the chosen machine, which only that machine can
// enumerate. Offline peers are left out: they cannot serve the
// workspace being opened.
//
// It panics if mesh is nil: completion is wired from the same node the
// scheme dials through, so a missing one is a programming error.
func Completer(mesh Mesh) command.Completer {
	if mesh == nil {
		panic("workspacerune.Completer: mesh is required")
	}
	return command.FuncCompleter(func(
		ctx context.Context, args []string,
	) (iterator.Iterator[string], string, error) {
		var last string
		if len(args) > 1 {
			last = args[len(args)-1]
		}
		// Only contribute once the user has committed to the scheme;
		// otherwise every workspaceopen completion would be padded
		// with peers the user is not asking for.
		if !strings.HasPrefix(last, prefix) {
			return iterator.Empty[string](), "", nil
		}
		peer, typedPath, chosen := strings.Cut(
			strings.TrimPrefix(last, prefix), "/")
		if !chosen {
			return deferred(func(ctx context.Context) ([]string, error) {
				return matchingPeers(ctx, mesh, peer)
			}), "", nil
		}
		return deferred(func(ctx context.Context) ([]string, error) {
			return peerDirs(ctx, mesh, peer, "/"+typedPath)
		}), "", nil
	})
}

// deferred keeps the mesh round-trip out of Complete, which runs on the
// editor's event loop; the prompt drains the iterator off it and
// cancels ctx as soon as the user types again.
func deferred(
	fetch func(context.Context) ([]string, error),
) iterator.Iterator[string] {
	var (
		results  []string
		fetched  bool
		fetchErr error
		idx      int
	)
	return iterator.FromFunc(func(ctx context.Context) (string, bool, error) {
		if !fetched {
			results, fetchErr = fetch(ctx)
			fetched = true
		}
		if fetchErr != nil {
			return "", false, fetchErr
		}
		if idx >= len(results) {
			return "", false, nil
		}
		ret := results[idx]
		idx++
		return ret, true, nil
	}, func() error { return nil })
}

func matchingPeers(
	ctx context.Context, peers PeerLister, typed string,
) ([]string, error) {
	found, err := peers.Peers(ctx)
	if err != nil {
		return nil, err
	}
	ret := make([]string, 0, len(found))
	for _, p := range found {
		if !p.Online || !strings.HasPrefix(p.Hostname, typed) {
			continue
		}
		ret = append(ret, prefix+p.Hostname+"/")
	}
	return ret, nil
}

// peerDirs offers the directories of typed's parent on peer. Only
// directories are offered because a workspace is a directory, and the
// completions carry a trailing separator so the next keystroke keeps
// descending instead of re-listing what was just picked.
func peerDirs(
	ctx context.Context, dialer Dialer, peer, typed string,
) ([]string, error) {
	dir, base := path.Split(typed)
	entries, err := readPeerDir(ctx, dialer, peer, dir)
	if err != nil {
		return nil, err
	}
	ret := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !strings.HasPrefix(name, base) {
			continue
		}
		// Hidden directories stay out of the way until the user asks
		// for them, as they do in local path completion.
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		ret = append(ret, prefix+peer+path.Join(dir, name)+"/")
	}
	return ret, nil
}

// readPeerDir lists dir on peer over a connection of its own: a
// completion is not tied to any open workspace, and the peer serves
// its whole filesystem, so no chroot is needed to reach an absolute
// path.
func readPeerDir(
	ctx context.Context, dialer Dialer, peer, dir string,
) ([]fs.DirEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, peerDirTimeout)
	defer cancel()

	conn, err := dialPeer(dialer, peer)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", peer, err)
	}
	defer func() { _ = conn.Close() }()

	client := workspacerpc.NewClient(ctx, conn)
	defer func() { _ = client.Close() }()
	entries, err := client.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s on %s: %w", dir, peer, err)
	}
	return entries, nil
}
