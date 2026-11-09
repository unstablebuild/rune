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
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/mouse"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/text"
)

func TestAuxBarDrawLinesRelative(t *testing.T) {
	buf := cell.NewBuffer()
	buf.WriteString(copy)
	fs := &testFoldsService{}
	fs.view = buf.WithView(fs)
	scroll := component.NewScroll(buf)
	h := newTestHandler(scroll)
	cb := func(fn func()) bool {
		fn()
		return true
	}

	cfg := text.AuxBarConfig{LinesEnabled: true, ScheduleNextTick: cb}
	bar := text.WithAuxBar(h, buf, scroll, cfg)
	bar.Resize(20, 10)
	w := term.NewStringWriter(20, 10)

	tests := []comptest.TestCase{
		{Expected: `
1  package main     
1                   
2  import (         
3      "fmt"        
4                   
5      "github.com/u
6  )                
7                   
8  func main() {    
9      fmt.Println("`,
		},
		{
			Action: func() {
				require.True(t, scroll.SeekDown())
			},
			Expected: `
2                   
1  import (         
2      "fmt"        
3                   
4      "github.com/u
5  )                
6                   
7  func main() {    
8      fmt.Println("
9      for i := 0; i`,
		},
	}
	comptest.TestComponent(t, bar, w, tests)
}

func TestAuxBarDrawLinesAbsolute(t *testing.T) {
	buf := cell.NewBuffer()
	buf.WriteString(copy)
	fs := &testFoldsService{}
	fs.view = buf.WithView(fs)
	scroll := component.NewScroll(buf)
	h := newTestHandler(scroll)
	cb := func(fn func()) bool {
		fn()
		return true
	}

	cfg := text.AuxBarConfig{LinesEnabled: true, AbsoluteLines: true, ScheduleNextTick: cb}
	bar := text.WithAuxBar(h, buf, scroll, cfg)
	bar.Resize(20, 10)
	w := term.NewStringWriter(20, 10)

	tests := []comptest.TestCase{
		{Expected: `
1  package main     
2                   
3  import (         
4      "fmt"        
5                   
6      "github.com/u
7  )                
8                   
9  func main() {    
10     fmt.Println("`,
		},
		{
			Action: func() {
				require.True(t, scroll.SeekDown())
			},
			Expected: `
2                   
3  import (         
4      "fmt"        
5                   
6      "github.com/u
7  )                
8                   
9  func main() {    
10     fmt.Println("
11     for i := 0; i`,
		},
	}
	comptest.TestComponent(t, bar, w, tests)
}

func TestAuxBarDrawLinesAbsolute_NoFoldsService(t *testing.T) {
	buf := cell.NewBuffer()
	buf.WriteString(copy)
	scroll := component.NewScroll(buf)
	h := newTestHandler(scroll)
	cb := func(fn func()) bool {
		fn()
		return true
	}

	cfg := text.AuxBarConfig{
		LinesEnabled:     true,
		AbsoluteLines:    true,
		FoldsEnabled:     true,
		ScheduleNextTick: cb,
	}
	bar := text.WithAuxBar(h, buf, scroll, cfg)
	bar.Resize(20, 10)
	w := term.NewStringWriter(20, 10)

	tests := []comptest.TestCase{
		{Expected: `
1    package main   
2                   
3    import (       
4        "fmt"      
5                   
6        "github.com
7    )              
8                   
9    func main() {  
10       fmt.Println`,
		},
	}
	comptest.TestComponent(t, bar, w, tests)
}

func TestAuxBarDrawLines_FoldsServiceNotReady(t *testing.T) {
	buf := cell.NewBuffer()
	buf.WriteString(copy)
	fs := &blockingFoldsService{}
	fs.view = buf.WithView(fs)
	scroll := component.NewScroll(buf)
	h := newTestHandler(scroll)
	cb := func(fn func()) bool {
		fn()
		return true
	}

	cfg := text.AuxBarConfig{
		LinesEnabled:     true,
		AbsoluteLines:    true,
		FoldsEnabled:     true,
		ScheduleNextTick: cb,
	}
	bar := text.WithAuxBar(h, buf, scroll, cfg)
	bar.Resize(20, 10)
	w := term.NewStringWriter(20, 10)

	tests := []comptest.TestCase{
		{Expected: `
1    package main   
2                   
3    import (       
4        "fmt"      
5                   
6        "github.com
7    )              
8                   
9    func main() {  
10       fmt.Println`,
		},
	}
	comptest.TestComponent(t, bar, w, tests)
}

