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

package idescavenger

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/retry"
	"unstable.build/rune/internal/debug"
)

const (
	// partitionName is the sub-partition the Cleaner owns. It holds
	// nothing but the tracking document and the seed marker.
	partitionName = "scavenger"

	trackedDocumentID   = "workspaces"
	trackedDocumentKind = "scavenger-workspaces"

	// The seed marker has its own document: an older IDE process
	// rewriting the tracking document whole must not erase it.
	seedDocumentID   = "seed"
	seedDocumentKind = "scavenger-seed"

	fileScheme = "file"
)

// WorkspaceHook reclaims the storage a workspace left behind. Hooks are
// only invoked for workspaces whose root directory no longer exists, and
// must be idempotent: a hook that fails is retried on the next pass.
type WorkspaceHook func(ctx context.Context, cwd workspaceapi.URI) error

// Config configures a Cleaner.
type Config struct {
	// Storage is the IDE storage. The Cleaner partitions its own
	// dataset out of it and does not take ownership of it.
	Storage storageapi.Service

	// OpenWorkspaces returns the workspaces that are currently open. A
	// pass that cannot determine them is abandoned rather than risking
	// the storage of a live workspace. Optional: when nil, no workspace
	// is considered open.
	OpenWorkspaces func(ctx context.Context) ([]workspaceapi.URI, error)

	// Seed lists the workspaces that predate the Cleaner, typically
	// recovered from another IDE dataset. That listing decodes every
	// document in the dataset, so the pass Start runs consults it once
	// per dataset, off the caller's goroutine, and records that it
	// did. Optional.
	Seed func(ctx context.Context) ([]workspaceapi.URI, error)

	// Stat resolves a workspace root. Defaults to os.Stat.
	Stat func(name string) (fs.FileInfo, error)

	// StartRetry bounds the retries of the pass Start runs. Defaults
	// to an exponential backoff giving up after about two minutes,
	// enough to outlast the startup window in which the event loop
	// does not accept work yet.
	StartRetry retry.Strategy
}

// Cleaner reclaims the storage of workspaces that no longer exist on
// disk. It is safe for concurrent use.
type Cleaner struct {
	storage        storageapi.Service
	openWorkspaces func(ctx context.Context) ([]workspaceapi.URI, error)
	seed           func(ctx context.Context) ([]workspaceapi.URI, error)
	stat           func(name string) (fs.FileInfo, error)
	startRetry     retry.Strategy

	mu    sync.Mutex
	hooks []WorkspaceHook
	stop  context.CancelFunc
}

// New returns a Cleaner that tracks workspaces in its own partition of
// cfg.Storage.
func New(cfg Config) (*Cleaner, error) {
	if cfg.Storage == nil {
		return nil, errors.New("idescavenger: storage is required")
	}
	storage, err := cfg.Storage.Partition(partitionName)
	if err != nil {
		return nil, fmt.Errorf("idescavenger: partition storage: %w", err)
	}
	stat := cfg.Stat
	if stat == nil {
		stat = os.Stat
	}
	startRetry := cfg.StartRetry
	if startRetry == nil {
		startRetry = retry.CombinedStrategy(retry.LimitStrategy(8),
			retry.ExponentialStrategy(time.Second, 30*time.Second))
	}
	return &Cleaner{
		storage:        storage,
		openWorkspaces: cfg.OpenWorkspaces,
		seed:           cfg.Seed,
		stat:           stat,
		startRetry:     startRetry,
	}, nil
}

// AddWorkspaceHook registers a hook run against every workspace whose
// root has disappeared. A workspace stops being tracked once all of its
// hooks have succeeded, so hooks must be registered before Start.
func (c *Cleaner) AddWorkspaceHook(hook WorkspaceHook) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hooks = append(c.hooks, hook)
}

// RegisterNewWorkspace starts tracking uri. It is idempotent.
func (c *Cleaner) RegisterNewWorkspace(
	ctx context.Context, uri workspaceapi.URI,
) error {
	return c.register(ctx, uri)
}

// Start seeds the dataset if that has not happened yet, then runs a
// single pass, in the background and retrying a failure with the
// configured backoff: the pass first runs while the IDE is still
// starting up, when the event loop does not accept work yet and the
// open workspaces cannot be determined, and the seed listing can be
// refused by a storage another process leads. Close stops the retries.
func (c *Cleaner) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.stop = cancel
	c.mu.Unlock()
	go debug.CapturePanicReport(func() {
		defer cancel()
		for attempt := uint(1); ; attempt++ {
			err := c.seedOnce(ctx)
			if err == nil {
				err = c.RunOnce(ctx)
			}
			if err == nil || ctx.Err() != nil {
				return
			}
			sleep, stop := c.startRetry(attempt)
			if stop {
				logger().Errorf("scavenge workspaces: %v", err)
				return
			}
			logger().Debugf("scavenge workspaces (attempt %d): %v",
				attempt, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(sleep):
			}
		}
	})
}

