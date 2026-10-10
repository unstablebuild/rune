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
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"go.uber.org/goleak"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component/notifications"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/ide/syntax/grammarfixture"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/text/vi"
	"unstable.build/rune/internal/workspace"
)

func TestTreeFoldsIntegration(t *testing.T) {
	t.Run("first call to Folds waits for language and parser to be initialized", func(t *testing.T) {
		tree, cleanup := newTree(t)

		it, ok := tree.Folds()
		require.True(t, ok)

		actual, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)

		expected := []term.Range{
			{
				Start: term.Coordinates{Y: 2},
				End:   term.Coordinates{X: 1, Y: 6},
			},
			{
				Start: term.Coordinates{Y: 2},
				End:   term.Coordinates{X: 1, Y: 6},
			},
			{
				Start: term.Coordinates{Y: 8},
				End:   term.Coordinates{X: 1, Y: 13},
			},
			{
				Start: term.Coordinates{X: 12, Y: 8},
				End:   term.Coordinates{X: 1, Y: 13},
			},
			{
				Start: term.Coordinates{X: 1, Y: 10},
				End:   term.Coordinates{X: 2, Y: 12},
			},
			{
				Start: term.Coordinates{X: 25, Y: 10},
				End:   term.Coordinates{X: 2, Y: 12},
			},
			{
				Start: term.Coordinates{X: 0, Y: 15},
				End:   term.Coordinates{X: 42, Y: 19},
			},
		}
		assert.ElementsMatch(t, expected, actual)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("if folds.scm file is not found and tree is NOT ready, the iterators are eventually empty", func(t *testing.T) {
		rootPkgs := newInstalledPkgManagerWithFiles(t,
			"go/tree-sitter.so",
			"go/highlights.scm",
			"go/indents.scm",
		)
		var mu sync.Mutex
		libDirIt := iterator.FromFunc(
			func(ctx context.Context) (string, bool, error) {
				mu.Lock()
				defer mu.Unlock()
				val, ok := rootPkgs.ret.Next(ctx)
				return val, ok, nil
			},
			func() error {
				return nil
			},
		)

		pkgs := &mockPkgManager{ret: libDirIt}
		ready := func(context.Context) error {
			return nil
		}
		const width, height = 30, 15
		tmu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)

		// prevent tree from becoming ready, so we can test not ready path
		mu.Lock()

		tmu.Lock()
		_, h := newEditFile(t, tmu, comp, fileContent)
		tmu.Unlock()

		cref, ok := h.(*text.StatusBar)
		tree, ok := cref.Buffer().View().(*treesitter.Tree)
		require.True(t, ok)

		it, ok := tree.Folds()
		require.True(t, ok)

		it2, ok := tree.InitialFolds()
		require.True(t, ok)

		// syntax tree becomes ready
		mu.Unlock()

		// this should block until initialization is complete
		actual, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Empty(t, actual)

		actual, err = iterator.ToSlice(context.Background(), it2)
		require.NoError(t, err)
		assert.Empty(t, actual)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("if language package has no parser file, the iterators are eventually empty", func(t *testing.T) {
		rootPkgs := newInstalledPkgManagerWithFiles(t,
			"go/highlights.scm",
			"go/indents.scm",
			"go/folds.scm",
		)
		var mu sync.Mutex
		libDirIt := iterator.FromFunc(
			func(ctx context.Context) (string, bool, error) {
				mu.Lock()
				defer mu.Unlock()
				val, ok := rootPkgs.ret.Next(ctx)
				return val, ok, nil
			},
			func() error {
				return nil
			},
		)

		pkgs := &mockPkgManager{ret: libDirIt}
		ready := func(context.Context) error {
			return nil
		}
		const width, height = 30, 15
		tmu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)

		// prevent tree from becoming ready, so we can test not ready path
		mu.Lock()

		tmu.Lock()
		_, h := newEditFile(t, tmu, comp, fileContent)
		tmu.Unlock()

		cref, ok := h.(*text.StatusBar)
		tree, ok := cref.Buffer().View().(*treesitter.Tree)
		require.True(t, ok)

		it, ok := tree.Folds()
		require.True(t, ok)

		it2, ok := tree.InitialFolds()
		require.True(t, ok)

		// syntax tree becomes ready
		mu.Unlock()

		// this should block until initialization is complete
		actual, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Empty(t, actual)

		actual, err = iterator.ToSlice(context.Background(), it2)
		require.NoError(t, err)
		assert.Empty(t, actual)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("if file has no extension or no language package, the iterators are eventually empty", func(t *testing.T) {
		rootPkgs := newInstalledPkgManagerWithFiles(t,
			"go/tree-sitter.so",
			"go/highlights.scm",
			"go/indents.scm",
			"go/folds.scm",
		)
		var mu sync.Mutex
		libDirIt := iterator.FromFunc(
			func(ctx context.Context) (string, bool, error) {
				mu.Lock()
				defer mu.Unlock()
				val, ok := rootPkgs.ret.Next(ctx)
				return val, ok, nil
			},
			func() error {
				return nil
			},
		)

		pkgs := &mockPkgManager{ret: libDirIt}
		ready := func(context.Context) error {
			return nil
		}
		const width, height = 30, 15
		tmu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)

		// prevent tree from becoming ready, so we can test not ready path
		mu.Lock()

		tmu.Lock()
		_, h := newEditFileName(t, tmu, comp, "abc", strconv.Itoa(int(rand.Int())))

		cref, ok := h.(*text.StatusBar)
		tree, ok := cref.Buffer().View().(*treesitter.Tree)
		require.True(t, ok)

		it, ok := tree.Folds()
		require.True(t, ok)

		it2, ok := tree.InitialFolds()
		require.True(t, ok)
		tmu.Unlock()

		// syntax tree becomes ready
		mu.Unlock()

		// this should block until initialization is complete
		actual, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Empty(t, actual)

		actual, err = iterator.ToSlice(context.Background(), it2)
		require.NoError(t, err)
		assert.Empty(t, actual)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("if folds.scm file is not found and tree is ready Folds returns false", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"go/tree-sitter.so",
			"go/highlights.scm",
			"go/indents.scm",
		)
		_, _, tree, cleanup := newTreeWithPkgManager(t, pkgs)

		_, ok := tree.Folds()
		require.False(t, ok)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("first call to InitialFolds waits for language and parser to be initialized", func(t *testing.T) {
		tree, cleanup := newTree(t)

		it, ok := tree.InitialFolds()
		require.True(t, ok)

		actual, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)

		expected := []term.Range{
			{
				Start: term.Coordinates{Y: 2},
				End:   term.Coordinates{X: 1, Y: 6},
			},
		}
		assert.ElementsMatch(t, expected, actual)

		cleanup()
	})
}

func TestTreeFoldsOffEventLoopBufferRace(t *testing.T) {
	pkgs := newInstalledPkgManager(t)
	var wg sync.WaitGroup
	ready := func(context.Context) error {
		wg.Done()
		return nil
	}
	const width, height = 30, 15
	mu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)
	defer cleanup()

	wg.Add(1)
	mu.Lock()
	_, h := newEditFile(t, mu, comp, fileContent)
	mu.Unlock()
	wg.Wait()

	cref := h.(*text.StatusBar)
	tree := cref.Buffer().View().(*treesitter.Tree)
	buf := cref.Buffer()
	defer func() { require.NoError(t, tree.Close()) }()

	const iterations = 50
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range iterations {
			it, ok := tree.Folds()
			if !ok {
				continue
			}
			_, _ = iterator.ToSlice(context.Background(), it)
		}
	}()

	for i := range iterations {
		mu.Lock()
		buf.InsertString(term.Coordinates{}, "// edit "+strconv.Itoa(i)+"\n")
		mu.Unlock()
	}
	<-done
}

func TestTreeIndentsIntegration(t *testing.T) {
	var wg sync.WaitGroup
	ready := func(context.Context) error {
		wg.Done()
		return nil
	}
	const width, height = 30, 15
	pkgs := newInstalledPkgManager(t)
	mu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)
	w := newWriter(width, height)

	wg.Add(1)
	mu.Lock()
	newEditFile(t, mu, comp, fileContent)
	mu.Unlock()
	wg.Wait()

	sequenceCases := []handlertest.SequenceTestCase{
		{
			"ggi\n", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│                            │
│####### main                │
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<ukO\n", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│                            │
│▐                           │
│####### main                │
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<ujo", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│▐                           │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<ugg0jjjo", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│###### (                    │
│    #####                   │
│    ▐                       │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<ugg0jjo", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│###### (                    │
│    ▐                       │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<ugg0jjjjo", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ▐                       │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<ugg0jjjjjo", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│    ▐                       │
│)                           │
│                            │
│#### main() {               │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<ugg0jjjjjjofunc hello() {\nfmt.Println(\"\")\n\t}", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│#### hello() {              │
│    fmt.Println(##)         │
│}▐                          │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<ugg0jjjjjjjjogo debug(func() {\nfmt.Println(\"\")\n}\t)", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│    ## debug(####() {       │
│    fmt.Println(##)         │
│    }    )▐                 │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<ugg0jjjjjjjjogo debug(func() {\nfmt.Println(\"\")\n^}\t)", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│    ## debug(####() {       │
│    fmt.Println(##)         │
│}    )▐                     │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<ugg0jjjjjjjo", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│▐                           │
│#### main() {               │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<ugg0jjjjjjjjo", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│    ▐                       │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<ugg0jjjjjjjjjo", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│    fmt.Println(#####, cli.N│
│    ▐                       │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<ugg0jjjjjjjjjjo", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│    fmt.Println(#####, cli.N│
│    ### i := #; i < ##; i++ │
│        ▐                   │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<ugg0jjjjjjjjjjjo", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│    fmt.Println(#####, cli.N│
│    ### i := #; i < ##; i++ │
│        fmt.Println(####, i)│
│        ▐                   │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<ugg0jjjjjjjjjjjjo", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│    fmt.Println(#####, cli.N│
│    ### i := #; i < ##; i++ │
│        fmt.Println(####, i)│
│    }                       │
│    ▐                       │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<ugg0jjjjjjjjjjjjjo", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│    ########################│
│)                           │
│                            │
│#### main() {               │
│    fmt.Println(#####, cli.N│
│    ### i := #; i < ##; i++ │
│        fmt.Println(####, i)│
│    }                       │
│}                           │
│▐                           │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<uggGO", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│    }                       │
│}                           │
│                            │
│##### fileContent = ########│
│    ############+           │
│    ###########+            │
│    ####+                   │
│    ########################│
│    ###                     │
│▐                           │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<uggGo", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│}                           │
│                            │
│##### fileContent = ########│
│    ############+           │
│    ###########+            │
│    ####+                   │
│    ########################│
│    ###                     │
│                            │
│▐                           │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<uggjjjO", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│###### (                    │
│    ▐                       │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<uggjjO", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│▐                           │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<uGO", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│    }                       │
│}                           │
│                            │
│##### fileContent = ########│
│    ############+           │
│    ###########+            │
│    ####+                   │
│    ########################│
│    ###                     │
│▐                           │
│                     #######│
└────────────────────────────┘`,
		},
		{
			"<kcc", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│    }                       │
│}                           │
│                            │
│##### fileContent = ########│
│    ############+           │
│    ###########+            │
│    ####+                   │
│    ########################│
│    ▐                       │
│                            │
│                     #######│
└────────────────────────────┘`,
		},
	}
	handlertest.TestHandlerSequenceWriter(t, w, comp.Browser(), width, height, sequenceCases)

	cleanup()
	goleak.VerifyNone(t)
}

