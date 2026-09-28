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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
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

// A code action applies its edits under the command's context, which is
// cancelled as soon as the command returns. rebuildBar clears the bar
// and repopulates it from a goroutine that gives up when that context
// is done, so the gutter keeps its line numbers and loses folds and git
// signs until something else triggers a rebuild -- in practice the next
// flush.
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

// blockingFoldsService simulates a syntax.Tree whose parser is not yet
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
