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

package idelsp

import (
	"context"
	"errors"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
)

// multiLangServer multiplexes one language id across several
// langServers. children[0] is the default backend that handles any
// method without an explicit route and whose initialize result is
// reported for the language. routes maps a request method to the
// index of the child that owns it; requests have a single response so
// they go to exactly one child. Notifications are fire-and-forget and
// are always fanned out to every child so each backend keeps a
// consistent view of documents and workspace state.
type multiLangServer struct {
	cfg      langConfig
	children []server
	routes   map[string]int
	mu       sync.Mutex
	init     semanticapi.InitializeResult
}

var _ server = (*multiLangServer)(nil)

func (m *multiLangServer) childFor(method string) server {
	m.mu.Lock()
	defer m.mu.Unlock()
	if idx, ok := m.routes[method]; ok && idx >= 0 && idx < len(m.children) {
		return m.children[idx]
	}
	return m.children[0]
}

func (m *multiLangServer) allChildren() []server {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]server, len(m.children))
	copy(out, m.children)
	return out
}

// replaceChild swaps old for replacement by pointer identity. It is
// used by the manager's restart supervisor when a single child
// crashes and is re-spawned, leaving the other children untouched.
// It reports whether old was found.
func (m *multiLangServer) replaceChild(old, replacement server) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, c := range m.children {
		if c == old {
			m.children[i] = replacement
			return true
		}
	}
	return false
}

func (m *multiLangServer) call(
	ctx context.Context, method string, params, result any,
) error {
	return m.childFor(method).call(ctx, method, params, result)
}

// pullDiagnostics fans a textDocument/diagnostic pull out to every
// child and merges their reports so a single pull returns findings
// from all backends (e.g. ty type errors and ruff lint), not just the
// default child's. A child that does not support the pull is skipped;
// the call fails only if every child fails.
func (m *multiLangServer) pullDiagnostics(
	ctx context.Context, params semanticapi.DocumentDiagnosticParams,
) (semanticapi.DocumentDiagnosticReport, error) {
	children := m.allChildren()
	merged := semanticapi.DocumentDiagnosticReport{Kind: "full"}
	var errs []error
	var succeeded bool
	for _, c := range children {
		report, err := c.pullDiagnostics(ctx, params)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		succeeded = true
		merged.Items = append(merged.Items, report.Items...)
	}
	if !succeeded && len(errs) > 0 {
		return semanticapi.DocumentDiagnosticReport{}, errors.Join(errs...)
	}
	return merged, nil
}

func (m *multiLangServer) notify(
	ctx context.Context, method string, params any,
) error {
	var errs []error
	for _, c := range m.allChildren() {
		if err := c.notify(ctx, method, params); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *multiLangServer) initialize(ctx context.Context) (
	semanticapi.InitializeResult, error,
) {
	var errs []error
	children := m.allChildren()
	for _, c := range children {
		if _, err := c.initialize(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return semanticapi.InitializeResult{}, err
	}
	res := children[0].initResult()
	m.mu.Lock()
	m.init = res
	m.mu.Unlock()
	return res, nil
}

func (m *multiLangServer) start(ctx context.Context) error {
	// start owns the lifecycle of every child it brings up: on the first
	// child that fails, close the ones already started and return the
	// error. A failed langServer.start has already cleaned up after itself,
	// so it must not be Closed again. This keeps the contract that a caller
	// only Closes a multiLangServer whose start returned nil.
	children := m.allChildren()
	for i, c := range children {
		if err := c.start(ctx); err != nil {
			for _, started := range children[:i] {
				_ = started.Close()
			}
			return err
		}
	}
	m.mu.Lock()
	m.init = children[0].initResult()
	m.mu.Unlock()
	return nil
}

func (m *multiLangServer) stop(ctx context.Context) error {
	var errs []error
	for _, c := range m.allChildren() {
		if err := c.stop(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *multiLangServer) Close() error {
	var errs []error
	for _, c := range m.allChildren() {
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *multiLangServer) config() langConfig {
	return m.cfg
}

func (m *multiLangServer) key() serverKey {
	return m.children[0].key()
}

// name returns the default child's name so a multi-server is
// addressable by ServerID via its primary backend.
func (m *multiLangServer) name() string {
	return m.children[0].name()
}

func (m *multiLangServer) initResult() semanticapi.InitializeResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.init
}

// supportsPullDiagnostics follows the default child, whose initialize
// result already defines the language's reported capabilities.
func (m *multiLangServer) supportsPullDiagnostics() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.children[0].supportsPullDiagnostics()
}

func (m *multiLangServer) isAlive() bool {
	for _, c := range m.allChildren() {
		if c.isAlive() {
			return true
		}
	}
	return false
}
