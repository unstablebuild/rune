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

// Package langexttest provides fakes for testing language extensions
// built on langext.
package langexttest

import (
	"context"
	"os"
	"sync/atomic"

	"github.com/unstablebuild/rune-go-sdk/api/pkgapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

// Installer is a langext.Installer for a workspace host whose package
// holds Files, whichever package is asked for. Listing the package fails
// with Err when it is set. Provisioned maps the tools found under the
// install root, which a Rune without the packages service falls back to;
// probing anything else there fails with ProbeErr, or os.ErrNotExist
// when it is nil. It must be used through a pointer so it can count.
type Installer struct {
	Files       []string
	Err         error
	Provisioned map[string]string
	ProbeErr    error

	lookups atomic.Int32
	listed  atomic.Int32
	closed  atomic.Int32
}

// Packages satisfies langext.Installer.
func (i *Installer) Packages(context.Context) pkgapi.Manager { return (*packages)(i) }

// FindInstalledExecutable satisfies langext.Installer.
func (i *Installer) FindInstalledExecutable(_ context.Context, name string) (string, error) {
	if p, ok := i.Provisioned[name]; ok {
		return p, nil
	}
	if i.ProbeErr != nil {
		return "", i.ProbeErr
	}
	return "", os.ErrNotExist
}

// Lookups reports how many times a package was listed, each of which
// could have asked the user to install it.
func (i *Installer) Lookups() int { return int(i.lookups.Load()) }

// Listed reports how many files were handed out across every listing.
func (i *Installer) Listed() int { return int(i.listed.Load()) }

// Closed reports how many listings were closed.
func (i *Installer) Closed() int { return int(i.closed.Load()) }

type packages Installer

func (p *packages) LibDir(context.Context, string) (iterator.Iterator[string], error) {
	p.lookups.Add(1)
	if p.Err != nil {
		return nil, p.Err
	}
	files := p.Files
	return iterator.FromFunc(func(context.Context) (string, bool, error) {
		if len(files) == 0 {
			return "", false, nil
		}
		p.listed.Add(1)
		f := files[0]
		files = files[1:]
		return f, true, nil
	}, func() error {
		p.closed.Add(1)
		return nil
	}), nil
}