func TestEdgeCaseIndents(t *testing.T) {
	/* the parser parses nodes incorrectly so there's not much
	   we can do without a big refactor in the logic. The following
	   are the logs that we gathered from the first test case:
	START(10)"
	shouldProcess: true && isBegin: false && (isInErr: true || startRow: 9 != endRow: 9) && (startRow: 9 != line 10) -> NODE: [{9 17},{9 18}]: {"
	shouldProcess: true && isBegin: true && (isInErr: true || startRow: 9 != endRow: 15) && (startRow: 9 != line 10) -> NODE: [{9 17},{15 1}]: {\n\n\tfmt.Println(\"%+v\", cli.NewCLI)\n\tfor i := 0; i < 10; i++ {\n\t\tfmt.Println(\"%d\", i)\n\t}\n}"
	BINGO, new indent: 1"
	shouldProcess: false && isBegin: true && (isInErr: false || startRow: 9 != endRow: 15) && (startRow: 9 != line 10) -> NODE: [{9 10},{15 1}]: func() {\n\n\tfmt.Println(\"%+v\", cli.NewCLI)\n\tfor i := 0; i < 10; i++ {\n\t\tfmt.Println(\"%d\", i)\n\t}\n}"
	shouldProcess: false && isBegin: false && (isInErr: false || startRow: 9 != endRow: 17) && (startRow: 9 != line 10) -> NODE: [{9 10},{17 19}]: func() {\n\n\tfmt.Println(\"%+v\", cli.NewCLI)\n\tfor i := 0; i < 10; i++ {\n\t\tfmt.Println(\"%d\", i)\n\t}\n}\n\nconst fileContent ="
	shouldProcess: true && isBegin: false && (isInErr: true || startRow: 8 != endRow: 22) && (startRow: 8 != line 10) -> NODE: [{8 12},{22 4}]: {\n\tgo debug(func() {\n\n\tfmt.Println(\"%+v\", cli.NewCLI)\n\tfor i := 0; i < 10; i++ {\n\t\tfmt.Println(\"%d\", i)\n\t}\n}\n\nconst fileContent = \"package main\\n\" +\n\t\"import (\\n\"+\n\t\"\\\"fmt\\\"\\n\"+\n\t\"\\n\"+\n\t\"\\\"github.com/unstablebuild/blue/cli\\\"\\n\"\n\t\")\""
	shouldProcess: true && isBegin: false && (isInErr: false || startRow: 0 != endRow: 23) && (startRow: 0 != line 10) -> NODE: [{0 0},{23 0}]: package main\n\nimport (\n\t\"fmt\"\n\n\t\"github.com/unstablebuild/blue/cli\"\n)\n\nfunc main() {\n\tgo debug(func() {\n\n\tfmt.Println(\"%+v\", cli.NewCLI)\n\tfor i := 0; i < 10; i++ {\n\t\tfmt.Println(\"%d\", i)\n\t}\n}\n\nconst fileContent = \"package main\\n\" +\n\t\"import (\\n\"+\n\t\"\\\"fmt\\\"\\n\"+\n\t\"\\n\"+\n\t\"\\\"github.com/unstablebuild/blue/cli\\\"\\n\"\n\t\")\"                                      \n"
	END(10)"

	vs for example, if we were to close "go debug(func()" with "(" instead of "{":
	START(10)"
	shouldProcess: true && isBegin: false && (isInErr: true || startRow: 9 != endRow: 9) && (startRow: 9 != line 10) -> NODE: [{9 17},{9 18}]: ("
	shouldProcess: true && isBegin: true && (isInErr: true || startRow: 9 != endRow: 11) && (startRow: 9 != line 10) -> NODE: [{9 17},{11 12}]: (\n\n\tfmt.Println"
	BINGO, new indent: 1"
	shouldProcess: false && isBegin: false && (isInErr: false || startRow: 9 != endRow: 11) && (startRow: 9 != line 10) -> NODE: [{9 10},{11 12}]: func() (\n\n\tfmt.Println"
	shouldProcess: false && isBegin: false && (isInErr: false || startRow: 9 != endRow: 11) && (startRow: 9 != line 10) -> NODE: [{9 10},{11 31}]: func() (\n\n\tfmt.Println(\"%+v\", cli.NewCLI)"
	shouldProcess: false && isBegin: false && (isInErr: false || startRow: 9 != endRow: 12) && (startRow: 9 != line 10) -> NODE: [{9 9},{12 11}]: (func() (\n\n\tfmt.Println(\"%+v\", cli.NewCLI)\n\tfor i := 0"
	shouldProcess: false && isBegin: false && (isInErr: false || startRow: 9 != endRow: 12) && (startRow: 9 != line 10) -> NODE: [{9 1},{12 24}]: go debug(func() (\n\n\tfmt.Println(\"%+v\", cli.NewCLI)\n\tfor i := 0; i < 10; i++"
	shouldProcess: false && isBegin: false && (isInErr: false || startRow: 9 != endRow: 12) && (startRow: 9 != line 10) -> NODE: [{9 1},{12 24}]: go debug(func() (\n\n\tfmt.Println(\"%+v\", cli.NewCLI)\n\tfor i := 0; i < 10; i++"
	shouldProcess: true && isBegin: true && (isInErr: true || startRow: 8 != endRow: 15) && (startRow: 8 != line 10) -> NODE: [{8 12},{15 1}]: {\n\tgo debug(func() (\n\n\tfmt.Println(\"%+v\", cli.NewCLI)\n\tfor i := 0; i < 10; i++ {\n\t\tfmt.Println(\"%d\", i)\n\t}\n}"
	BINGO, new indent: 2"
	shouldProcess: false && isBegin: false && (isInErr: false || startRow: 8 != endRow: 15) && (startRow: 8 != line 10) -> NODE: [{8 0},{15 1}]: func main() {\n\tgo debug(func() (\n\n\tfmt.Println(\"%+v\", cli.NewCLI)\n\tfor i := 0; i < 10; i++ {\n\t\tfmt.Println(\"%d\", i)\n\t}\n}"
	shouldProcess: true && isBegin: false && (isInErr: false || startRow: 0 != endRow: 23) && (startRow: 0 != line 10) -> NODE: [{0 0},{23 0}]: package main\n\nimport (\n\t\"fmt\"\n\n\t\"github.com/unstablebuild/blue/cli\"\n)\n\nfunc main() {\n\tgo debug(func() (\n\n\tfmt.Println(\"%+v\", cli.NewCLI)\n\tfor i := 0; i < 10; i++ {\n\t\tfmt.Println(\"%d\", i)\n\t}\n}\n\nconst fileContent = \"package main\\n\" +\n\t\"import (\\n\"+\n\t\"\\\"fmt\\\"\\n\"+\n\t\"\\n\"+\n\t\"\\\"github.com/unstablebuild/blue/cli\\\"\\n\"\n\t\")\"                                      \n"
	END(10)"
	*/
	t.SkipNow()

	var wg sync.WaitGroup
	ready := func(context.Context) error {
		wg.Done()
		return nil
	}
	const width, height = 30, 15
	pkgs := newInstalledPkgManager(t)
	mu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)
	defer cleanup()
	w := newWriter(width, height)

	wg.Add(1)
	mu.Lock()
	newEditFile(t, mu, comp, fileContent)
	mu.Unlock()
	wg.Wait()

	logrus.SetLevel(logrus.TraceLevel)
	defer logrus.SetLevel(logrus.InfoLevel)

	sequenceCases := []handlertest.SequenceTestCase{
		{
			"jjjjjjjjogo debug(func() {\nfmt", `┌────────────────────────────┐
│o #####                     │
├────────────────────────────┤
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│    ## debug(####() {       │
│        fmt▐                │
│                     #######│
└────────────────────────────┘`,
		},
		{
			".Println(\"\")\nif true {\n// nothing \n} else {\nif true {\ngo debug.CapturePanic(func(){\n", `┌────────────────────────────┐
│o #####                     │
├────────────────────────────┤
│                            │
│#### main() {               │
│    ## debug(####() {       │
│        fmt.Println(##)     │
│        ## true {           │
│            ###########     │
│        } #### {            │
│            ## true {       │
│                ## debug(###│
│                    ▐       │
│                     #######│
└────────────────────────────┘`,
		},
	}

	handlertest.TestHandlerSequenceWriter(t, w, comp.Browser(), width, height, sequenceCases)
}