func TestAuxBarResizeLinesRelative_FoldsServiceNotReady(t *testing.T) {
	buf := cell.NewBuffer()
	buf.WriteString(copy)
	fs := &blockingFoldsService{}
	fs.view = buf.WithView(fs)
	scroll := component.NewScroll(buf)
	h := newTestHandler(scroll)
	cb := func(fn func()) bool {
		fn()
		return true
	}

	cfg := text.AuxBarConfig{
		LinesEnabled:     true,
		FoldsEnabled:     true,
		ScheduleNextTick: cb,
	}
	bar := text.WithAuxBar(h, buf, scroll, cfg)
	bar.Resize(20, 5)
	w := term.NewStringWriter(20, 10)

	bar.Resize(20, 10)
	tests := []comptest.TestCase{
		{Expected: `
1    package main   
1                   
2    import (       
3        "fmt"      
4                   
5        "github.com
6    )              
7                   
8    func main() {  
9        fmt.Println`,
		},
	}
	comptest.TestComponent(t, bar, w, tests)
}

func TestAuxBarDrawFolds(t *testing.T) {
	buf := cell.NewBuffer()
	buf.WriteString(copy)
	fs := &testFoldsService{}
	fs.view = buf.WithView(fs)
	scroll := component.NewScroll(buf)
	h := newTestHandler(scroll)
	var wg sync.WaitGroup
	var mu sync.Mutex
	cb := func(fn func()) bool {
		mu.Lock()
		defer mu.Unlock()
		fn()
		wg.Done()
		return true
	}

	wg.Add(1)
	mu.Lock()
	cfg := text.AuxBarConfig{
		GitEnabled:       true, // test that if no lines => disabled
		FoldsEnabled:     true,
		ScheduleNextTick: cb,
	}
	bar := text.WithAuxBar(h, buf, scroll, cfg)
	bar.Resize(20, 10)
	mu.Unlock()
	w := term.NewStringWriter(20, 10)

	tests := []comptest.TestCase{
		{Expected: `
  package main      
                    
 import (          
      "fmt"         
                    
      "github.com/un
  )                 
                    
 func main() {     
      fmt.Println("%`,
		},
	}
	wg.Wait()

	mu.Lock()
	comptest.TestComponent(t, bar, w, tests)
	mu.Unlock()
	require.True(t, scroll.SeekDown())

	tests = []comptest.TestCase{
		{Expected: `
                    
 import (          
      "fmt"         
                    
      "github.com/un
  )                 
                    
 func main() {     
      fmt.Println("%
      for i := 0; i `,
		},
	}
	mu.Lock()
	comptest.TestComponent(t, bar, w, tests)
	mu.Unlock()

	wg.Add(1)
	_, handled := bar.Handle(
		term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 0, MouseY: 1})
	require.True(t, handled)

	tests = []comptest.TestCase{
		{Expected: `
                    
 import ( [5 lines]
                    
 func main() {     
      fmt.Println("%
      for i := 0; i 
          fmt.Printl
      }             
  }                 
                    `,
		},
	}

	wg.Wait()
	mu.Lock()
	comptest.TestComponent(t, bar, w, tests)
	mu.Unlock()

	wg.Add(1)
	_, handled = bar.Handle(
		term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 0, MouseY: 3})
	require.True(t, handled)

	tests = []comptest.TestCase{
		{Expected: `
                    
 import ( [5 lines]
                    
 func main() { [6 l
                    
  const fileContent 
      "import (\n"+ 
      "\"fmt\"\n"+  
      "\n"+         
      "\"github.com/`,
		},
	}

	wg.Wait()
	mu.Lock()
	comptest.TestComponent(t, bar, w, tests)
	mu.Unlock()

	wg.Add(1)
	_, handled = bar.Handle(
		term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 0, MouseY: 1})
	require.True(t, handled)

	tests = []comptest.TestCase{
		{Expected: `
                    
 import (          
      "fmt"         
                    
      "github.com/un
  )                 
                    
 func main() { [6 l
                    
  const fileContent `,
		},
	}

	wg.Wait()
	mu.Lock()
	comptest.TestComponent(t, bar, w, tests)
	mu.Unlock()

	// nothing happens if we click outside of line
	_, handled = bar.Handle(
		term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 2, MouseY: 1})
	require.True(t, handled)

	tests = []comptest.TestCase{
		{Expected: `
                    
 import (          
      "fmt"         
                    
      "github.com/un
  )                 
                    
 func main() { [6 l
                    
  const fileContent `,
		},
	}

	wg.Wait()
	mu.Lock()
	comptest.TestComponent(t, bar, w, tests)
	mu.Unlock()
}

