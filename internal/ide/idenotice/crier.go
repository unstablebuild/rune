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

package idenotice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"unstable.build/rune/internal/component/markdown"
	mdhandler "unstable.build/rune/internal/handler/markdown"
)

// fileSystem is the subset of workspace.Workspace that the Crier
// needs. Narrowed for testability.
type fileSystem interface {
	OpenFile(name string, flag int, perm os.FileMode) (workspaceapi.File, error)
}

// Crier resolves the configured workspace notice and opens it in a
// floating window.
type Crier struct {
	fs               fileSystem
	wm               browserapi.WindowManager
	parser           syntaxapi.Parser
	scheduleNextTick func(func()) bool
	onLinkClick      func(*url.URL) bool
	cfg              Config
	store            *store
}

// New returns a Crier that loads the notice via fs and displays it
// through wm. parser and scheduleNextTick are forwarded to the
// markdown component so fenced code blocks get asynchronous syntax
// highlighting. onLinkClick is invoked when the user clicks a link
// in the rendered notice; returning true suppresses the markdown
// handler's default same-page anchor behavior.
func New(
	fs fileSystem, wm browserapi.WindowManager,
	parser syntaxapi.Parser, scheduleNextTick func(func()) bool,
	onLinkClick func(*url.URL) bool,
	cfg Config,
) *Crier {
	return &Crier{
		fs:               fs,
		wm:               wm,
		parser:           parser,
		scheduleNextTick: scheduleNextTick,
		onLinkClick:      onLinkClick,
		cfg:              cfg,
		store:            newStore(cfg.Storage),
	}
}

// Show resolves the configured notice and opens a floating window.
// It is a no-op when no notice is configured, and under ShowOnce
// it is also a no-op when the same fingerprint was already shown.
func (c *Crier) Show(ctx context.Context) error {
	content, err := c.resolveContent()
	if err != nil {
		return err
	}
	if content == "" {
		return nil
	}
	fp := fingerprint(content)
	uri := c.cfg.WorkspaceURI.String()
	if c.cfg.effectiveShow() == ShowOnce {
		shown, err := c.store.Shown(ctx, uri, fp)
		if err != nil {
			return err
		}
		if shown {
			return nil
		}
	}
	if err := c.openFloating(content); err != nil {
		return err
	}
	if c.cfg.effectiveShow() == ShowOnce {
		if err := c.store.MarkShown(ctx, uri, fp); err != nil {
			return err
		}
	}
	return nil
}

func (c *Crier) resolveContent() (string, error) {
	if c.cfg.Literal != "" {
		return c.cfg.Literal, nil
	}
	if c.cfg.Path == "" {
		return "", nil
	}
	f, err := c.fs.OpenFile(c.cfg.Path, os.O_RDONLY, 0)
	if err != nil {
		return "", fmt.Errorf("idenotice: open %q: %w", c.cfg.Path, err)
	}
	defer f.Close() //nolint:errcheck
	b, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("idenotice: read %q: %w", c.cfg.Path, err)
	}
	return string(b), nil
}

func fingerprint(content string) string {
	h := sha256.New()
	h.Write([]byte(content))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func (c *Crier) openFloating(content string) error {
	inner, err := buildNoticeHandler(content,
		c.parser, c.scheduleNextTick, c.onLinkClick)
	if err != nil {
		return err
	}
	_, err = c.wm.Floating(browserapi.NopFloatingHandler(inner), browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	})
	if err != nil {
		return fmt.Errorf("idenotice: open floating: %w", err)
	}
	return nil
}

func buildNoticeHandler(
	content string,
	parser syntaxapi.Parser, scheduleNextTick func(func()) bool,
	onLinkClick func(*url.URL) bool,
) (*handler.Span, error) {
	mcfg := markdown.DefaultConfig()
	mcfg.HeaderPrefix = false
	mcfg.Parser = parser
	mcfg.ScheduleNextTick = scheduleNextTick
	md, err := markdown.NewWithConfig(content, mcfg)
	if err != nil {
		return nil, fmt.Errorf("idenotice: parse markdown: %w", err)
	}
	mdh := mdhandler.New(md, mdhandler.WithOnLinkClick(onLinkClick))
	return handler.NewSpan(mdh, component.SpanConfig{
		PadHorizontal:    2,
		ContentAlignment: component.AlignmentCentered,
	}), nil
}
