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

package syntaxtest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/syntax/grammarfixture"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/vi"
	"unstable.build/rune/internal/workspace"
)

var ctx = context.Background()

func TestCommandsIntegration(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "")
	require.NoError(t, err)
	path := filepath.Join(dir, "fai.go")
	f, err := os.Create(path)
	require.NoError(t, err)
	_, err = f.WriteString(fileContent)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})

	uri, err := workspaceapi.CurrentUserHostURI(f.Name())
	require.NoError(t, err)

	var mu sync.Mutex
	var wg sync.WaitGroup
	scheduleNextTick := func(fn func()) bool {
		defer wg.Done()
		mu.Lock()
		defer mu.Unlock()
		fn()
		return true
	}

	mu.Lock()
	const width, height = 30, 10
	c, pkg := newTestComponentWithFile(t, dir, uri, scheduleNextTick)
	c.Resize(width, height)
	mu.Unlock()

	wg.Add(1)
	it, _, err := c.CompleteCommand(context.Background(), textapi.Command{
		Name:   "jumptoast",
		Args:   []string{},
		Window: c.Browser().Focus(),
		URI:    uri,
	})
	require.NoError(t, err)
	items, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{"locals.scm"}, items)

	it, _, err = c.CompleteCommand(context.Background(), textapi.Command{
		Name:   "jumptoast",
		Args:   []string{"locals.scm", ""},
		Window: c.Browser().Focus(),
		URI:    uri,
	})
	require.NoError(t, err)
	items, err = iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		"local.definition.function",
		"local.definition.var",
		"local.scope",
		"local.definition.namespace",
		"local.reference",
	}, items)

	it, _, err = c.CompleteCommand(context.Background(), textapi.Command{
		Name:   "jumptoast",
		Args:   []string{"locals.scm", "local.definition.namespace", ""},
		Window: c.Browser().Focus(),
		URI:    uri,
	})
	require.NoError(t, err)
	items, err = iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		"'package main'",
	}, items)

	_, err = c.DispatchCommand(ctx, textapi.Command{
		Name:   "jumptoast",
		Args:   []string{"NONEXISTENT", "local.definition.namespace", "package main"},
		Window: c.Browser().Focus(),
		URI:    uri,
	})
	require.True(t, strings.Contains(err.Error(), "NONEXISTENT"))

	_, err = c.DispatchCommand(ctx, textapi.Command{
		Name:   "jumptoast",
		Args:   []string{"locals.scm", "NONEXISTENT", "package main"},
		Window: c.Browser().Focus(),
		URI:    uri,
	})
	require.EqualError(t, err, "1 error occurred: capture name 'NONEXISTENT' does not exist in query")

	_, err = c.DispatchCommand(ctx, textapi.Command{
		Name:   "jumptoast",
		Args:   []string{"locals.scm", "local.definition.namespace", "NONEXISTENT"},
		Window: c.Browser().Focus(),
		URI:    uri,
	})
	require.EqualError(t, err, "could not find matching node")

	// test that we actually jump
	tests := []comptest.TestCase{
		{Action: func() {
			handled, err := c.DispatchCommand(ctx, textapi.Command{
				Name:   "jumptoast",
				Args:   []string{"locals.scm", "local.definition.var", "const fileContent = \"package main\\n\" +"},
				Window: c.Browser().Focus(),
				URI:    uri,
			})
			require.NoError(t, err)
			assert.True(t, handled)
		}, Expected: `
┌━━━━━━━━────────────────────┐
│o fai.go                    │
├────────────────────────────┤
│    for i := 0; i < 10; i++ │
│        fmt.Println("%d", i)│
│    }                       │
│}                           │
│                            │
│const fileContent = "package│
└────────────────────────────┘`,
		},
	}

	w := term.NewStringWriter(width, height)
	comptest.TestComponent(t, component.Sync(&mu, c), w, tests)

	// test re-open re-register
	mu.Lock()
	wg.Add(1)
	c.Browser().RemoveWindowContent(c.Browser().Focus())
	pkg.ret = iterator.FromSlice([]string{ // re-hydrate files iterator
		grammarfixture.Parser("go"),
		"go/highlights.scm",
		"go/indents.scm",
		"go/folds.scm",
		"go/locals.scm",
	})
	_, err = c.OpenFileTab(uri, false)
	require.NoError(t, err)
	require.True(t, c.Browser().PreviousTab(c.Browser().Focus()))
	mu.Unlock()
	wg.Wait()

	it, _, err = c.CompleteCommand(context.Background(), textapi.Command{
		Name:   "jumptoast",
		Args:   []string{"locals.scm", "local.definition.namespace", ""},
		Window: c.Browser().Focus(),
		URI:    uri,
	})
	require.NoError(t, err)
	items, err = iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		"'package main'",
	}, items)

	tests = []comptest.TestCase{
		{Action: func() {
		}, Expected: `
┌━━━━━━━━────────────────────┐
│o fai.go                    │
├────────────────────────────┤
│package main                │
│                            │
│import (                    │
│    "fmt"                   │
│                            │
│    "github.com/unstablebuil│
└────────────────────────────┘`,
		},
		{Action: func() {
			handled, err := c.DispatchCommand(ctx, textapi.Command{
				Name:   "jumptoast",
				Args:   []string{"locals.scm", "local.definition.var", "const fileContent = \"package main\\n\" +"},
				Window: c.Browser().Focus(),
				URI:    uri,
			})
			require.NoError(t, err)
			require.True(t, handled)
		}, Expected: `
┌━━━━━━━━────────────────────┐
│o fai.go                    │
├────────────────────────────┤
│    for i := 0; i < 10; i++ │
│        fmt.Println("%d", i)│
│    }                       │
│}                           │
│                            │
│const fileContent = "package│
└────────────────────────────┘`,
		},
	}

	comptest.TestComponent(t, component.Sync(&mu, c), w, tests)
	require.NoError(t, c.Close())
}

func newTestComponentWithFile(
	t *testing.T, dir string, filename workspaceapi.URI,
	scheduleNextTick func(func()) bool,
) (*text.Component, *mockPkgManager) {
	cfg := text.DefaultConfig()
	cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
	pkg := newInstalledPkgManager(t)
	cfg.PkgManager = pkg
	cfg.NoMaxSize = false
	cfg.Syntax.ScheduleNextTick = scheduleNextTick
	cfg.SyntaxTree = treesitter.New
	uri, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	loader := workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule)
	c, err := text.NewComponent(vi.Editor(), loader, cfg)
	require.NoError(t, err)
	_, err = c.OpenFileTab(filename, false)
	require.NoError(t, err)
	c.Browser().PreviousTab(c.Browser().Focus())
	return c, pkg
}

// inlineSchedule is a synchronous workspace.ScheduleNextTick stub
// that runs fn on the calling goroutine. Test-only: production code
// must use the host event-loop scheduler so reload's buffer
// mutations do not run on a worker goroutine.
func inlineSchedule(fn func()) bool {
	fn()
	return true
}