func TestAuxBarEditContextCancelledKeepsFolds(t *testing.T) {
	const foldGlyph = "\uf44b"

	buf := cell.NewBuffer()
	buf.WriteString(copy)
	fs := &testFoldsService{}
	fs.view = buf.WithView(fs)
	scroll := component.NewScroll(buf)
	h := newTestHandler(scroll)

	var mu sync.Mutex
	var pending sync.WaitGroup
	cb := func(fn func()) bool {
		mu.Lock()
		defer mu.Unlock()
		fn()
		pending.Done()
		return true
	}

	pending.Add(1)
	mu.Lock()
	bar := text.WithAuxBar(h, buf, scroll, text.AuxBarConfig{
		FoldsEnabled:     true,
		ScheduleNextTick: cb,
	})
	bar.Resize(20, 10)
	mu.Unlock()
	pending.Wait()

	render := func() string {
		mu.Lock()
		defer mu.Unlock()
		w := term.NewStringWriter(20, 10)
		require.NoError(t, w.Clear(term.Attributes{}))
		bar.Draw(w)
		require.NoError(t, w.Flush())
		return w.String()
	}
	require.Contains(t, render(), foldGlyph, "folds must render before the edit")

	// The edit has to change the row count, which is what makes the bar
	// rebuild at all.
	pending.Add(1)
	ctx, cancel := context.WithCancel(context.Background())
	buf.Edit(ctx, term.Coordinates{Y: 1}, term.Coordinates{Y: 2}, "")
	cancel()
	pending.Wait()

	require.Contains(t, render(), foldGlyph,
		"folds must survive an edit whose context is already cancelled")
}

func TestAuxBarGitEventSubscriptions(t *testing.T) {
	fixture := newAuxBarEventFixture(t)
	defer fixture.close(t)

	require.ElementsMatch(t, []textapi.EventType{
		textapi.EventTypeFocus,
		textapi.EventTypeChange,
		textapi.EventTypeRename,
		textapi.EventTypeCreate,
		textapi.EventTypeFlush,
	}, fixture.publisher.events)
}

func TestAuxBarGitEventInvalidation(t *testing.T) {
	fixture := newAuxBarEventFixture(t)
	defer fixture.close(t)

	fixture.publisher.dispatch(textapi.Event{
		Type: textapi.EventTypeFocus,
		URI:  fixture.uri,
	})
	fixture.awaitScheduled(t)
	require.EqualValues(t, 1, fixture.differ.calls.Load())
	requireNoDiffCall(t, fixture.differ.called)

	for _, eventType := range []textapi.EventType{
		textapi.EventTypeChange,
		textapi.EventTypeRename,
		textapi.EventTypeCreate,
	} {
		fixture.publisher.dispatch(textapi.Event{
			Type: eventType,
			URI:  fixture.uri,
		})
		require.EqualValues(t, 1, fixture.differ.calls.Load())
	}
	fixture.requireQuiet(t)

	fixture.publisher.dispatch(textapi.Event{
		Type: textapi.EventTypeFocus,
		URI:  fixture.uri,
	})
	fixture.awaitDiffCall(t, 2)
	fixture.awaitScheduled(t)

	fixture.publisher.dispatch(textapi.Event{
		Type: textapi.EventTypeFlush,
		URI:  fixture.uri,
	})
	fixture.awaitDiffCall(t, 3)
	fixture.awaitScheduled(t)

	otherURI, err := workspaceapi.ParseURI("file:///workspace/other.go")
	require.NoError(t, err)
	fixture.publisher.dispatch(textapi.Event{
		Type: textapi.EventTypeChange,
		URI:  otherURI,
	})
	require.EqualValues(t, 3, fixture.differ.calls.Load())
	fixture.requireQuiet(t)
}

type auxBarEventFixture struct {
	uri       workspaceapi.URI
	bar       text.Handler
	publisher *auxBarEventPublisher
	differ    *eventDiffer
	scheduled chan struct{}
}

