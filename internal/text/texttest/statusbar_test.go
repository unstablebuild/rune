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

package texttest

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"go.uber.org/goleak"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/text"
)

func TestStatusBarStatus(t *testing.T) {
	testURI, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	mockGit := &differ{}
	cb := func(fn func()) bool {
		fn()
		return true
	}
	cfg := text.StatusBarConfig{
		ScheduleNextTick: cb,
		Workspace:        testURI,
		Publisher:        &TestEditor{},
		Layout: []text.StatusBarComponent{
			{
				Type:       text.StatusBarStatus,
				Attributes: term.Attributes{Fg: term.ColorYellow, Bg: term.ColorRed},
				Template:   "█%s█▓▒░",
			},
		},
		GitService: mockGit,
	}
	buf := cell.NewBuffer()
	buf.WriteString(copy)
	fs := &testFoldsService{}
	fs.view = buf.WithView(fs)
	scroll := component.NewScroll(buf)
	h := newTestHandler(scroll)
	bar := text.WithStatusBar(h, buf, scroll, false, false, cfg)

	bar.Resize(20, 10)
	w := term.NewStringWriter(20, 10)

	tests := []comptest.TestCase{
		{Expected: `
package main        
                    
import (            
    "fmt"           
                    
    "github.com/unst
)                   
                    
func main() {       
██▓▒░               `,
		},
	}
	comptest.TestComponent(t, bar, w, tests)

	cells := w.Cells()
	require.Len(t, cells, 10*20)
	require.Equal(t, '█', cells[180].Ch)
	assert.Equal(t, term.ColorRed, cells[180].Fg)
	assert.Equal(t, term.ColorRed, cells[181].Fg)
	for i := 182; i < 185; i++ {
		assert.Equal(t, term.ColorRed, cells[i].Fg)
	}

	bar.SetStatus("INTERESTING",
		term.Attributes{Bg: term.ColorOlive, Fg: term.ColorMaroon})
	tests = []comptest.TestCase{
		{Expected: `
package main        
                    
import (            
    "fmt"           
                    
    "github.com/unst
)                   
                    
func main() {       
█INTERESTING█▓▒░    `,
		},
	}
	comptest.TestComponent(t, bar, w, tests)

	cells = w.Cells()
	require.Len(t, cells, 10*20)
	assert.Equal(t, term.ColorOlive, cells[180].Fg)
	for i := 181; i < 181+len("INTERESTING"); i++ {
		assert.Equal(t, term.ColorMaroon, cells[i].Fg)
		assert.Equal(t, term.ColorOlive, cells[i].Bg)
	}
	for i := 193; i < 196; i++ {
		assert.Equal(t, term.ColorOlive, cells[i].Fg)
	}
}