func TestTreeHighlightsMissingHighlightsFile(t *testing.T) {
	ready := func(context.Context) error {
		return nil
	}
	const width, height = 30, 15
	pkgs := newInstalledPkgManagerWithFiles(t,
		"go/tree-sitter.so",
	)
	mu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)

	mu.Lock()
	_, h := newEditFile(t, mu, comp, fileContent)
	mu.Unlock()
	cref, ok := h.(*text.StatusBar)
	require.True(t, ok)
	tree, ok := cref.Buffer().View().(*treesitter.Tree)
	require.True(t, ok)

	require.NoError(t, tree.Close())
	cleanup()
	goleak.VerifyNone(t)
}

func TestTreeHighlightsIntegration(t *testing.T) {
	var wg sync.WaitGroup
	ready := func(context.Context) error {
		wg.Done()
		return nil
	}
	const width, height = 30, 15
	pkgs := newInstalledPkgManager(t)
	mu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)
	w := newWriter(width, height)

	wg.Add(1)
	mu.Lock()
	ed, _ := newEditFile(t, mu, comp, fileContent)
	mu.Unlock()
	wg.Wait()

	// content was added oob so cursor start at the bottom:
	// gg to the top
	cases := []handlertest.SingleTestCase{
		{
			term.Event{Ch: 'g', Type: term.EventKey}, `
┌######──────────────────────┐
│# ####                      │
├────────────────────────────┤
│    }                       │
│}                           │
│                            │
│##### fileContent = ########│
│    ############+           │
│    ###########+            │
│    ####+                   │
│    ########################│
│    ###                     │
│                            │
│                      NORMAL│
└────────────────────────────┘`,
		},
	}
	handlertest.TestHandler(t, comp.Browser(), cases, w)

	start := term.Coordinates{Y: 8}
	end := term.Coordinates{Y: 8, X: 1}
	_, _, _ = ed.Edit(context.Background(), start, end, "")

	cases = []handlertest.SingleTestCase{
		{
			term.Event{Ch: 'g', Type: term.EventKey}, `
┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│unc main() {                │
│    fmt.Println(#####, cli.N│
│                      NORMAL│
└────────────────────────────┘`,
		},
	}
	handlertest.TestHandler(t, comp.Browser(), cases, w)

	start = term.Coordinates{Y: 9}
	end = term.Coordinates{Y: 9, X: 1}
	ed.Edit(context.Background(), start, end, "")

	cases = []handlertest.SingleTestCase{
		{
			term.Event{}, `
┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│unc main() {                │
│fmt.Println(#####, cli.NewCL│
│                      NORMAL│
└────────────────────────────┘`,
		},
	}
	handlertest.TestHandler(t, comp.Browser(), cases, w)

	start = term.Coordinates{Y: 9}
	end = term.Coordinates{Y: 9}
	ed.Edit(context.Background(), start, end, "\t")

	cases = []handlertest.SingleTestCase{
		{
			term.Event{}, `
┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│unc main() {                │
│    fmt.Println(#####, cli.N│
│                      NORMAL│
└────────────────────────────┘`,
		},
	}
	handlertest.TestHandler(t, comp.Browser(), cases, w)

	start = term.Coordinates{Y: 8}
	end = term.Coordinates{Y: 10}
	ed.Edit(context.Background(), start, end, "func main() {\n\tfmt.Sprintf(\"%s\", \"\")\n")

	cases = []handlertest.SingleTestCase{
		{
			term.Event{}, `
┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│    fmt.Sprintf(####, ##)   │
│                      NORMAL│
└────────────────────────────┘`,
		},
	}
	handlertest.TestHandler(t, comp.Browser(), cases, w)

	start = term.Coordinates{Y: 9, X: 16}
	end = start
	ed.Edit(context.Background(), start, end, "🔥")

	cases = []handlertest.SingleTestCase{
		{
			term.Event{}, `
┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│    fmt.Sprintf(#### #, ##) │
│                      NORMAL│
└────────────────────────────┘`,
		},
	}
	handlertest.TestHandler(t, comp.Browser(), cases, w)

	start = term.Coordinates{Y: 9, X: 16}
	end = term.Coordinates{Y: 9, X: 17}
	ed.Edit(context.Background(), start, end, "")

	cases = []handlertest.SingleTestCase{
		{
			term.Event{}, `
┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│    fmt.Sprintf(####, ##)   │
│                      NORMAL│
└────────────────────────────┘`,
		},
	}
	handlertest.TestHandler(t, comp.Browser(), cases, w)

	start = term.Coordinates{Y: 9, X: 0}
	end = term.Coordinates{Y: 10, X: 0}
	ed.Edit(context.Background(), start, end, "")

	cases = []handlertest.SingleTestCase{
		{
			term.Event{}, `
┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│    ### i := #; i < ##; i++ │
│                      NORMAL│
└────────────────────────────┘`,
		},
	}
	handlertest.TestHandler(t, comp.Browser(), cases, w)

	start = term.Coordinates{Y: 7, X: 0}
	end = term.Coordinates{Y: 8, X: 0}
	_, _, _ = ed.Edit(context.Background(), start, end, "")

	sequenceCases := []handlertest.SequenceTestCase{
		{
			"", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│#### main() {               │
│    ### i := #; i < ##; i++ │
│        fmt.Println(####, i)│
│                      NORMAL│
└────────────────────────────┘`,
		},
		{
			"uu", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│   ▐fmt.Sprintf(####, ##)   │
│                      NORMAL│
└────────────────────────────┘`,
		},
		{
			"G", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│    }                       │
│}                           │
│                            │
│##### fileContent = ########│
│    ############+           │
│    ###########+            │
│    ####+                   │
│    ########################│
│    ###                     │
│▐                           │
│                      NORMAL│
└────────────────────────────┘`,
		},
		{
			"VkkkkkkduVkkkkkkkkkkkkkkkkkkdugg", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│    fmt.Sprintf(####, ##)   │
│                      NORMAL│
└────────────────────────────┘`,
		},
		{
			"ggjjjjjjjjjwi/* <$i*/<", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│####### main                │
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│    #### fmt.Sprintf(####, #│
│                      NORMAL│
└────────────────────────────┘`,
		},
	}
	handlertest.TestHandlerSequenceWriter(t, w, comp.Browser(), width, height, sequenceCases)

	// after flush it should be the same
	win, err := comp.Focus()
	require.NoError(t, err)
	{
		ch, err := comp.Flush(context.Background(), win)
		require.NoError(t, err)
		require.NoError(t, <-ch)
	}
	sequenceCases = []handlertest.SequenceTestCase{
		{
			"", `┌######──────────────────────┐
│# ####                      │
├────────────────────────────┤
│####### main                │
│                            │
│###### (                    │
│    #####                   │
│                            │
│    ########################│
│)                           │
│                            │
│#### main() {               │
│    #### fmt.Sprintf(####, #│
│                      NORMAL│
└────────────────────────────┘`,
		},
	}
	handlertest.TestHandlerSequenceWriter(t, w, comp.Browser(), width, height, sequenceCases)

	sequenceCases = []handlertest.SequenceTestCase{
		{
			"Gofunc helloWorld(){\n\tfmt.Println(\"hello world\")\n}", `┌#######─────────────────────┐