func newAuxBarEventFixture(t *testing.T) *auxBarEventFixture {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file:///workspace/file.go")
	require.NoError(t, err)

	buf := cell.NewBuffer()
	buf.WriteString(copy)
	scroll := component.NewScroll(buf)
	h := newTestHandler(scroll)
	h.URI = uri
	fixture := &auxBarEventFixture{
		uri:       uri,
		publisher: new(auxBarEventPublisher),
		differ:    newEventDiffer(),
		scheduled: make(chan struct{}, 8),
	}
	fixture.bar = text.WithAuxBar(h, buf, scroll, text.AuxBarConfig{
		GitEnabled:       true,
		LinesEnabled:     true,
		ScheduleNextTick: fixture.scheduleNextTick,
		Publisher:        fixture.publisher,
		CommandRegistry:  auxBarCommandRegistry{},
		Service:          fixture.differ,
	})
	fixture.awaitDiffCall(t, 1)
	fixture.awaitScheduled(t)
	return fixture
}

func (f *auxBarEventFixture) scheduleNextTick(fn func()) bool {
	fn()
	f.scheduled <- struct{}{}
	return true
}

func (f *auxBarEventFixture) awaitDiffCall(t *testing.T, want int32) {
	t.Helper()
	select {
	case got := <-f.differ.called:
		require.Equal(t, want, got)
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for diff call %d", want)
	}
}

func (f *auxBarEventFixture) awaitScheduled(t *testing.T) {
	t.Helper()
	select {
	case <-f.scheduled:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for aux-bar rebuild")
	}
}

func (f *auxBarEventFixture) requireQuiet(t *testing.T) {
	t.Helper()
	select {
	case call := <-f.differ.called:
		t.Fatalf("unexpected diff call %d", call)
	case <-f.scheduled:
		t.Fatal("unexpected aux-bar rebuild")
	case <-time.After(50 * time.Millisecond):
	}
}

func (f *auxBarEventFixture) close(t *testing.T) {
	t.Helper()
	require.NoError(t, f.bar.Close())
}

type auxBarEventPublisher struct {
	events  []textapi.EventType
	handler text.EventHandler
}

func (p *auxBarEventPublisher) SubscribeEvents(
	events []textapi.EventType, handler text.EventHandler,
) error {
	p.events = append([]textapi.EventType(nil), events...)
	p.handler = handler
	return nil
}

func (p *auxBarEventPublisher) UnsubscribeEvents(text.EventHandler) (bool, error) {
	p.handler = nil
	return true, nil
}

func (p *auxBarEventPublisher) dispatch(event textapi.Event) {
	p.handler.Handle(context.Background(), event)
}

type auxBarCommandRegistry struct{}

func (auxBarCommandRegistry) SubscribeCommandForFile(
	workspaceapi.URI, textapi.CommandManual, text.CommandHandler,
) error {
	return nil
}

func (auxBarCommandRegistry) UnsubscribeCommandForFile(workspaceapi.URI, string) error {
	return nil
}

type eventDiffer struct {
	calls  atomic.Int32
	called chan int32
}

func newEventDiffer() *eventDiffer {
	return &eventDiffer{called: make(chan int32, 8)}
}

func (d *eventDiffer) Diff(context.Context, workspaceapi.URI) (vctrl.FileDiff, error) {
	call := d.calls.Add(1)
	d.called <- call
	return vctrl.FileDiff{}, nil
}

func (d *eventDiffer) WorkingDiff(
	context.Context, workspaceapi.URI, int,
) ([]vctrl.FileDiff, error) {
	return nil, nil
}

func (d *eventDiffer) ListRemotes(context.Context, workspaceapi.URI) ([]string, error) {
	return nil, nil
}

func (d *eventDiffer) ShortRef(context.Context, workspaceapi.URI) (string, error) {
	return "", nil
}

func (d *eventDiffer) CurrentCommit(context.Context, workspaceapi.URI) (string, error) {
	return "", nil
}

func (d *eventDiffer) RemoteURL(
	context.Context, workspaceapi.URI, string,
) (string, error) {
	return "", nil
}

func (d *eventDiffer) RelPath(context.Context, string) (string, error) {
	return "", nil
}

func requireNoDiffCall(t *testing.T, called <-chan int32) {
	t.Helper()
	select {
	case call := <-called:
		t.Fatalf("unexpected diff call %d", call)
	default:
	}
}

func BenchmarkAuxBarAbsoluteSmall(b *testing.B) {
	benchmarkAuxBar(b, 10, 10, true, false)
}

func BenchmarkAuxBarAbsoluteMedium(b *testing.B) {
	benchmarkAuxBar(b, 100, 100, true, false)
}

func BenchmarkAuxBarAbsoluteLarge(b *testing.B) {
	benchmarkAuxBar(b, 1000, 1000, true, false)
}

func BenchmarkAuxBarRelativeSmall(b *testing.B) {
	benchmarkAuxBar(b, 10, 10, false, false)
}