func TestStatusBarFilepath(t *testing.T) {
	testURI, err := workspaceapi.ParseURI("memory:///workspace")
	require.NoError(t, err)
	mockGit := &differ{}
	cb := func(fn func()) bool {
		fn()
		return true
	}
	ed := TestEditor{}
	cfg := text.StatusBarConfig{
		ScheduleNextTick: cb,
		Workspace:        testURI,
		Publisher:        &ed,
		GitService:       mockGit,
		Layout: []text.StatusBarComponent{
			{
				Type:       text.StatusBarFilePath,
				Attributes: term.Attributes{Fg: term.ColorYellow, Bg: term.ColorRed},
				Template:   "  %s",
			},
			{
				Type:     text.StatusBarStatus,
				Template: " %s",
			},
		},
	}
	buf := cell.NewBuffer()
	buf.WriteString(copy)
	fs := &testFoldsService{}
	fs.view = buf.WithView(fs)
	scroll := component.NewScroll(buf)
	h := newTestHandler(scroll)
	h.URI, err = workspaceapi.ParseURI("memory:///workspace/relpath.go")
	require.NoError(t, err)

	bar := text.WithStatusBar(h, buf, scroll, false, false, cfg)
	bar.Resize(20, 10)
	w := term.NewStringWriter(20, 10)

	bar.SetStatus("X", term.Attributes{})
	tests := []comptest.TestCase{
		{Expected: `
package main        
                    
import (            
    "fmt"           
                    
    "github.com/unst
)                   
                    
func main() {       
  relpath.go    X   `,
		},
	}
	comptest.TestComponent(t, bar, w, tests)

	cells := w.Cells()
	require.Len(t, cells, 10*20)
	for i := 180; i < 180+len("relpath.go"); i++ {
		assert.Equal(t, term.ColorYellow, cells[i].Fg)
		assert.Equal(t, term.ColorRed, cells[i].Bg)
	}

	buf.InsertString(term.Coordinates{}, "package")
	tests = []comptest.TestCase{
		{Expected: `
packagepackage main 
                    
import (            
    "fmt"           
                    
    "github.com/unst
)                   
                    
func main() {       
  relpath.go   X   `,
		},
	}
	comptest.TestComponent(t, bar, w, tests)

	cells = w.Cells()
	require.Len(t, cells, 10*20)
	for i := 180; i < 180+len("relpath.go"); i++ {
		assert.Equal(t, term.ColorOlive, cells[i].Fg)
		assert.Equal(t, term.ColorRed, cells[i].Bg)
	}

	ok, _ := buf.Undo()
	require.True(t, ok)
	tests = []comptest.TestCase{
		{Expected: `
package main        
                    
import (            
    "fmt"           
                    
    "github.com/unst
)                   
                    
func main() {       
  relpath.go    X   `,
		},
	}
	comptest.TestComponent(t, bar, w, tests)

	cells = w.Cells()
	require.Len(t, cells, 10*20)
	for i := 180; i < 180+len("relpath.go"); i++ {
		assert.Equal(t, term.ColorYellow, cells[i].Fg)
		assert.Equal(t, term.ColorRed, cells[i].Bg)
	}

	ok, _ = buf.Redo()
	require.True(t, ok)
	tests = []comptest.TestCase{
		{Expected: `
packagepackage main 
                    
import (            
    "fmt"           
                    
    "github.com/unst
)                   
                    
func main() {       
  relpath.go   X   `,
		},
	}
	comptest.TestComponent(t, bar, w, tests)

	cells = w.Cells()
	require.Len(t, cells, 10*20)
	for i := 180; i < 180+len("relpath.go"); i++ {
		assert.Equal(t, term.ColorOlive, cells[i].Fg)
		assert.Equal(t, term.ColorRed, cells[i].Bg)
	}

	require.Len(t, ed.subs[textapi.EventTypeFlush], 1)
	assert.False(t, ed.subs[textapi.EventTypeFlush][0].
		Handle(context.Background(),
			textapi.Event{URI: h.URI, Type: textapi.EventTypeFlush}))
	tests = []comptest.TestCase{
		{Expected: `
packagepackage main 
                    
import (            
    "fmt"           
                    
    "github.com/unst
)                   
                    
func main() {       
  relpath.go    X   `,
		},
	}
	comptest.TestComponent(t, bar, w, tests)

	cells = w.Cells()
	require.Len(t, cells, 10*20)
	for i := 180; i < 180+len("relpath.go"); i++ {
		assert.Equal(t, term.ColorYellow, cells[i].Fg)
		assert.Equal(t, term.ColorRed, cells[i].Bg)
	}

}

