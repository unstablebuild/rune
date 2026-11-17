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

package workspacetest

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

var _ schemeapi.Scheme = (*PtyScheme)(nil)

// PtyScheme is a scheme whose ptys and commands a [PtyExecutor] runs:
// what a host serves to other machines, as far as terminals are
// concerned. Each root chrooted is a PtyScheme of its own, with an
// executor of its own, so a test can tell which root a terminal was
// made in. Its files do nothing; see [NopScheme].
type PtyScheme struct {
	*NopScheme
	*PtyExecutor
	root string
	// Foreground is the process group in the foreground of its ptys,
	// as the host of the ptys tells it. Zero is none: the executor's
	// answer, which cannot tell.
	Foreground atomic.Int64
	// Missing are the paths Chroot refuses with os.ErrNotExist.
	Missing map[string]bool

	mu      sync.Mutex
	chroots map[string]*PtyScheme
	closed  bool
}

// NewPtyScheme returns a PtyScheme rooted at root.
func NewPtyScheme(root string) *PtyScheme {
	nop, _ := NewNopScheme("file")(context.Background(), nil, workspaceapi.URI{})
	return &PtyScheme{
		NopScheme:   nop.(*NopScheme),
		PtyExecutor: NewPtyExecutor(),
		root:        root,
		chroots:     make(map[string]*PtyScheme),
	}
}

// Root satisfies schemeapi.Scheme.
func (s *PtyScheme) Root() string { return s.root }

// Chroot satisfies schemeapi.Scheme. The same path is the same scheme.
func (s *PtyScheme) Chroot(path string) (schemeapi.Scheme, error) {
	if s.Missing[path] {
		return nil, os.ErrNotExist
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.chroots[path]
	if !ok {
		sub = NewPtyScheme(path)
		s.chroots[path] = sub
	}
	return sub, nil
}

// Chrooted is the scheme Chroot made for path, nil when it made none.
func (s *PtyScheme) Chrooted(path string) *PtyScheme {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chroots[path]
}

// Close satisfies schemeapi.Scheme.
func (s *PtyScheme) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// Closed reports whether Close was called.
func (s *PtyScheme) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// NewPty satisfies schemeapi.Terminal.
func (s *PtyScheme) NewPty(ctx context.Context) (workspaceapi.Pty, error) {
	return s.PtyExecutor.NewPty(ctx)
}

// SetPtySize satisfies schemeapi.Terminal.
func (s *PtyScheme) SetPtySize(p workspaceapi.Pty, size workspaceapi.PtySize) error {
	return s.PtyExecutor.SetPtySize(p, size)
}

// ForegroundProcessGroup satisfies schemeapi.Terminal; see Foreground.
func (s *PtyScheme) ForegroundProcessGroup(p workspaceapi.Pty) (int, error) {
	if fg := s.Foreground.Load(); fg != 0 {
		return int(fg), nil
	}
	return s.PtyExecutor.ForegroundProcessGroup(p)
}

// StartCommand satisfies schemeapi.Executor.
func (s *PtyScheme) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	return s.PtyExecutor.StartCommand(ctx, cmd)
}

// Signal satisfies schemeapi.Executor.
func (s *PtyScheme) Signal(pid workspaceapi.Pid, sig syscall.Signal) error {
	return s.PtyExecutor.Signal(pid, sig)
}

// LookPath satisfies schemeapi.Executor.
func (s *PtyScheme) LookPath(ctx context.Context, name, dir string) (string, error) {
	return s.PtyExecutor.LookPath(ctx, name, dir)
}