func BenchmarkAuxBarRelativeMedium(b *testing.B) {
	benchmarkAuxBar(b, 100, 100, false, false)
}

func BenchmarkAuxBarRelativeLarge(b *testing.B) {
	benchmarkAuxBar(b, 1000, 1000, false, false)
}

func BenchmarkAuxBarAbsoluteMoveCursorSmall(b *testing.B) {
	benchmarkAuxBar(b, 10, 10, true, true)
}

func BenchmarkAuxBarAbsoluteMoveCursorMedium(b *testing.B) {
	benchmarkAuxBar(b, 100, 100, true, true)
}

func BenchmarkAuxBarAbsoluteMoveCursorLarge(b *testing.B) {
	benchmarkAuxBar(b, 1000, 1000, true, true)
}

func BenchmarkAuxBarRelativeMoveCursorSmall(b *testing.B) {
	benchmarkAuxBar(b, 10, 10, false, true)
}

// Adding or removing a line rebuilds every absolute line number, so the cost of
// an edit must grow linearly with the file's rows.
func BenchmarkAuxBarAbsoluteRebuildOnNewLine(b *testing.B) {
	for _, rows := range []int{1_000, 2_000, 4_000, 8_000, 16_000} {
		b.Run(fmt.Sprintf("rows=%d", rows), func(b *testing.B) {
			buf := cell.NewBuffer()
			buf.WriteString(strings.Repeat("line\n", rows))
			scroll := component.NewScroll(buf)
			bar := text.WithAuxBar(newTestHandler(scroll), buf, scroll, text.AuxBarConfig{
				LinesEnabled:     true,
				AbsoluteLines:    true,
				ScheduleNextTick: func(fn func()) bool { fn(); return true },
			})
			bar.Resize(80, 50)

			newLine := false
			for b.Loop() {
				if newLine {
					buf.Edit(context.Background(), term.Coordinates{}, term.Coordinates{Y: 1}, "")
				} else {
					buf.InsertString(term.Coordinates{}, "\n")
				}
				newLine = !newLine
			}
		})
	}
}

func BenchmarkAuxBarRelativeMoveCursorMedium(b *testing.B) {
	benchmarkAuxBar(b, 100, 100, false, true)
}

func BenchmarkAuxBarRelativeMoveCursorLarge(b *testing.B) {
	benchmarkAuxBar(b, 1000, 1000, false, true)
}