func TestStatusBarGitService(t *testing.T) {
	testURI, err := workspaceapi.ParseURI("memory:///workspace")
	require.NoError(t, err)
	mockGit := &differ{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	cb := func(fn func()) bool {
		defer wg.Done()
		mu.Lock()
		defer mu.Unlock()
		fn()
		return true
	}
	ed := TestEditor{}
	cfg := text.StatusBarConfig{
		ScheduleNextTick: cb,
		Workspace:        testURI,
		Publisher:        &ed,
		GitService:       mockGit,
		Layout: []text.StatusBarComponent{
			{Template: "   %s  ", Type: text.StatusBarGitShortRef},
			{Template: "  %d", Type: text.StatusBarGitDiffAdded},
			{Template: "   %d ", Type: text.StatusBarGitDiffDeleted},
		},
	}
	buf := cell.NewBuffer()
	buf.WriteString(copy)
	fs := &testFoldsService{}
	fs.view = buf.WithView(fs)
	scroll := component.NewScroll(buf)
	h := newTestHandler(scroll)
	h.URI, err = workspaceapi.ParseURI("memory:///workspace/relpath.go")
	require.NoError(t, err)

	wg.Add(1)
	mu.Lock()
	bar := text.WithStatusBar(h, buf, scroll, false, false, cfg)
	bar.Resize(20, 10)
	w := term.NewStringWriter(20, 10)
	mu.Unlock()

	tests := []comptest.TestCase{
		{Expected: `
package main        
                    
import (            
    "fmt"           
                    
    "github.com/unst
)                   
                    
func main() {       
   main     2    `,
		},
	}
	wg.Wait()
	mu.Lock()
	comptest.TestComponent(t, bar, w, tests)
	mu.Unlock()

	wg.Add(1)
	mu.Lock()
	buf.InsertString(term.Coordinates{}, "package")
	assert.False(t, ed.subs[textapi.EventTypeFlush][0].
		Handle(context.Background(),
			textapi.Event{URI: h.URI, Type: textapi.EventTypeFlush}))
	mu.Unlock()
	tests = []comptest.TestCase{
		{Expected: `
packagepackage main 
                    
import (            
    "fmt"           
                    
    "github.com/unst
)                   
                    
func main() {       
   main     2    `,
		},
	}
	wg.Wait()
	mu.Lock()
	comptest.TestComponent(t, bar, w, tests)
	mu.Unlock()
}

func TestStatusBarCursor(t *testing.T) {
	testURI, err := workspaceapi.ParseURI("memory:///workspace")
	require.NoError(t, err)
	mockGit := &differ{}
	cb := func(fn func()) bool {
		fn()
		return true
	}
	ed := TestEditor{}
	cfg := text.StatusBarConfig{
		ScheduleNextTick: cb,
		Workspace:        testURI,
		Publisher:        &ed,
		GitService:       mockGit,
		Layout: []text.StatusBarComponent{
			{Type: text.StatusBarVoid},
			{Template: "%d:", Type: text.StatusBarCoordinatesCursorX},
			{Template: "%d  ", Type: text.StatusBarCoordinatesCursorY},
			{Template: "%d lines  ", Type: text.StatusBarTotalLines},
			{
				Template:   "%s  ",
				Type:       text.StatusBarLanguage,
				Attributes: term.Attributes{Attrs: term.AttrBold},
			},
		},
	}
	buf := cell.NewBuffer()
	buf.WriteString(copy)
	fs := &testFoldsService{}
	fs.view = buf.WithView(fs)
	scroll := component.NewScroll(buf)
	h := newTestHandler(scroll)
	h.URI, err = workspaceapi.ParseURI("memory:///workspace/relpath.go")
	require.NoError(t, err)

	bar := text.WithStatusBar(h, buf, scroll, false, false, cfg)
	bar.Resize(20, 10)
	w := term.NewStringWriter(20, 10)

	tests := []comptest.TestCase{
		{Expected: `
package main        
                    
import (            
    "fmt"           
                    
    "github.com/unst
)                   
                    
func main() {       
1:1  22 lines  unkno`,
		},
	}
	comptest.TestComponent(t, bar, w, tests)

	h.cursor = 10
	_, handled := bar.Handle(term.Event{Ch: 'a', Type: term.EventKey})
	assert.True(t, handled)
	tests = []comptest.TestCase{
		{Expected: `
package main        
                    
import (            
    "fmt"           
                    
    "github.com/unst
)                   
                    
func main() {       
1:11  22 lines  unkn`,
		},
	}
	comptest.TestComponent(t, bar, w, tests)
}

func TestStatusBarSyntax(t *testing.T) {
	testURI, err := workspaceapi.ParseURI("memory:///workspace")
	require.NoError(t, err)
	mockGit := &differ{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	cb := func(fn func()) bool {
		defer wg.Done()
		mu.Lock()
		defer mu.Unlock()
		fn()
		return true
	}
	ed := TestEditor{}
	cfg := text.StatusBarConfig{
		ScheduleNextTick: cb,
		Workspace:        testURI,
		Publisher:        &ed,
		GitService:       mockGit,
		ErrorColor:       term.ColorMaroon,
		Layout: []text.StatusBarComponent{
			{Type: text.StatusBarVoid},
			{
				Template:   "%s  ",
				Type:       text.StatusBarLanguage,
				Attributes: term.Attributes{Fg: term.ColorYellow, Bg: term.ColorRed},
			},
		},
	}
	buf := cell.NewBuffer()
	buf.WriteString(copy)
	fs := &testFoldsService{}
	fs.view = buf.WithView(fs)
	scroll := component.NewScroll(buf)
	h := newTestHandler(scroll)
	h.URI, err = workspaceapi.ParseURI("memory:///workspace/relpath.go")
	require.NoError(t, err)

	state := make(chan syntax.State)
	mockSyntax := &testSyntax{ch: state}
	view := buf.WithView(mockSyntax)
	mockSyntax.view = view

	go func() {
		state <- syntax.State{}
	}()
	wg.Add(1)
	mu.Lock()
	bar := text.WithStatusBar(h, buf, scroll, false, false, cfg)
	bar.Resize(20, 10)
	w := term.NewStringWriter(20, 10)
	mu.Unlock()

	tests := []comptest.TestCase{
		{Expected: `
package main        
                    
import (            
    "fmt"           
                    
    "github.com/unst
)                   
                    
func main() {       
           unknown  `,
		},
	}
	wg.Wait()
	mu.Lock()
	comptest.TestComponent(t, bar, w, tests)
	mu.Unlock()

	wg.Add(1)
	mu.Lock()
	state <- syntax.State{LangID: "GO"}
	mu.Unlock()
	tests = []comptest.TestCase{
		{Expected: `
package main        
                    
import (            
    "fmt"           
                    
    "github.com/unst
)                   
                    
func main() {       
                GO  `,
		},
	}
	wg.Wait()
	mu.Lock()
	comptest.TestComponent(t, bar, w, tests)
	mu.Unlock()

	cells := w.Cells()
	require.Len(t, cells, 10*20)
	for i := 196; i < 196+len("GO"); i++ {
		assert.Equal(t, term.ColorYellow, cells[i].Fg)
		assert.Equal(t, term.ColorRed, cells[i].Bg)
	}

	wg.Add(1)
	mu.Lock()
	state <- syntax.State{ParserError: "OOPS", LangID: "GO"}
	mu.Unlock()
	tests = []comptest.TestCase{
		{Expected: `
package main        
                    
import (            
    "fmt"           
                    
    "github.com/unst
)                   
                    
func main() {       
                GO  `,
		},
	}
	wg.Wait()
	mu.Lock()
	comptest.TestComponent(t, bar, w, tests)
	mu.Unlock()

	cells = w.Cells()
	require.Len(t, cells, 10*20)
	for i := 196; i < 196+len("GO"); i++ {
		assert.Equal(t, term.ColorMaroon, cells[i].Fg, i)
		assert.Equal(t, term.ColorRed, cells[i].Bg, i)
	}

	require.NoError(t, bar.Close())
}

type testSyntax struct {
	ch   chan syntax.State
	view cell.View
}

func (s testSyntax) State() iterator.Iterator[syntax.State] {
	return iterator.FromFunc(func(ctx context.Context) (syntax.State, bool, error) {
		state, ok := <-s.ch
		return state, ok, nil
	}, func() error {
		close(s.ch)
		return nil
	})
}

func (s testSyntax) Rows() int {
	return s.view.Rows()
}
func (s testSyntax) Columns(row int) int {
	return s.view.Columns(row)
}
func (s testSyntax) Cell(pos term.Coordinates) (term.Cell, bool) {
	return s.view.Cell(pos)
}
func (s testSyntax) RawCells() [][]term.Cell {
	return s.view.RawCells()
}

func (s testSyntax) String() string {
	return s.view.String()
}

func TestStatusBarRestyle(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	tests := []struct {
		name    string
		initial string
		run     func(t *testing.T, styles *text.StatusBarStyles, f *restyleBar)
	}{
		{
			name:    "draws the fields of the new layout",
			initial: `{{ .Status }}`,
			run: func(t *testing.T, styles *text.StatusBarStyles, f *restyleBar) {
				styles.Restyle(restyleStyle(t, `<{{ .Filepath }}>{{ .Status }}`), nil)
				assert.Contains(t, f.row(), "<relpath.go")
				assert.Contains(t, f.row(), "NORMAL")
			},
		},
		{
			name:    "keeps the status message",
			initial: `{{ .Status }}`,
			run: func(t *testing.T, styles *text.StatusBarStyles, f *restyleBar) {
				styles.Restyle(restyleStyle(t, `[{{ .Status }}]`), nil)
				assert.Equal(t, "[NORMAL]", strings.TrimSpace(f.row()))
			},
		},
		{
			name:    "draws the new background color",
			initial: `{{ .Status }}`,
			run: func(t *testing.T, styles *text.StatusBarStyles, f *restyleBar) {
				style := restyleStyle(t, `{{ .Status }}`)
				style.BackgroundColor = term.ColorBlue
				styles.Restyle(style, nil)
				cells := f.cells()
				for i := range len("NORMAL") {
					assert.Equal(t, term.ColorBlue, cells[i].Bg, i)
				}
			},
		},
		{
			name:    "draws parser errors in the new error color",
			initial: `{{ .Language }}`,
			run: func(t *testing.T, styles *text.StatusBarStyles, f *restyleBar) {
				f.sendSyntax(t, syntax.State{LangID: "GO", ParserError: "oops"}, "GO")
				style := restyleStyle(t, `{{ .Language }}`)
				style.ErrorColor = term.ColorBlue
				styles.Restyle(style, nil)
				cells := f.cells()
				for i := range len("GO") {
					assert.Equal(t, term.ColorBlue, cells[i].Fg, i)
				}
			},
		},
		{
			name:    "keeps the language",
			initial: `{{ .Language }}`,
			run: func(t *testing.T, styles *text.StatusBarStyles, f *restyleBar) {
				f.sendSyntax(t, syntax.State{LangID: "GO"}, "GO")
				styles.Restyle(restyleStyle(t, `[{{ .Language }}]`), nil)
				assert.Equal(t, "[GO]", strings.TrimSpace(f.row()))
			},
		},
		{
			name:    "keeps the git branch",
			initial: `{{ .GitShortRef }}`,
			run: func(t *testing.T, styles *text.StatusBarStyles, f *restyleBar) {
				f.settle(t, "main")
				styles.Restyle(restyleStyle(t, `<{{ .GitShortRef }}>`), nil)
				assert.Equal(t, "<main>", strings.TrimSpace(f.row()))
			},
		},
		{
			name:    "streams the language once it is added",
			initial: `{{ .Status }}`,
			run: func(t *testing.T, styles *text.StatusBarStyles, f *restyleBar) {
				assert.Zero(t, f.streams.Load())
				styles.Restyle(restyleStyle(t, `{{ .Language }}`), nil)
				styles.Restyle(restyleStyle(t, `[{{ .Language }}]`), nil)
				f.sendSyntax(t, syntax.State{LangID: "GO"}, "[GO]")
				// The bar opens the stream from a goroutine; a state only
				// reaches it once the stream is open.
				assert.Equal(t, int32(1), f.streams.Load())
			},
		},
		{
			name:    "keeps streaming the language it streamed",
			initial: `{{ .Language }}`,
			run: func(t *testing.T, styles *text.StatusBarStyles, f *restyleBar) {
				styles.Restyle(restyleStyle(t, `{{ .Status }}`), nil)
				styles.Restyle(restyleStyle(t, `[{{ .Language }}]`), nil)
				f.sendSyntax(t, syntax.State{LangID: "GO"}, "[GO]")
				assert.Equal(t, int32(1), f.streams.Load())
			},
		},
		{
			name:    "restyles no closed bar",
			initial: `{{ .Status }}`,
			run: func(t *testing.T, styles *text.StatusBarStyles, f *restyleBar) {
				before := f.row()
				require.NoError(t, f.bar.Close())
				styles.Restyle(restyleStyle(t, `[{{ .Status }}]`), nil)
				assert.Equal(t, before, f.row())
			},
		},
		{
			name:    "creates bars with the style restyled last",
			initial: `{{ .Status }}`,
			run: func(t *testing.T, styles *text.StatusBarStyles, _ *restyleBar) {
				styles.Restyle(restyleStyle(t, `[{{ .Status }}]`), nil)
				f := newRestyleBar(t, styles)
				assert.Equal(t, "[NORMAL]", strings.TrimSpace(f.row()))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			styles := text.NewStatusBarStyles(restyleStyle(t, tt.initial))
			assert.Equal(t, restyleStyle(t, tt.initial), styles.Current())
			tt.run(t, styles, newRestyleBar(t, styles))
		})
	}
}

func restyleStyle(t *testing.T, layout string) text.StatusBarStyle {
	t.Helper()
	components, err := text.ParseStatusBarLayout(layout)
	require.NoError(t, err)
	return text.StatusBarStyle{Layout: components, ErrorColor: term.ColorMaroon}
}

// restyleBar is a 40 columns wide status bar of relpath.go, with status
// NORMAL, whose scheduled ticks run when the test settles it, as they
// run on the event loop.
type restyleBar struct {
	bar     *text.StatusBar
	ticks   chan func()
	syntax  chan syntax.State
	streams *atomic.Int32
}

func newRestyleBar(t *testing.T, styles *text.StatusBarStyles) *restyleBar {
	t.Helper()
	workspace, err := workspaceapi.ParseURI("memory:///workspace")
	require.NoError(t, err)
	f := &restyleBar{
		ticks:   make(chan func(), 64),
		syntax:  make(chan syntax.State),
		streams: new(atomic.Int32),
	}
	buf := cell.NewBuffer()
	buf.WriteString("first line\n")
	s := &streamedSyntax{ch: f.syntax, streams: f.streams}
	s.View = buf.WithView(s)
	scroll := component.NewScroll(buf)
	h := newTestHandler(scroll)
	h.URI, err = workspaceapi.ParseURI("memory:///workspace/relpath.go")
	require.NoError(t, err)
	f.bar = text.WithStatusBar(h, buf, scroll, false, false, text.StatusBarConfig{
		ScheduleNextTick: func(fn func()) bool {
			f.ticks <- fn
			return true
		},
		Workspace:  workspace,
		Publisher:  &TestEditor{},
		GitService: differ{},
		Styles:     styles,
	})
	t.Cleanup(func() { assert.NoError(t, f.bar.Close()) })
	f.bar.SetStatus("NORMAL", term.Attributes{})
	f.bar.Resize(40, 3)
	return f
}

func (f *restyleBar) draw() *term.StringWriter {
	w := term.NewStringWriter(40, 3)
	f.bar.Draw(w)
	_ = w.Flush()
	return w
}

// row returns the bar's row.
func (f *restyleBar) row() string {
	rows := strings.Split(f.draw().String(), "\n")
	return rows[len(rows)-1]
}

// cells returns the cells of the bar's row.
func (f *restyleBar) cells() []term.Cell {
	cells := f.draw().Cells()
	return cells[len(cells)-40:]
}

// settle runs the scheduled ticks until the bar's row contains want.
func (f *restyleBar) settle(t *testing.T, want string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for !strings.Contains(f.row(), want) {
		select {
		case fn := <-f.ticks:
			fn()
		case <-timeout:
			t.Fatalf("timed out waiting for %q, last row %q", want, f.row())
		}
	}
}

// sendSyntax streams state to the bar and settles it until its row
// contains want.
func (f *restyleBar) sendSyntax(t *testing.T, state syntax.State, want string) {
	t.Helper()
	select {
	case f.syntax <- state:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out streaming the syntax state")
	}
	f.settle(t, want)
}

// streamedSyntax is a syntax service whose states are sent on ch, which
// counts the streams opened on it.
type streamedSyntax struct {
	cell.View
	ch      chan syntax.State
	streams *atomic.Int32
}

func (s *streamedSyntax) State() iterator.Iterator[syntax.State] {
	s.streams.Add(1)
	return iterator.FromFunc(func(ctx context.Context) (syntax.State, bool, error) {
		select {
		case state := <-s.ch:
			return state, true, nil
		case <-ctx.Done():
			return syntax.State{}, false, ctx.Err()
		}
	}, func() error { return nil })
}

func TestStatusBarProgressiveHiding(t *testing.T) {
	testURI, err := workspaceapi.ParseURI("memory:///workspace")
	require.NoError(t, err)
	mockGit := &differ{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	cb := func(fn func()) bool {
		defer wg.Done()
		mu.Lock()
		defer mu.Unlock()
		fn()
		return true
	}
	ed := TestEditor{}

	// Layout: filepath + git branch + git add/del on left,
	// total lines + language on right.
	cfg := text.StatusBarConfig{
		ScheduleNextTick: cb,
		Workspace:        testURI,
		Publisher:        &ed,
		GitService:       mockGit,
		Layout: []text.StatusBarComponent{
			{Template: " %s", Type: text.StatusBarFilePath},
			{Template: "  %s", Type: text.StatusBarGitShortRef},
			{Template: " +%d", Type: text.StatusBarGitDiffAdded},
			{Template: " -%d", Type: text.StatusBarGitDiffDeleted},
			{Type: text.StatusBarVoid},
			{Template: "%d lines ", Type: text.StatusBarTotalLines},
			{Template: "%s ", Type: text.StatusBarLanguage},
		},
	}
	buf := cell.NewBuffer()
	buf.WriteString(copy)
	fs := &testFoldsService{}
	fs.view = buf.WithView(fs)

	state := make(chan syntax.State)
	mockSyntax := &testSyntax{ch: state}
	view := buf.WithView(mockSyntax)
	mockSyntax.view = view

	scroll := component.NewScroll(buf)
	h := newTestHandler(scroll)
	h.URI, err = workspaceapi.ParseURI("memory:///workspace/main.go")
	require.NoError(t, err)

	go func() {
		state <- syntax.State{LangID: "go"}
	}()
	wg.Add(2)
	mu.Lock()
	bar := text.WithStatusBar(h, buf, scroll, false, false, cfg)
	mu.Unlock()
	wg.Wait()

	// Component widths:
	// filepath " main.go   " = 11 (template " %s", flushed suffix "   ")
	// branch   "  main"     = 6  (template "  %s")
	// git add  " +2"        = 3  (template " +%d")
	// git del  " -2"        = 3  (template " -%d")
	// Left total (all): 11 + 6 + 3 + 3 = 23
	// total lines "22 lines " = 9  (template "%d lines ")
	// language    "go "       = 3  (template "%s ")
	// Right total: 9 + 3 = 12
	// Grand total: 23 + 12 = 35

	// At 60 columns, everything fits with a 25-column gap.
	mu.Lock()
	bar.Resize(60, 10)
	w := term.NewStringWriter(60, 10)
	tests := []comptest.TestCase{
		{Expected: `
package main                                                
                                                            
import (                                                    
    "fmt"                                                   
                                                            
    "github.com/unstablebuild/blue/cli"                     
)                                                           
                                                            
func main() {                                               
 main.go     main +2 -2                         22 lines go `},
	}
	comptest.TestComponent(t, bar, w, tests)
	mu.Unlock()

	// At 35 columns, everything fits exactly (23 + 12 = 35), no gap.
	mu.Lock()
	bar.Resize(35, 10)
	w = term.NewStringWriter(35, 10)
	tests = []comptest.TestCase{
		{Expected: `
package main                       
                                   
import (                           
    "fmt"                          
                                   
    "github.com/unstablebuild/blue/
)                                  
                                   
func main() {                      
 main.go     main +2 -222 lines go `},
	}
	comptest.TestComponent(t, bar, w, tests)
	mu.Unlock()

	// At 29 columns, only branch is hidden (17 + 12 = 29), no gap.
	mu.Lock()
	bar.Resize(29, 10)
	w = term.NewStringWriter(29, 10)
	tests = []comptest.TestCase{
		{Expected: `
package main                 
                             
import (                     
    "fmt"                    
                             
    "github.com/unstablebuild
)                            
                             
func main() {                
 main.go    +2 -222 lines go `},
	}
	comptest.TestComponent(t, bar, w, tests)
	mu.Unlock()

	// At 28 columns, branch + stats hidden (11 + 12 = 23 <= 28), 5-col gap.
	mu.Lock()
	bar.Resize(28, 10)
	w = term.NewStringWriter(28, 10)
	tests = []comptest.TestCase{
		{Expected: `
package main                
                            
import (                    
    "fmt"                   
                            
    "github.com/unstablebuil
)                           
                            
func main() {               
 main.go        22 lines go `},
	}
	comptest.TestComponent(t, bar, w, tests)
	mu.Unlock()

	// At 20 columns, branch + stats + language hidden (11 + 9 = 20), no gap.
	mu.Lock()
	bar.Resize(20, 10)
	w = term.NewStringWriter(20, 10)
	tests = []comptest.TestCase{
		{Expected: `
package main        
                    
import (            
    "fmt"           
                    
    "github.com/unst
)                   
                    
func main() {       
 main.go   22 lines `},
	}
	comptest.TestComponent(t, bar, w, tests)
	mu.Unlock()

	// At 15 columns, all components hidden except filepath.
	mu.Lock()
	bar.Resize(15, 10)
	w = term.NewStringWriter(15, 10)
	tests = []comptest.TestCase{
		{Expected: `
package main   
               
import (       
    "fmt"      
               
    "github.com
)              
               
func main() {  
 main.go       `},
	}
	comptest.TestComponent(t, bar, w, tests)
	mu.Unlock()

	// Resize back to full width: everything should be restored.
	mu.Lock()
	bar.Resize(60, 10)
	w = term.NewStringWriter(60, 10)
	tests = []comptest.TestCase{
		{Expected: `
package main                                                
                                                            
import (                                                    
    "fmt"                                                   
                                                            
    "github.com/unstablebuild/blue/cli"                     
)                                                           
                                                            
func main() {                                               
 main.go     main +2 -2                         22 lines go `},
	}
	comptest.TestComponent(t, bar, w, tests)
	mu.Unlock()

	require.NoError(t, bar.Close())
}

func BenchmarkStatusBarMoveCursorSmall(b *testing.B) {
	benchmarkStatusBar(b, 10, 10, true)
}

func BenchmarkStatusBarMoveCursorMedium(b *testing.B) {
	benchmarkStatusBar(b, 100, 100, true)
}

func BenchmarkStatusBarMoveCursorLarge(b *testing.B) {
	benchmarkStatusBar(b, 1000, 1000, true)
}

func BenchmarkStatusBarRenderAllSmall(b *testing.B) {
	benchmarkStatusBar(b, 10, 10, false)
}

func BenchmarkStatusBarRenderAllMedium(b *testing.B) {
	benchmarkStatusBar(b, 100, 100, false)
}

func BenchmarkStatusBarRenderAllLarge(b *testing.B) {
	benchmarkStatusBar(b, 1000, 1000, false)
}

func benchmarkStatusBar(b *testing.B, width, height int, moveCursor bool) {
	buf := cell.NewBuffer()
	buf.WriteString(copy)
	fs := &testFoldsService{}
	fs.view = buf.WithView(fs)
	scroll := component.NewScroll(buf)
	h := newTestHandler(scroll)
	var wg sync.WaitGroup

	uri, err := workspaceapi.ParseURI("memory:///tmp/my_workspace")
	require.NoError(b, err)

	cfg := text.StatusBarConfig{
		Workspace: uri,
		ScheduleNextTick: func(fn func()) bool {
			fn()
			wg.Done()
			return true
		},
		Publisher:  &TestEditor{},
		GitService: &differ{},
	}

	// do not perform async ops on every draw:
	// if we move cursor before every draw, this would require wg.Add, wg.Wait
	// on every iteration, hiding some of the ops we want to analyze
	moveCursorModulo := 1
	var layout string
	if moveCursor {
		moveCursorModulo = 2
		layout = `█{{ .Status | bg "red" | fg "black" | bold }}█▓▒░  {{ .Filepath }}   {{ .ShiftRight }} {{ .CursorColumn }}:{{ .CursorLine }}  {{ .TotalLines }}  {{ .Language | bold }}  `
	} else {
		wg.Add(1)
		layout = `█{{ .Status | bg "red" | fg "black" | bold }}█▓▒░  {{ .Filepath }}   {{ .GitShortRef | italic }}   {{ .GitDiffAdd | fg "green" }}   {{ .GitDiffDel | fg "red" }} {{ .ShiftRight }} {{ .CursorColumn }}:{{ .CursorLine }}  {{ .TotalLines }}  {{ .Language | bold }}  `
	}
	cfg.Layout, err = text.ParseStatusBarLayout(layout)
	require.NoError(b, err)

	bar := text.WithStatusBar(h, buf, scroll, false, false, cfg)
	bar.SetStatus(" NORMAL", term.Attributes{})
	bar.Resize(width, height)
	bar.Draw(term.NoopWriter{})
	wg.Wait()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.cursor = i % moveCursorModulo
		bar.Handle(term.Event{})
		bar.Draw(term.NoopWriter{})
	}
}

func TestStatusBarPointerLeaves(t *testing.T) {
	t.Parallel()
	uri, err := workspaceapi.ParseURI("memory:///tmp/my_workspace")
	require.NoError(t, err)
	layout, err := text.ParseStatusBarLayout(`█{{ .Status | bg "red" }}█▓▒░  {{ .Filepath }}` +
		`   {{ .ShiftRight }} {{ .CursorColumn }}:{{ .CursorLine }}  {{ .TotalLines }}  {{ .Language }}  `)
	require.NoError(t, err)
	newBar := func() *text.StatusBar {
		ed := newPointerEditor()
		bar := text.WithStatusBar(ed, ed.buf, ed.Scroll, false, false, text.StatusBarConfig{
			Workspace:        uri,
			ScheduleNextTick: func(fn func()) bool { fn(); return true },
			Publisher:        &TestEditor{},
			Layout:           layout,
		})
		bar.SetStatus(" 日本語 🙂 NORMAL", term.Attributes{})
		return bar
	}
	for _, size := range pointerSizes {
		handlertest.RunPointerLeaves(t, newBar(), size.width, size.height)
	}
	requirePointerLeavesKeepState(t, newBar(), 40, 10,
		term.Coordinates{X: 10, Y: 2}, term.Coordinates{X: 16, Y: 4}, nil)
}