// seedOnce recovers the workspaces that predate the Cleaner unless a
// previous launch already did. The marker is only written once the
// listing and the registration both succeeded, so a refused listing is
// retried on the next launch.
func (c *Cleaner) seedOnce(ctx context.Context) error {
	if c.seed == nil {
		return nil
	}
	var marker seedDocument
	err := c.storage.Get(ctx, seedDocumentID, &marker)
	if err == nil {
		return nil
	}
	if !errors.Is(err, storageapi.ErrNotFound) {
		return fmt.Errorf("idescavenger: load seed marker: %w", err)
	}
	uris, err := c.seed(ctx)
	if err != nil {
		return fmt.Errorf("idescavenger: seed: %w", err)
	}
	if err := c.register(ctx, uris...); err != nil {
		return err
	}
	marker = seedDocument{Kind: seedDocumentKind, SeededAt: time.Now()}
	if err := c.storage.Set(ctx, seedDocumentID, &marker); err != nil {
		return fmt.Errorf("idescavenger: store seed marker: %w", err)
	}
	return nil
}

// RunOnce drops the storage of every tracked workspace whose root
// directory no longer exists.
func (c *Cleaner) RunOnce(ctx context.Context) error {
	tracked, err := c.load(ctx)
	if err != nil {
		return err
	}
	if len(tracked.URIs) == 0 {
		return nil
	}

	open, err := c.openWorkspaceSet(ctx)
	if err != nil {
		return err
	}

	c.mu.Lock()
	hooks := append([]WorkspaceHook(nil), c.hooks...)
	c.mu.Unlock()

	remaining := make([]string, 0, len(tracked.URIs))
	for _, raw := range tracked.URIs {
		keep, err := c.scavenge(ctx, hooks, open, raw)
		if err != nil {
			return err
		}
		if keep {
			remaining = append(remaining, raw)
		}
	}
	if len(remaining) == len(tracked.URIs) {
		return nil
	}
	return c.store(ctx, remaining)
}

// scavenge reports whether raw must stay tracked.
func (c *Cleaner) scavenge(
	ctx context.Context, hooks []WorkspaceHook,
	open map[string]struct{}, raw string,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return true, err
	}
	uri, err := workspaceapi.ParseURI(raw)
	if err != nil {
		// unparseable entries can never be acted upon
		return false, nil
	}
	if _, isOpen := open[raw]; isOpen {
		return true, nil
	}
	// only local roots can be told apart from an unreachable host
	if uri.Scheme() != fileScheme {
		return true, nil
	}
	// a stat error other than "not there" (a permission problem, an
	// unmounted volume) says nothing about the workspace being gone
	if _, err := c.stat(uri.Path()); !errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}

	for _, hook := range hooks {
		if err := hook(ctx, uri); err != nil {
			logger().Errorf("scavenge workspace %q: %v", raw, err)
			return true, nil
		}
	}
	logger().Debugf("scavenged storage of removed workspace %q", raw)
	return false, nil
}

func (c *Cleaner) openWorkspaceSet(
	ctx context.Context,
) (map[string]struct{}, error) {
	if c.openWorkspaces == nil {
		return nil, nil
	}
	uris, err := c.openWorkspaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("idescavenger: open workspaces: %w", err)
	}
	open := make(map[string]struct{}, len(uris))
	for _, uri := range uris {
		open[uri.String()] = struct{}{}
	}
	return open, nil
}

// register adds uris to the tracking document. The read-modify-write is
// only guarded within this process: a registration lost to another IDE
// process is recovered the next time that workspace is opened.
func (c *Cleaner) register(
	ctx context.Context, uris ...workspaceapi.URI,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	tracked, err := c.load(ctx)
	if err != nil {
		return err
	}
	known := make(map[string]struct{}, len(tracked.URIs))
	for _, raw := range tracked.URIs {
		known[raw] = struct{}{}
	}

	added := false
	for _, uri := range uris {
		// remote roots cannot be distinguished from an unreachable
		// host, so they are never scavenged and never tracked
		if uri.Scheme() != fileScheme {
			continue
		}
		raw := uri.String()
		if _, ok := known[raw]; ok {
			continue
		}
		known[raw] = struct{}{}
		tracked.URIs = append(tracked.URIs, raw)
		added = true
	}
	if !added {
		return nil
	}
	return c.store(ctx, tracked.URIs)
}

type trackedWorkspaces struct {
	Kind string
	URIs []string
}

type seedDocument struct {
	Kind     string
	SeededAt time.Time
}

func (c *Cleaner) load(ctx context.Context) (trackedWorkspaces, error) {
	var doc trackedWorkspaces
	err := c.storage.Get(ctx, trackedDocumentID, &doc)
	if errors.Is(err, storageapi.ErrNotFound) {
		return trackedWorkspaces{}, nil
	}
	if err != nil {
		return trackedWorkspaces{}, fmt.Errorf(
			"idescavenger: load tracked workspaces: %w", err)
	}
	return doc, nil
}

func (c *Cleaner) store(ctx context.Context, uris []string) error {
	doc := trackedWorkspaces{Kind: trackedDocumentKind, URIs: uris}
	if err := c.storage.Set(ctx, trackedDocumentID, &doc); err != nil {
		return fmt.Errorf("idescavenger: store tracked workspaces: %w", err)
	}
	return nil
}

// Close stops the pass Start may still be retrying and releases the
// partition handle owned by this Cleaner.
func (c *Cleaner) Close() error {
	c.mu.Lock()
	stop := c.stop
	c.stop = nil
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
	return c.storage.Close()
}

func logger() *log.Entry {
	return log.WithField(logging.KeyClass, "ide.idescavenger")
}