│# #####                     │
├────────────────────────────┤
│##### fileContent = ########│
│    ############+           │
│    ###########+            │
│    ####+                   │
│    ########################│
│    ###                     │
│                            │
│#### helloWorld(){          │
│    fmt.Println(############│
│}▐                          │
│                     #######│
└────────────────────────────┘`,
		},
	}
	handlertest.TestHandlerSequenceWriter(t, w, comp.Browser(), width, height, sequenceCases)

	cleanup()
	goleak.VerifyNone(t)
}

func TestTreeSelectionIntegration(t *testing.T) {
	const content = `package main

type person struct {
	Name string
}

func greet(p person) string {
	values := []string{
		p.Name,
		format("hello", p.Name),
	}
	if len(values) > 0 {
		for _, value := range values {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
`

	_, _, tree, cleanup := newTreeWithPkgManagerContent(t, newInstalledPkgManager(t), content)
	defer cleanup()
	defer func() { require.NoError(t, tree.Close()) }()

	t.Run("expand cases", func(t *testing.T) {
		cases := []struct {
			name string
			from term.Range
			want []term.Range
		}{
			{
				name: "caret in selector expression climbs to statement then function",
				from: term.Range{Start: term.Coordinates{Y: 8, X: 4}, End: term.Coordinates{Y: 8, X: 4}},
				want: []term.Range{
					{Start: term.Coordinates{Y: 8, X: 4}, End: term.Coordinates{Y: 8, X: 8}},
					{Start: term.Coordinates{Y: 8, X: 2}, End: term.Coordinates{Y: 8, X: 8}},
					{Start: term.Coordinates{Y: 7, X: 19}, End: term.Coordinates{Y: 10, X: 2}},
					{Start: term.Coordinates{Y: 7, X: 11}, End: term.Coordinates{Y: 10, X: 2}},
					{Start: term.Coordinates{Y: 7, X: 1}, End: term.Coordinates{Y: 10, X: 2}},
					{Start: term.Coordinates{Y: 7, X: 1}, End: term.Coordinates{Y: 17, X: 0}},
				},
			},
			{
				name: "existing call argument selection expands to call and literal element",
				from: term.Range{Start: term.Coordinates{Y: 9, X: 9}, End: term.Coordinates{Y: 9, X: 16}},
				want: []term.Range{
					{Start: term.Coordinates{Y: 9, X: 8}, End: term.Coordinates{Y: 9, X: 25}},
					{Start: term.Coordinates{Y: 9, X: 2}, End: term.Coordinates{Y: 9, X: 25}},
				},
			},
			{
				name: "condition expands through if statement",
				from: term.Range{Start: term.Coordinates{Y: 11, X: 8}, End: term.Coordinates{Y: 11, X: 14}},
				want: []term.Range{
					{Start: term.Coordinates{Y: 11, X: 7}, End: term.Coordinates{Y: 11, X: 15}},
					{Start: term.Coordinates{Y: 11, X: 4}, End: term.Coordinates{Y: 11, X: 15}},
					{Start: term.Coordinates{Y: 11, X: 4}, End: term.Coordinates{Y: 11, X: 19}},
				},
			},
			{
				name: "return expression expands to return statement and for statement",
				from: term.Range{Start: term.Coordinates{Y: 13, X: 10}, End: term.Coordinates{Y: 13, X: 17}},
				want: []term.Range{
					{Start: term.Coordinates{Y: 13, X: 10}, End: term.Coordinates{Y: 13, X: 27}},
					{Start: term.Coordinates{Y: 13, X: 10}, End: term.Coordinates{Y: 13, X: 34}},
					{Start: term.Coordinates{Y: 13, X: 3}, End: term.Coordinates{Y: 13, X: 34}},
				},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				current := tc.from
				for _, want := range tc.want {
					next, ok := tree.SelectionExpand(current)
					require.True(t, ok, "from: %#v", current)
					assert.Equal(t, want, next)
					current = next
				}
			})
		}
	})

	t.Run("shrink cases", func(t *testing.T) {
		cases := []struct {
			name  string
			from  term.Range
			caret term.Coordinates
			want  term.Range
		}{
			{
				name:  "function body shrinks toward selector at caret",
				from:  term.Range{Start: term.Coordinates{Y: 6, X: 28}, End: term.Coordinates{Y: 17, X: 1}},
				caret: term.Coordinates{Y: 9, X: 18},
				want:  term.Range{Start: term.Coordinates{Y: 7, X: 1}, End: term.Coordinates{Y: 17, X: 0}},
			},
			{
				name:  "literal value shrinks to element nearest caret",
				from:  term.Range{Start: term.Coordinates{Y: 7, X: 19}, End: term.Coordinates{Y: 10, X: 2}},
				caret: term.Coordinates{Y: 8, X: 4},
				want:  term.Range{Start: term.Coordinates{Y: 8, X: 2}, End: term.Coordinates{Y: 8, X: 8}},
			},
			{
				name:  "if block shrinks to for statement around caret",
				from:  term.Range{Start: term.Coordinates{Y: 11, X: 20}, End: term.Coordinates{Y: 15, X: 2}},
				caret: term.Coordinates{Y: 13, X: 18},
				want:  term.Range{Start: term.Coordinates{Y: 12, X: 2}, End: term.Coordinates{Y: 15, X: 0}},
			},
			{
				name:  "file shrinks to type declaration around type caret",
				from:  term.Range{Start: term.Coordinates{}, End: term.Coordinates{Y: 18}},
				caret: term.Coordinates{Y: 2, X: 6},
				want:  term.Range{Start: term.Coordinates{Y: 2, X: 0}, End: term.Coordinates{Y: 4, X: 1}},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, ok := tree.SelectionShrink(tc.from, tc.caret)
				require.True(t, ok)
				assert.Equal(t, tc.want, got)
			})
		}
	})

	t.Run("returns false when syntax tree is unavailable", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"go/highlights.scm",
			"go/indents.scm",
			"go/folds.scm",
		)
		_, _, tree, cleanup := newTreeWithPkgManager(t, pkgs)
		defer cleanup()
		defer func() { require.NoError(t, tree.Close()) }()

		_, ok := tree.SelectionExpand(term.Range{
			Start: term.Coordinates{Y: 2, X: 15},
			End:   term.Coordinates{Y: 2, X: 18},
		})
		assert.False(t, ok)
	})
}

func TestViZModeSelectionRangeExpandShrinkIntegration(t *testing.T) {
	const content = `package main

func greet(value string) string {
	if value != "" {
		return strings.TrimSpace(value)
	}
	return ""
}
`

	buf, _, tree, cleanup := newTreeWithPkgManagerContent(t, newInstalledPkgManager(t), content)
	defer cleanup()
	defer func() { require.NoError(t, tree.Close()) }()

	uri, err := workspaceapi.ParseURI("memory:///zselect.go")
	require.NoError(t, err)

	var wg sync.WaitGroup
	var mu sync.Mutex
	scheduleNextTick := func(fn func()) bool {
		go func() {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock()
			fn()
		}()
		return true
	}
	h := vi.New(buf, uri, vi.WithScheduleNextTick(scheduleNextTick))
	mu.Lock()
	h.Resize(80, 12)
	require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 4, X: 17}))
	mu.Unlock()

	rangeFromZHighlight := func(t *testing.T) (term.Range, bool) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		for _, set := range h.LocationLists() {
			if set.ID != "_foldHighlightID" || len(set.Locations) == 0 {
				continue
			}
			loc := set.Locations[0]
			return term.Range{Start: loc.From, End: loc.To}, true
		}
		return term.Range{}, false
	}

	handleRune := func(t *testing.T, ch rune) bool {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		_, handled := h.Handle(term.Event{Type: term.EventKey, Ch: ch})
		return handled
	}

	wg.Add(1)
	require.True(t, handleRune(t, 'z'))
	wg.Wait()
	var widest term.Range
	for range 32 {
		handled := handleRune(t, 'h')
		if !handled {
			break
		}
		var ok bool
		widest, ok = rangeFromZHighlight(t)
		require.True(t, ok)
		require.NotEqual(t, term.Range{}, widest)
	}
	require.NotEqual(t, term.Range{}, widest)

	previous, ok := rangeFromZHighlight(t)
	require.True(t, ok)
	var narrowest term.Range
	for range 32 {
		handled := handleRune(t, 'l')
		current, currentOK := rangeFromZHighlight(t)
		if !handled {
			require.True(t, currentOK, "failed shrink should keep previous z-mode range highlighted")
			assert.Equal(t, previous, current)
			assert.NotEqual(t, term.Range{}, current)
			assert.NotEqual(t, term.Coordinates{}, current.End)
			narrowest = current
			break
		}
		require.True(t, currentOK)
		require.NotEqual(t, term.Range{}, current)
		previous = current
		narrowest = current
	}
	require.NotEqual(t, term.Range{}, narrowest)

	require.True(t, handleRune(t, 'v'))
	mu.Lock()
	selection, ok := h.Selection()
	mu.Unlock()
	require.True(t, ok)
	require.NotEmpty(t, selection)
}

func TestTreeStateIntegration(t *testing.T) {
	t.Run("first call to State waits for language and parser to be initialized", func(t *testing.T) {
		tree, cleanup := newTree(t)

		it := tree.State()

		actual, ok := it.Next(context.Background())
		require.True(t, ok)
		defer it.Close()
		assert.Equal(t, syntax.State{
			LangID:     "go",
			Folds:      true,
			Indents:    true,
			Highlights: true,
			Progress:   1,
		}, actual)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("if folds.scm file is not found and tree is NOT ready, State itertor eventually streams state", func(t *testing.T) {
		rootPkgs := newInstalledPkgManagerWithFiles(t,
			"go/tree-sitter.so",
			"go/highlights.scm",
			"go/indents.scm",
		)
		var mu sync.Mutex
		libDirIt := iterator.FromFunc(
			func(ctx context.Context) (string, bool, error) {
				mu.Lock()
				defer mu.Unlock()
				val, ok := rootPkgs.ret.Next(ctx)
				return val, ok, nil
			},
			func() error {
				return nil
			},
		)

		pkgs := &mockPkgManager{ret: libDirIt}
		ready := func(context.Context) error {
			return nil
		}
		const width, height = 30, 15
		tmu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)

		// prevent tree from becoming ready, so we can test not ready path
		mu.Lock()

		tmu.Lock()
		_, h := newEditFile(t, tmu, comp, fileContent)
		tmu.Unlock()

		cref, ok := h.(*text.StatusBar)
		tree, ok := cref.Buffer().View().(*treesitter.Tree)
		require.True(t, ok)

		it := tree.State()

		// syntax tree becomes ready
		mu.Unlock()

		// this should block until initialization is complete
		actual, ok := it.Next(context.Background())
		require.True(t, ok)
		defer it.Close()
		assert.Equal(t, syntax.State{
			LangID:     "go",
			Folds:      false,
			Indents:    true,
			Highlights: true,
			Progress:   1,
		}, actual)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("if file has no extension or no language package, State eventually returns State", func(t *testing.T) {
		rootPkgs := newInstalledPkgManagerWithFiles(t,
			"go/tree-sitter.so",
			"go/highlights.scm",
			"go/indents.scm",
			"go/folds.scm",
		)
		var mu sync.Mutex
		libDirIt := iterator.FromFunc(
			func(ctx context.Context) (string, bool, error) {
				mu.Lock()
				defer mu.Unlock()
				val, ok := rootPkgs.ret.Next(ctx)
				return val, ok, nil
			},
			func() error {
				return nil
			},
		)

		pkgs := &mockPkgManager{ret: libDirIt}
		ready := func(context.Context) error {
			return nil
		}
		const width, height = 30, 15
		tmu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)

		// prevent tree from becoming ready, so we can test not ready path
		mu.Lock()

		tmu.Lock()
		_, h := newEditFileName(t, tmu, comp, "abc", strconv.Itoa(int(rand.Int())))

		cref, ok := h.(*text.StatusBar)
		tree, ok := cref.Buffer().View().(*treesitter.Tree)
		require.True(t, ok)

		it := tree.State()

		// syntax tree becomes ready
		mu.Unlock()
		tmu.Unlock()

		// this should block until initialization is complete
		actual, ok := it.Next(context.Background())
		require.True(t, ok)
		defer it.Close()
		assert.Equal(t, syntax.State{ParserError: "file does not have an extension " +
			"and it's not a recognized file"}, actual)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("if highligghts.scm file is not found and tree is ready State returns state with Folds set to false", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"go/tree-sitter.so",
			"go/indents.scm",
			"go/folds.scm",
		)
		_, _, tree, cleanup := newTreeWithPkgManager(t, pkgs)

		it := tree.State()
		actual, ok := it.Next(context.Background())
		require.True(t, ok)
		assert.Equal(t, syntax.State{
			LangID:     "go",
			Folds:      true,
			Indents:    true,
			Highlights: false,
			Progress:   1,
		}, actual)
		cleanup()
	})

	t.Run("Tree.Close closes all iterators gracefully", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"go/tree-sitter.so",
			"go/indents.scm",
			"go/folds.scm",
		)
		_, _, tree, cleanup := newTreeWithPkgManager(t, pkgs)

		ctx := context.Background()
		prev := tree.State()
		_, ok := prev.Next(ctx) // drain
		assert.True(t, ok)

		prev2 := tree.State() // don't drain

		var wg sync.WaitGroup
		wg.Go(func() {
			async := tree.State()
			_, ok := async.Next(ctx)
			assert.True(t, ok)

			// this blocks until Close is called
			_, ok = async.Next(ctx)
			assert.False(t, ok)
		})

		require.NoError(t, tree.Close())

		_, ok = prev.Next(ctx)
		assert.False(t, ok)

		_, ok = prev2.Next(ctx)
		assert.True(t, ok) // there was one state in the buffered channel
		_, ok = prev2.Next(ctx)
		assert.False(t, ok)

		wg.Wait() // async also closes

		after := tree.State()
		state, ok := after.Next(ctx)
		assert.True(t, ok)
		assert.True(t, state.Closed)
		_, ok = after.Next(ctx)
		assert.False(t, ok)

		cleanup()
	})

	t.Run("reports parse errors", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"go/tree-sitter.so",
			"go/highlights.scm",
			"go/indents.scm",
			"go/folds.scm",
		)
		buffer, _, tree, cleanup := newTreeWithPkgManager(t, pkgs)

		buffer.InsertString(term.Coordinates{}, "/* */")

		it := tree.State()
		actual, ok := it.Next(context.Background())
		require.True(t, ok)
		assert.Equal(t, syntax.State{
			LangID:     "go",
			Progress:   1,
			Folds:      true,
			Indents:    true,
			Highlights: true,
		}, actual)

		buffer.InsertString(term.Coordinates{}, "}}}}}}}}}}}}}}}}(*&^%$#@!*&^%$#@!")

		actual, ok = it.Next(context.Background())
		require.True(t, ok)
		assert.Equal(t, syntax.State{
			ParserError: "incremental parse error",
			LangID:      "go",
			Progress:    1,
			Folds:       true,
			Indents:     true,
			Highlights:  true,
		}, actual)

		require.NoError(t, tree.Close())
		cleanup()
	})
}

func TestTreeIncrementalParseReleasesPreviousTree(t *testing.T) {
	pkgs := newInstalledPkgManagerWithFiles(t,
		"go/tree-sitter.so",
		"go/highlights.scm",
		"go/indents.scm",
		"go/folds.scm",
	)
	buffer, _, tree, cleanup := newTreeWithPkgManager(t, pkgs)

	it := tree.State()
	_, ok := it.Next(context.Background())
	require.True(t, ok)

	for range 50 {
		buffer.InsertString(term.Coordinates{}, "// edit\n")
		state, ok := it.Next(context.Background())
		require.True(t, ok)
		require.Empty(t, state.ParserError)
	}

	folds, ok := tree.Folds()
	require.True(t, ok)
	ranges, err := iterator.ToSlice(context.Background(), folds)
	require.NoError(t, err)
	assert.NotEmpty(t, ranges)

	require.NoError(t, tree.Close())
	cleanup()
}

func TestTreeIncrementalHighlightsMatchFullReparse(t *testing.T) {
	tests := []struct {
		name    string
		content string
		edits   []bufferEdit
	}{
		{
			name: "typing and newlines",
			content: "package main\n\nfunc main() {\n\tvalue := 1\n\tprintln(value)\n}\n" +
				strings.Repeat("// padding\n", 20),
			edits: []bufferEdit{
				{start: term.Coordinates{Y: 3, X: 6}, content: "Name"},
				{start: term.Coordinates{Y: 4}, content: "\t/* prefix */\n"},
				{start: term.Coordinates{Y: 5, X: 9}, end: term.Coordinates{Y: 5, X: 14}, content: `"value"`},
			},
		},
		{
			name:    "comment string and brace boundaries",
			content: "package main\n\nfunc main() {\n\tprintln(\"text\")\n\tvalue := 2\n}\n",
			edits: []bufferEdit{
				{start: term.Coordinates{Y: 3, X: 1}, content: "/*"},
				{start: term.Coordinates{Y: 4, X: 11}, content: "*/"},
				{start: term.Coordinates{Y: 5}, end: term.Coordinates{Y: 5, X: 1}},
				{start: term.Coordinates{Y: 5}, content: "}"},
			},
		},
		{
			name: "UTF-8 and multiline replacements",
			content: "package main\n// alpha\n\nfunc main() {\n\tprintln(\"界\")\n}\n" +
				strings.Repeat("// padding\n", 20),
			edits: []bufferEdit{
				{start: term.Coordinates{Y: 1, X: 3}, end: term.Coordinates{Y: 1, X: 8}, content: "世界"},
				{start: term.Coordinates{Y: 1, X: 3}, end: term.Coordinates{Y: 1, X: 5}, content: "first\n// second"},
				{start: term.Coordinates{Y: 1, X: 8}, end: term.Coordinates{Y: 2, X: 3}, content: " "},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			buffer, handler, tree, _, cleanup := newTreeWithPkgManagerContentHandler(
				t, newInstalledPkgManager(t), test.content)
			defer func() {
				require.NoError(t, tree.Close())
				cleanup()
			}()

			for i, edit := range test.edits {
				end := edit.end
				if end == (term.Coordinates{}) {
					end = edit.start
				}
				buffer.Edit(context.Background(), edit.start, end, edit.content)
				assertHighlightsEqualFullReparse(t, handler, tree, i)
			}
		})
	}
}

func TestTreeIncrementalHighlightsMatchFullReparseRandomEdits(t *testing.T) {
	const seed int64 = 317
	rng := rand.New(rand.NewSource(seed))
	buffer, handler, tree, _, cleanup := newTreeWithPkgManagerContentHandler(
		t, newInstalledPkgManager(t), benchmarkGoContent(120))
	defer func() {
		require.NoError(t, tree.Close())
		cleanup()
	}()

	insertions := []string{"x", "0", `"s"`, "// note", "/*", "*/", "\n\t", "{"}
	for i := range 80 {
		row := rng.Intn(buffer.Rows())
		columns := buffer.Columns(row)
		start := term.Coordinates{Y: row, X: rng.Intn(columns + 1)}
		end := start
		content := insertions[rng.Intn(len(insertions))]
		if columns > start.X && rng.Intn(3) == 0 {
			end.X += 1 + rng.Intn(columns-start.X)
			if rng.Intn(2) == 0 {
				content = ""
			}
		}
		buffer.Edit(context.Background(), start, end, content)
		assertHighlightsEqualFullReparse(t, handler, tree, i)
	}
}

type bufferEdit struct {
	start   term.Coordinates
	end     term.Coordinates
	content string
}

func assertHighlightsEqualFullReparse(
	t *testing.T, handler text.Handler, tree *treesitter.Tree, edit int,
) {
	t.Helper()
	incremental := syntaxLocations(handler.LocationLists())
	full := forceFullHighlights(t, handler, tree)
	if len(full) != len(incremental) {
		t.Errorf("highlight count mismatch after edit %d: full=%d incremental=%d; first diff: %s",
			edit, len(full), len(incremental), firstHighlightDiff(full, incremental))
		return
	}
	if diff := firstHighlightDiff(full, incremental); diff != "" {
		t.Errorf("highlight mismatch after edit %d: %s", edit, diff)
	}
}

func firstHighlightDiff(full, incremental []textapi.Location) string {
	for i := 0; i < min(len(full), len(incremental)); i++ {
		if full[i] != incremental[i] {
			return fmt.Sprintf("index=%d full=%+v incremental=%+v", i, full[i], incremental[i])
		}
	}
	if len(full) > len(incremental) {
		return fmt.Sprintf("index=%d full=%+v incremental=<missing>",
			len(incremental), full[len(incremental)])
	}
	if len(incremental) > len(full) {
		return fmt.Sprintf("index=%d full=<missing> incremental=%+v",
			len(full), incremental[len(full)])
	}
	return ""
}

func forceFullHighlights(t *testing.T, handler text.Handler, tree *treesitter.Tree) []textapi.Location {
	t.Helper()
	ch, err := tree.Flush(context.Background())
	require.NoError(t, err)
	require.NoError(t, <-ch)
	return syntaxLocations(handler.LocationLists())
}

func syntaxLocations(lists []text.LocationSet) []textapi.Location {
	for _, list := range lists {
		if list.ID == "syntax" {
			return append([]textapi.Location(nil), list.Locations...)
		}
	}
	return nil
}

func BenchmarkKeystrokeInsert(b *testing.B) {
	content := benchmarkGoContent(3_000)
	pkgs := newInstalledPkgManager(b)
	buffer, _, tree, cleanup := newTreeWithPkgManagerContent(b, pkgs, content)
	defer func() {
		require.NoError(b, tree.Close())
		cleanup()
	}()

	pos := term.Coordinates{Y: 1, X: len("// benchmark")}
	inserted := false
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if inserted {
			buffer.Edit(context.Background(), pos,
				term.Coordinates{Y: pos.Y, X: pos.X + 1}, "")
		} else {
			buffer.InsertContext(context.Background(), pos, 'x')
		}
		inserted = !inserted
	}
}

func TestTreeQueryIntegration(t *testing.T) {
	t.Run("if locals.scm file is not found and tree is ready Query returns error", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"go/tree-sitter.so",
			"go/highlights.scm",
			"go/indents.scm",
		)
		_, _, tree, cleanup := newTreeWithPkgManager(t, pkgs)

		_, err := tree.Query("locals.scm", "query")
		require.Error(t, err)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("if indents.scm file is not found and tree is ready Query returns error", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"go/tree-sitter.so",
			"go/highlights.scm",
		)
		_, _, tree, cleanup := newTreeWithPkgManager(t, pkgs)

		_, err := tree.Query("indents.scm", "query")
		require.Error(t, err)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("if highlights.scm file is not found and tree is ready Query returns error", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"go/tree-sitter.so",
		)
		_, _, tree, cleanup := newTreeWithPkgManager(t, pkgs)

		_, err := tree.Query("highlights.scm", "query")
		require.Error(t, err)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("if custom file is not found and tree is ready Query returns error", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"go/tree-sitter.so",
		)
		_, _, tree, cleanup := newTreeWithPkgManager(t, pkgs)

		_, err := tree.Query("nonexistent.scm", "local.reference")
		require.Error(t, err)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("if custom file is given Query uses it to run the query", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"go/tree-sitter.so",
			"abc.scm",
		)
		_, comp, tree, cleanup := newTreeWithPkgManager(t, pkgs)

		w := comp.Workspace()
		f, err := w.OpenFile("abc.scm", os.O_CREATE|os.O_WRONLY, 0666)
		require.NoError(t, err)
		_, err = f.Write([]byte(`((package_identifier) @local.reference
  (#set! reference.kind "namespace"))`))
		require.NoError(t, err)
		require.NoError(t, f.Close())

		it, err := tree.Query(f.Name(), "local.reference")
		require.NoError(t, err)

		expected := []treesitter.Match{
			{
				CaptureName: "local.reference",
				LineString:  "package main",
				Line:        0,
			},
		}
		matches, err := iterator.ToSlice(context.Background(), it)
		assert.Equal(t, expected, matches)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("if locals.scm is given Query uses it to run the query", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"go/tree-sitter.so",
			"go/locals.scm",
		)
		_, _, tree, cleanup := newTreeWithPkgManager(t, pkgs)

		it, err := tree.Query("locals.scm", "local.definition.namespace")
		require.NoError(t, err)

		expected := []treesitter.Match{
			{
				CaptureName: "local.definition.namespace",
				LineString:  "package main",
				Line:        0,
			},
		}
		matches, err := iterator.ToSlice(context.Background(), it)
		assert.Equal(t, expected, matches)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("if language parser is not found, Query iterator errors", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"go/indents.scm",
		)
		_, _, tree, cleanup := newTreeWithPkgManager(t, pkgs)

		it, err := tree.Query("indents.scm", "query")
		require.NoError(t, err)
		_, ok := it.Next(context.Background())
		assert.False(t, ok)
		require.Error(t, it.Err())

		require.NoError(t, tree.Close())
		cleanup()
	})
}

func TestTreeCommentCoverageIntegration(t *testing.T) {
	t.Run("returns exact line comment ranges when fully covered", func(t *testing.T) {
		content := "package main\n\n// alpha\n// beta\nfunc main() {}\n"
		_, _, tree, cleanup := newTreeWithPkgManagerContent(t, newInstalledPkgManager(t), content)

		ranges, ok := tree.CommentCoverage(term.Range{
			Start: term.Coordinates{Y: 2, X: 0},
			End:   term.Coordinates{Y: 3, X: len("// beta")},
		})
		stateIt := tree.State()
		state, _ := stateIt.Next(context.Background())
		_ = stateIt.Close()
		assert.Equal(t, syntax.State{
			LangID: "go", Folds: true, Indents: true, Highlights: true,
			Progress: 1,
		}, state)
		require.True(t, ok)
		assert.Equal(t, []term.Range{
			{Start: term.Coordinates{Y: 2, X: 0}, End: term.Coordinates{Y: 2, X: len("// alpha")}},
			{Start: term.Coordinates{Y: 3, X: 0}, End: term.Coordinates{Y: 3, X: len("// beta")}},
		}, ranges)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("returns false when selection is only partially covered by comments", func(t *testing.T) {
		content := "package main\n\n// alpha\nfunc main() {}\n"
		_, _, tree, cleanup := newTreeWithPkgManagerContent(t, newInstalledPkgManager(t), content)

		ranges, ok := tree.CommentCoverage(term.Range{
			Start: term.Coordinates{Y: 2, X: 0},
			End:   term.Coordinates{Y: 3, X: 4},
		})
		assert.False(t, ok)
		assert.Nil(t, ranges)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("returns exact block comment range when fully covered", func(t *testing.T) {
		content := "package main\n\n/* alpha */\nfunc main() {}\n"
		_, _, tree, cleanup := newTreeWithPkgManagerContent(t, newInstalledPkgManager(t), content)

		ranges, ok := tree.CommentCoverage(term.Range{
			Start: term.Coordinates{Y: 2, X: 0},
			End:   term.Coordinates{Y: 2, X: len("/* alpha */")},
		})
		stateIt := tree.State()
		state, _ := stateIt.Next(context.Background())
		_ = stateIt.Close()
		assert.Equal(t, syntax.State{
			LangID: "go", Folds: true, Indents: true, Highlights: true,
			Progress: 1,
		}, state)
		require.True(t, ok)
		assert.Equal(t, []term.Range{{
			Start: term.Coordinates{Y: 2, X: 0},
			End:   term.Coordinates{Y: 2, X: len("/* alpha */")},
		}}, ranges)

		require.NoError(t, tree.Close())
		cleanup()
	})
}

func newInstalledPkgManager(t testing.TB) *mockPkgManager {
	return newInstalledPkgManagerWithFiles(t,
		"go/tree-sitter.so",
		"go/highlights.scm",
		"go/indents.scm",
		"go/folds.scm",
		"go/locals.scm",
	)
}

func newInstalledPkgManagerWithFiles(t testing.TB, files ...string) *mockPkgManager {
	wd, err := os.Getwd()
	require.NoError(t, err)
	var fullPathFiles []string
	for _, file := range files {
		fullPathFiles = append(fullPathFiles, resolveFixture(t, wd, file))
	}
	return &mockPkgManager{
		ret: iterator.FromSlice(fullPathFiles),
	}
}

// resolveFixture turns a "<lang>/<file>" fixture reference into an
// absolute path. Callers name the parser as "<lang>/tree-sitter.so",
// which is only where the darwin fixture lives.
func resolveFixture(t testing.TB, wd, file string) string {
	if filepath.IsAbs(file) {
		return file
	}
	if filepath.Base(file) == treesitter.ParserFilename {
		return grammarfixture.ParserPath(t, filepath.Join(wd, filepath.Dir(file)))
	}
	return filepath.Join(wd, file)
}

type mockPkgManager struct {
	ret           iterator.Iterator[string]
	fullPathFiles []string
}

func (m mockPkgManager) LibDir(ctx context.Context, pkg string) (iterator.Iterator[string], error) {
	if m.ret == nil {
		return iterator.FromSlice(m.fullPathFiles), nil
	}
	return m.ret, nil
}

var i atomic.Int32

func newEditFile(t testing.TB, mu sync.Locker, comp *text.Component, content string) (
	cell.Editor, text.Handler,
) {
	i := i.Add(1)
	return newEditFileName(t, mu, comp, content, strconv.Itoa(int(i))+".go")
}

func newEditFileName(
	t testing.TB, mu sync.Locker, comp *text.Component,
	content string, filename string,
) (
	cell.Editor, text.Handler,
) {
	uri, err := workspaceapi.ParseURI("memory:///" + filename)
	require.NoError(t, err)

	tab, err := comp.OpenFileTab(uri, false)
	require.NoError(t, err)

	h, err := comp.Editor(uri)
	require.NoError(t, err)

	start := term.Coordinates{}
	ed := h.CellEditor()
	_, _, _ = ed.Edit(context.Background(), start, start, content)

	{
		ch, err := comp.FlushTab(context.Background(), tab)
		require.NoError(t, err)
		// Release the scheduler mutex while waiting on the flush
		// channel: with async wrapReparse, the channel result is sent
		// from inside a ScheduleNextTick callback that needs the same
		// mutex. Re-acquire before returning so callers can continue
		// to assume the mutex is held.
		mu.Unlock()
		err = <-ch
		mu.Lock()
		require.NoError(t, err)
	}

	err = comp.Browser().Focus().SetContent(tab)
	require.NoError(t, err)

	return ed, h
}

func newWriter(width, height int) *term.StringWriter {
	writer := term.NewStringWriter(width, height)
	writer.ForegroundCh = '#'
	return writer
}

func newTestCase(
	t testing.TB,
	pkgs syntax.PkgManager, width, height int,
	interrupt func(context.Context) error,
) (*sync.Mutex, *text.Component, func()) {
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)

	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)

	mu := new(sync.Mutex)
	cfg := syntax.DefaultConfig()
	cfg.ScheduleNextTick = func(fn func()) bool {
		mu.Lock()
		defer mu.Unlock()
		fn()
		return true
	}
	cfg.ReparseOnErrors = false
	cfg.StrictErrors = true

	ed := vi.Editor(vi.WithStatusBarConfig(true, text.StatusBarConfig{
		Publisher:        texttest.NopEditor(),
		ScheduleNextTick: cfg.ScheduleNextTick,
	}))
	w := workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule)
	tcfg := text.DefaultConfig()
	// Share cfg's mutex-serializing scheduler: text.Component's async
	// flush dispatch and treesitter.Tree's async parser init both call
	// ScheduleNextTick from their own goroutines, and both touch the
	// same cell.Buffer/rawCells. Two independent inline schedulers let
	// those calls run concurrently and race on rawCells' row-meta
	// cache; production only has one scheduler (the host event loop),
	// so the test must mirror that with a single shared mutex.
	tcfg.ScheduleNextTick = cfg.ScheduleNextTick
	tcfg.Syntax = cfg
	tcfg.SyntaxTree = treesitter.New
	tcfg.PkgManager = pkgs
	tcfg.EventPublisher = func(ev term.Event) bool {
		interrupt(context.Background())
		return true
	}
	// give the focus tab icon attr an explicit fg so the
	// term.StringWriter's ForegroundCh substitution renders the
	// tab icon in the layouts under test.
	tcfg.FocusTabIconAttr = term.Attributes{Fg: term.ColorWhite}
	comp, err := text.NewComponent(ed, w, tcfg)
	require.NoError(t, err)

	comp.Browser().Resize(width, height)

	return mu, comp, func() {
		_ = comp.Close()
		_ = scheme.Close()
	}
}

type nopNotifications struct {
	t *testing.T
}

func (n nopNotifications) Notify(
	level notifications.Level, msg string, args ...any,
) (string, error) {
	switch level {
	case notifications.LevelError, notifications.LevelWarn:
		n.t.Logf(msg, args...)
	}
	return "", nil
}

func (n nopNotifications) NotifyOnce(
	level notifications.Level, msg string, args ...any,
) (string, error) {
	return n.Notify(level, msg, args...)
}

func (n nopNotifications) UpdateNotificationProgress(
	id, message string, progress, total int64,
) error {
	return nil
}

func newTree(t testing.TB) (*treesitter.Tree, func()) {
	pkgs := newInstalledPkgManager(t)
	_, _, tree, cleanup := newTreeWithPkgManager(t, pkgs)
	return tree, cleanup
}

func newTreeWithPkgManager(t testing.TB, pkgs syntax.PkgManager) (
	*cell.Buffer, *text.Component, *treesitter.Tree, func(),
) {
	return newTreeWithPkgManagerContent(t, pkgs, fileContent)
}

func newTreeWithPkgManagerContent(
	t testing.TB, pkgs syntax.PkgManager, content string,
) (*cell.Buffer, *text.Component, *treesitter.Tree, func()) {
	buffer, _, tree, comp, cleanup := newTreeWithPkgManagerContentHandler(t, pkgs, content)
	return buffer, comp, tree, cleanup
}

func newTreeWithPkgManagerContentHandler(
	t testing.TB, pkgs syntax.PkgManager, content string,
) (*cell.Buffer, text.Handler, *treesitter.Tree, *text.Component, func()) {
	var wg sync.WaitGroup
	ready := func(context.Context) error {
		wg.Done()
		return nil
	}
	const width, height = 30, 15
	mu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)

	wg.Add(1)
	mu.Lock()
	_, h := newEditFile(t, mu, comp, content)
	mu.Unlock()
	wg.Wait()
	cref, ok := h.(*text.StatusBar)
	require.True(t, ok)

	tree, ok := cref.Buffer().View().(*treesitter.Tree)
	require.True(t, ok)

	return cref.Buffer(), h, tree, comp, cleanup
}

func benchmarkGoContent(lines int) string {
	var content strings.Builder
	content.Grow(lines * 48)
	content.WriteString("package main\n// benchmark\n\nfunc main() {\n")
	for i := range lines {
		fmt.Fprintf(&content, "\tvalue%d := %d\n", i, i)
	}
	content.WriteString("}\n")
	return content.String()
}

const fileContent = `package main

import (
	"fmt"

	"github.com/unstablebuild/blue/cli"
)

func main() {
	fmt.Println("%+v", cli.NewCLI)
	for i := 0; i < 10; i++ {
		fmt.Println("%d", i)
	}
}

const fileContent = "package main\n" +
	"import (\n"+
	"\"fmt\"\n"+
	"\n"+
	"\"github.com/unstablebuild/blue/cli\"\n"
	")"                                      
`