func benchmarkAuxBar(b *testing.B, width, height int, absolute, moveCursor bool) {
	buf := cell.NewBuffer()
	for buf.Rows() < height {
		buf.WriteString(copy)
	}
	fs := &testFoldsService{}
	fs.view = buf.WithView(fs)
	scroll := component.NewScroll(buf)
	h := newTestHandler(scroll)
	var wg sync.WaitGroup
	cb := func(fn func()) bool {
		fn()
		wg.Done()
		return true
	}

	foldsEnabled := true
	moveCursorModulo := 1
	if moveCursor {
		moveCursorModulo = 2
		foldsEnabled = false
	}

	if foldsEnabled {
		wg.Add(1)
		if !absolute { // resize
			wg.Add(1)
		}
	}

	cfg := text.AuxBarConfig{
		FoldsEnabled:     foldsEnabled,
		LinesEnabled:     true,
		AbsoluteLines:    absolute,
		HighlightCursor:  true,
		ScheduleNextTick: cb,
	}
	bar := text.WithAuxBar(h, buf, scroll, cfg)
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

var _ = (foldsService)(testFoldsService{})

type foldsService interface {
	Folds() (iterator.Iterator[term.Range], bool)
}

type testFoldsService struct {
	view cell.View
}

func (f testFoldsService) Rows() int {
	return f.view.Rows()
}

func (f testFoldsService) Columns(row int) int {
	return f.view.Columns(row)
}

func (f testFoldsService) Cell(at term.Coordinates) (term.Cell, bool) {
	return f.view.Cell(at)
}

func (f testFoldsService) RawCells() [][]term.Cell {
	return f.view.RawCells()
}

func (f testFoldsService) String() string {
	return f.view.String()
}

func (f testFoldsService) FoldsFrom(pos term.Coordinates) (
	iterator.Iterator[term.Range], bool,
) {
	folds, ok := f.Folds()
	if !ok {
		return nil, false
	}
	return iterator.Filter(folds, func(rng term.Range) bool {
		return rng.End.Y > pos.Y || (rng.Start.Y == pos.Y && rng.End.X > pos.X)
	}), true
}

func (f testFoldsService) Folds() (iterator.Iterator[term.Range], bool) {
	return iterator.FromSlice([]term.Range{
		{Start: term.Coordinates{Y: 2, X: 0}, End: term.Coordinates{Y: 6}},
		{Start: term.Coordinates{Y: 8}, End: term.Coordinates{Y: 13}},
		{Start: term.Coordinates{Y: 8, X: 12}, End: term.Coordinates{Y: 13}},
	}), true
}

var _ = (foldsService)(blockingFoldsService{})

// blockingFoldsService simulates a treesitter.Tree whose parser is not yet
// ready: Folds/FoldsFrom return iterators whose Next blocks until the
// context is cancelled (mirroring the waitingReady channel never
// closing).
type blockingFoldsService struct {
	testFoldsService
}

func blockingIterator() iterator.Iterator[term.Range] {
	return iterator.FromFunc(
		func(ctx context.Context) (term.Range, bool, error) {
			<-ctx.Done()
			return term.Range{}, false, ctx.Err()
		},
		func() error { return nil },
	)
}

func (f blockingFoldsService) Folds() (iterator.Iterator[term.Range], bool) {
	return blockingIterator(), true
}

func (f blockingFoldsService) FoldsFrom(term.Coordinates) (
	iterator.Iterator[term.Range], bool,
) {
	return blockingIterator(), true
}

type testHandler struct {
	*component.Scroll
	cursor int
	URI    workspaceapi.URI
}

func newTestHandler(scroll *component.Scroll) (t *testHandler) {
	t = new(testHandler)
	t.Scroll = scroll
	return t
}

func (t *testHandler) Resource() workspaceapi.URI {
	return t.URI
}

func (t *testHandler) SetWrap(wrap bool) {
}

func (t *testHandler) ShowCommandBar(show bool) {
}

func (t *testHandler) SetCursorAtScroll(term.Coordinates) bool {
	return false
}

func (t *testHandler) Close() error {
	return nil
}

func (t *testHandler) SeekUp() bool {
	return false
}

func (t *testHandler) SeekDown() bool {
	return false
}

func (t *testHandler) SeekOffset() int {
	return 0
}

func (t *testHandler) MaxSeekOffset() int {
	return 0
}

func (h *testHandler) SetLocationList(
	pri textapi.LocationPriority, ID string, loc text.LocationList,
) {
}

func (h *testHandler) LocationLists() []text.LocationSet {
	return nil
}

func (h *testHandler) MoveToNextLocation(ID string) bool {
	return false
}

func (h *testHandler) MoveToPrevLocation(ID string) bool {
	return false
}

func (h *testHandler) CellView() cell.View {
	return nil
}

func (h *testHandler) CellEditor() cell.Editor {
	return nil
}

func (e *testHandler) SetDefaultAttributes(attr term.Attributes) {
}

func (*testHandler) Dimensions() (int, int) { return 0, 0 }

func (*testHandler) IsSearchMode() bool { return false }

func (*testHandler) IsNormalMode() bool { return false }

func (h *testHandler) CursorAtScroll() term.Coordinates {
	return term.Coordinates{Y: h.cursor}
}

func (t *testHandler) Handle(ev term.Event) (bool, bool) {
	return false, true
}

func (t *testHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{Y: t.cursor}, 0, false
}

func (t *testHandler) Selection() (string, bool) {
	return "", false
}

const copy = `package main

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

type differ struct {
}

func (d differ) ListRemotes(_ context.Context, file workspaceapi.URI) ([]string, error) {
	return nil, nil
}

func (d differ) ShortRef(ctx context.Context, file workspaceapi.URI) (string, error) {
	return "main", nil
}

func (d differ) Diff(ctx context.Context, file workspaceapi.URI) (vctrl.FileDiff, error) {
	return vctrl.FileDiff{Hunks: []vctrl.Hunk{{NewLines: 2, NewStartLine: 5}, {OrigStartLine: 10, OrigLines: 2}}}, nil
}

func (d differ) WorkingDiff(
	ctx context.Context, file workspaceapi.URI, contextLines int,
) ([]vctrl.FileDiff, error) {
	panic("unimplemented")
}

func (d differ) CurrentCommit(ctx context.Context, file workspaceapi.URI) (string, error) {
	panic("unimplemented")
}

func (d differ) RemoteURL(ctx context.Context, file workspaceapi.URI, remoteName string) (string, error) {
	panic("unimplemented")
}

func (d differ) RelPath(ctx context.Context, file string) (string, error) {
	panic("Unimplemented")
}

func TestAuxBarPointerLeaves(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		absolute bool
	}{
		{name: "relative lines"},
		{name: "absolute lines", absolute: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, size := range pointerSizes {
				bar, _, _ := newPointerAuxBar(t, tt.absolute)
				handlertest.RunPointerLeaves(t, bar, size.width, size.height)
			}

			bar, scroll, settle := newPointerAuxBar(t, tt.absolute)
			bar.Resize(40, 10)
			settle()
			fold, ok := screenRuneAt(pointerStateOf(bar, 40, 10).Screen, auxBarVisibleFoldIcon)
			require.True(t, ok, "no fold to collapse")
			ctx := term.ContextWithPointer(context.Background(), new(term.Pointer))
			for _, key := range []term.Key{term.MouseLeft, term.MouseRelease} {
				bar.Handle(term.Event{
					Type: term.EventMouse, Key: key, MouseX: fold.X, MouseY: fold.Y, Context: ctx,
				})
			}
			settle()
			require.Contains(t, pointerStateOf(bar, 40, 10).Screen, string(auxBarHiddenFoldIcon))
			hiddenLines := func() int {
				bar.mu.Lock()
				defer bar.mu.Unlock()
				return scroll.HiddenLineCount()
			}
			hidden := hiddenLines()
			require.NotZero(t, hidden)

			requirePointerLeavesKeepState(t, bar, 40, 10,
				term.Coordinates{X: 10, Y: 2}, term.Coordinates{X: 16, Y: 4}, settle)

			// A parent that captures a drag keeps sending it, held, once
			// the pointer is past the edges of the fold column: a drag
			// for the editor, not a click on a fold.
			far := 4*40 + 100
			for _, y := range []int{-1, 10, -far, far} {
				bar.Handle(term.Event{
					Type: term.EventMouse, Key: term.MouseLeft, MouseX: fold.X, MouseY: y, Context: ctx,
				})
				require.Equal(t, hidden, hiddenLines(), "a drag held at row %d toggled a fold", y)
			}
		})
	}
}

const (
	auxBarHiddenFoldIcon  = '\uf44a'
	auxBarVisibleFoldIcon = '\uf44b'
)

// newPointerAuxBar returns a pointerEditor in an aux bar with lines and
// folds, serialized with the ticks the bar schedules as the event loop
// does, the editor's scroll, and a func that waits for the bar to draw
// its folds.
func newPointerAuxBar(
	t *testing.T, absolute bool,
) (*lockedHandler, *component.Scroll, func()) {
	ed := newPointerEditor()
	fs := &testFoldsService{}
	fs.view = ed.buf.WithView(fs)
	mu := new(sync.Mutex)
	bar := &lockedHandler{mu: mu, Handler: text.WithAuxBar(ed, ed.buf, ed.Scroll, text.AuxBarConfig{
		LinesEnabled:  true,
		FoldsEnabled:  true,
		AbsoluteLines: absolute,
		ScheduleNextTick: func(fn func()) bool {
			mu.Lock()
			defer mu.Unlock()
			fn()
			return true
		},
	})}
	settle := func() {
		t.Helper()
		require.Eventually(t, func() bool {
			return strings.ContainsAny(pointerStateOf(bar, 40, 10).Screen,
				string([]rune{auxBarHiddenFoldIcon, auxBarVisibleFoldIcon}))
		}, 5*time.Second, time.Millisecond, "the bar never drew its folds")
	}
	return bar, ed.Scroll, settle
}

// lockedHandler holds mu while it calls Handler.
type lockedHandler struct {
	tui.Handler
	mu *sync.Mutex
}

func (h *lockedHandler) Handle(ev term.Event) (bool, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.Handler.Handle(ev)
}

func (h *lockedHandler) Draw(w term.Writer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Handler.Draw(w)
}

func (h *lockedHandler) Resize(width, height int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Handler.Resize(width, height)
}

func (h *lockedHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.Handler.Cursor()
}

func (h *lockedHandler) Selection() (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.Handler.Selection()
}

var pointerSizes = []struct{ width, height int }{{40, 10}, {1, 1}}

// pointerContent has more rows and columns than the windows the pointer
// leave tests draw it in, wide runes whose cells a pointer can split, and
// the rows testFoldsService folds.
const pointerContent = `package main

// 日本語のコメント と 絵文字 🙂🎉 in a line wider than any window drawn here
import (
	"fmt"
	"strings"
)

func main() {
	s := "日本語🙂"
	for i := range 3 {
		fmt.Println(strings.Repeat(s, i), "a long line that goes past the right edge")
	}
}
`

// pointerEditor is a text.Handler that handles the mouse the way the
// editors do: a mouse.Mouse drives its text.Cursor through
// text.CursorMouseDelegate.
type pointerEditor struct {
	*testHandler
	buf    *cell.Buffer
	cursor *text.Cursor
	mouse  *mouse.Mouse
}

func newPointerEditor() *pointerEditor {
	buf := cell.NewBuffer()
	buf.WriteString(pointerContent)
	scroll := component.NewScroll(buf)
	cursor := text.NewCursor(scroll, nil)
	return &pointerEditor{
		testHandler: newTestHandler(scroll),
		buf:         buf,
		cursor:      cursor,
		mouse:       mouse.New(text.CursorMouseDelegate(cursor)),
	}
}

func (e *pointerEditor) Handle(ev term.Event) (bool, bool) { return e.mouse.Handle(ev) }
func (e *pointerEditor) CursorAtScroll() term.Coordinates  { return e.cursor.CursorAtScroll() }
func (e *pointerEditor) CellView() cell.View               { return e.buf.View() }
func (e *pointerEditor) CellEditor() cell.Editor           { return e.buf.Editor() }

func (e *pointerEditor) SetCursorAtScroll(pos term.Coordinates) bool {
	_, ok := e.cursor.MoveToScroll(pos)
	return ok
}

func (e *pointerEditor) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return e.cursor.Coordinates(), term.CursorStyleDefault, true
}

func (e *pointerEditor) Selection() (string, bool) {
	s := e.cursor.Selection()
	return s, s != ""
}

// pointerState is what a handler draws and reports to the frame.
type pointerState struct {
	Screen    string
	Cursor    term.Coordinates
	Style     term.CursorStyle
	Shown     bool
	Selection string
	Selected  bool
}

func pointerStateOf(h tui.Handler, width, height int) pointerState {
	w := term.NewStringWriter(width, height)
	h.Draw(w)
	_ = w.Flush()
	s := pointerState{Screen: w.String()}
	s.Cursor, s.Style, s.Shown = h.Cursor()
	s.Selection, s.Selected = h.Selection()
	return s
}

// screenRuneAt returns the cell of screen, as drawn by pointerStateOf,
// that holds r.
func screenRuneAt(screen string, r rune) (term.Coordinates, bool) {
	for y, row := range strings.Split(screen, "\n") {
		for x, ch := range []rune(row) {
			if ch == r {
				return term.Coordinates{X: x, Y: y}, true
			}
		}
	}
	return term.Coordinates{}, false
}

// requirePointerLeavesKeepState selects from press to drag in h, sized
// width by height, then requires that the pointer leaving h to any side
// changes nothing h draws or reports, both once the button is released
// and while it is still held. settle, if not nil, waits for the work h
// defers to later ticks before each check.
func requirePointerLeavesKeepState(
	t *testing.T, h tui.Handler, width, height int, press, drag term.Coordinates,
	settle func(),
) {
	t.Helper()
	if settle == nil {
		settle = func() {}
	}
	h.Resize(width, height)
	ctx := term.ContextWithPointer(context.Background(), new(term.Pointer))
	mouseAt := func(key term.Key, at term.Coordinates) term.Event {
		return term.Event{
			Type: term.EventMouse, Key: key, MouseX: at.X, MouseY: at.Y, Context: ctx,
		}
	}
	h.Handle(mouseAt(term.MouseLeft, press))
	h.Handle(mouseAt(term.MouseLeft, drag))
	h.Handle(mouseAt(term.MouseRelease, drag))
	requireLeavesKeepState(t, h, width, height, "released", settle)
	h.Handle(mouseAt(term.MouseLeft, press))
	requireLeavesKeepState(t, h, width, height, "held", settle)
}

func requireLeavesKeepState(
	t *testing.T, h tui.Handler, width, height int, phase string, settle func(),
) {
	t.Helper()
	far := 4*max(width, height) + 100
	for _, out := range []term.Coordinates{
		{X: -1, Y: height / 2}, {X: width, Y: height / 2},
		{X: width / 2, Y: -1}, {X: width / 2, Y: height},
		{X: -1, Y: -1}, {X: width, Y: height}, {X: -far, Y: -far}, {X: far, Y: far},
	} {
		settle()
		before := pointerStateOf(h, width, height)
		h.Handle(term.Event{Type: term.EventMouse, MouseX: out.X, MouseY: out.Y})
		settle()
		require.Equal(t, before, pointerStateOf(h, width, height),
			"button %s, then a leave to %v", phase, out)
	}
}
