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

package syntax

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ernestrc/go-multierror"
	"github.com/ernestrc/logd-go/logging"
	log "github.com/sirupsen/logrus"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/idelsp/languages"
	"unstable.build/rune/internal/workspace"
)

const (
	// ParserFilename is the filename of the parser shared object.
	ParserFilename = "tree-sitter.so"

	// HighlightsFilename is the name given to the highlights query file
	HighlightsFilename = "highlights.scm"

	// IndentsFilename is the name given to the indents query file.
	IndentsFilename = "indents.scm"

	// FoldsFilename is the name given to the folds query file.
	FoldsFilename = "folds.scm"

	// LocalsFilename is the name given to the a query file
	// used to define scopes, definitions and references.
	LocalsFilename = "locals.scm"
)

// Opener abstract reading files and it's required to open custom query files.
type Opener interface {
	OpenFile(path string, flag int, perm os.FileMode) (workspaceapi.File, error)
}

// WithTree installs a tree parser into the given buffer via cell.Buffer.WithEditor,
// and wraps the given FlusherCloser to provide re-parse on reload and flush.
// A cell.Buffer's View method can be used to retrieve this Tree in other contexts.
func WithTree(
	ctx context.Context,
	n browserapi.Notifications, interrupter term.Interrupter,
	pkg PkgManager, loc LocationSetter,
	uri workspaceapi.URI, buf *cell.Buffer,
	fc workspace.FlusherCloser,
	opener Opener,
	config Config,
) *Tree {
	if config.ScheduleNextTick == nil {
		panic("invalid config")
	}
	ret := new(Tree)
	ret.config = config
	ret.buf = buf
	ret.uri = uri
	ret.opener = opener
	// NOTE: this shouldn't be removed as the buffer's view
	// is how we share this tree's capabilities with
	// other parts of the codebase via interface assertion.
	ret.cview = ret.buf.WithView(ret)
	ret.buf.Subscribe(ret)
	ret.n = n
	ret.interrupter = interrupter
	ret.pkg = pkg
	ret.loc = loc
	ret.fc = fc
	ret.statesubs = make(map[chan State]struct{})
	ret.waitingReady = make(chan struct{})

	go debug.CapturePanicReport(func() {
		files, err := ret.downloadFiles(ctx)
		if err != nil {
			config.ScheduleNextTick(func() {
				// set current state either way, so unblocking
				// waiting goroutines can stream the first state.
				defer close(ret.waitingReady)
				ret.mu.Lock()
				defer ret.mu.Unlock()
				ret.currState = State{Closed: ret.closed, ParserError: err.Error()}
			})
			return
		}
		config.ScheduleNextTick(func() {
			defer close(ret.waitingReady)
			err := ret.initParserFromFiles(ctx, files)
			ret.mu.Lock()
			defer ret.mu.Unlock()
			if err != nil {
				ret.currState = State{Closed: ret.closed, ParserError: err.Error()}
			} else {
				ret.updateCurrentState(files.langID)
			}
		})
	})
	return ret
}

// Tree represents a file's syntax tree powered by tree-sitter.
type Tree struct {
	n           browserapi.Notifications
	interrupter term.Interrupter
	pkg         PkgManager
	loc         LocationSetter
	config      Config
	fc          workspace.FlusherCloser
	uri         workspaceapi.URI
	opener      Opener
	buf         *cell.Buffer
	cview       cell.View

	ready             bool
	closed            bool
	lib               uintptr
	mu                sync.Mutex
	cellBytes         cell.ByteCounts
	content           []byte
	contentBuf        bytes.Buffer
	parser            *tree_sitter.Parser
	tree              *tree_sitter.Tree
	highlights        *tree_sitter.Query
	highlightBuf      []textapi.Location
	highlightSpareBuf []textapi.Location
	locations         []highlightLocation
	locationsBuf      []highlightLocation
	locationsMergeBuf []highlightLocation
	updatedLocations  []highlightLocation
	lineStarts        []int
	indents           *tree_sitter.Query
	folds             *tree_sitter.Query
	locals            *tree_sitter.Query
	statesubs         map[chan State]struct{}
	currState         State

	onWillEditStart term.Coordinates
	onWillEditEnd   term.Coordinates
	onWillEditStr   string
	waitingReady    chan struct{}
}

// IndentationAt returns the indentation that should correspond to a node placed
// at the given line.
func (t *Tree) IndentationAt(line int) (int, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.ready || t.closed || t.tree == nil || t.indents == nil {
		return 0, false
	}

	if !t.config.Autoindent {
		return 0, false
	}

	if line >= t.buf.Rows() || line < 0 {
		return 0, false
	}
	ret := t.getIndentation(uint(line))
	return ret, ret >= 0
}

// State returns an iterator that eventually, when the Tree is ready
// streams the current state of the tree, every time it's altered.
func (t *Tree) State() iterator.Iterator[State] {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return iterator.FromSlice([]State{t.currState})
	}

	if !t.ready {
		return newWaitStateIterator(t, t.waitingReady)
	}

	return newReadyStateIterator(t)
}

// Folds returns all the folds captured by the parser.
func (t *Tree) Folds() (iterator.Iterator[term.Range], bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil, false
	}

	if !t.ready {
		return newFoldsIterator(false, t, t.waitingReady), true
	}

	if t.folds == nil || t.tree == nil {
		return nil, false
	}

	return iterator.FromSlice(t.getFolds(false)), true
}

// FoldsFrom returns all the folds captured after the given position.
func (t *Tree) FoldsFrom(pos term.Coordinates) (iterator.Iterator[term.Range], bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil, false
	}

	if !t.ready {
		return newFoldsFromIterator(pos, t, t.waitingReady), true
	}

	if t.folds == nil || t.tree == nil {
		return nil, false
	}

	return iterator.FromSlice(t.getFoldsFrom(pos)), true
}

// InitialFolds returns the folds captured by the parser that should be folded
// when file is initialized.
func (t *Tree) InitialFolds() (iterator.Iterator[term.Range], bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil, false
	}

	if !t.ready {
		return newFoldsIterator(true, t, t.waitingReady), true
	}

	if t.folds == nil || t.tree == nil {
		return nil, false
	}

	return iterator.FromSlice(t.getFolds(true)), true
}

// Close closes all resources associated with this Tree.
func (t *Tree) Close() (ret error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()
	// fc.Close may block (waiting for in-flight async flush/reload
	// goroutines that, in turn, may need t.mu via OnDidEdit). Run it
	// without holding the mutex; OnDidEdit / incrementalParse short
	// out on t.closed and return immediately.
	if err := t.fc.Close(); err != nil {
		ret = multierror.Append(ret, err)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.ready {
		return nil
	}
	if t.tree != nil {
		t.tree.Close()
	}
	t.parser.Close()
	if t.highlights != nil {
		t.highlights.Close()
	}
	if t.folds != nil {
		t.folds.Close()
	}
	if t.locals != nil {
		t.locals.Close()
	}
	if t.indents != nil {
		t.indents.Close()
	}
	if err := sysDlclose(t.lib); err != nil {
		ret = multierror.Append(ret, err)
	}
	for ch := range t.statesubs {
		close(ch)
	}
	clear(t.statesubs)
	t.cellBytes = nil
	t.content = nil
	t.contentBuf = bytes.Buffer{}
	t.highlightBuf = nil
	t.highlightSpareBuf = nil
	t.locations = nil
	t.locationsBuf = nil
	t.locationsMergeBuf = nil
	t.updatedLocations = nil
	t.lineStarts = nil
	t.currState.Closed = true
	return
}

type internalTree = Tree

func (t *internalTree) Rows() int {
	return t.cview.Rows()
}

func (t *internalTree) Columns(row int) int {
	return t.cview.Columns(row)
}

func (t *internalTree) Cell(at term.Coordinates) (term.Cell, bool) {
	return t.cview.Cell(at)
}

func (t *internalTree) RawCells() [][]term.Cell {
	return t.cview.RawCells()
}

func (t *internalTree) String() string {
	return t.cview.String()
}

func (t *internalTree) OnWillEdit(ctx context.Context, start, end term.Coordinates, str string) {
	t.onWillEditStart = start
	t.onWillEditEnd = end
	t.onWillEditStr = str
}

func (t *internalTree) OnDidEdit(ctx context.Context, from, to term.Coordinates, old string) {
	t.incrementalParse(t.onWillEditStart, t.onWillEditEnd, from, to, t.onWillEditStr)
}

func (t *internalTree) Flush(ctx context.Context) (<-chan error, error) {
	return t.wrapReparse(t.fc.Flush(ctx))
}

func (t *internalTree) Reload(ctx context.Context) (<-chan error, error) {
	return t.wrapReparse(t.fc.Reload(ctx))
}

func (t *internalTree) ForceFlush(ctx context.Context) (<-chan error, error) {
	return t.wrapReparse(t.fc.ForceFlush(ctx))
}

// wrapReparse forwards the inner channel's result and triggers a
// re-parse on completion. The channel result is sent only after the
// scheduled re-parse runs so callers may rely on a channel send
// meaning "ready and reparsed".
func (t *internalTree) wrapReparse(
	inner <-chan error, err error,
) (<-chan error, error) {
	if err != nil {
		return nil, err
	}
	out := make(chan error, 1)
	go func() {
		res := <-inner
		t.config.ScheduleNextTick(func() {
			t.reparse()
			out <- res
			close(out)
		})
	}()
	return out, nil
}

func (t *internalTree) LastFlush() time.Time {
	return t.fc.LastFlush()
}

type files struct {
	langID       string
	files        []string
	packageFound bool
}

func (t *Tree) downloadFiles(ctx context.Context) (files, error) {
	filename := t.uri.Name()
	id, err := languages.LanguageForFile(filename)
	if err != nil {
		t.log(log.DebugLevel, "aborting syntax parsing: %s", err)
		return files{}, err
	}
	t.log(log.DebugLevel, "found language for file %s: %s", filename, id)
	iter, err := t.pkg.LibDir(ctx, id)
	if err != nil {
		if errors.Is(err, storageapi.ErrNotFound) {
			t.log(log.DebugLevel,
				"aborting syntax parsing: package for language %q does not exist or it's not installed",
				id)
			return files{langID: id}, nil
		}
		t.log(log.ErrorLevel, "aborting syntax parsing: %v", err)
		t.config.ScheduleNextTick(func() { t.notifyNotAvail(id) })
		return files{}, err
	}
	defer iter.Close()
	allFiles, err := iterator.ToSlice(ctx, iter)
	if err != nil {
		msg := fmt.Sprintf("fetch language %q package: %v", id, err)
		t.log(log.ErrorLevel, "%s", msg)
		t.config.ScheduleNextTick(func() { t.notifyNotAvail(id) })
		return files{}, errors.New(msg)
	}
	return files{files: allFiles, langID: id, packageFound: true}, nil
}

func (t *Tree) initParserFromFiles(ctx context.Context, f files) error {
	if !f.packageFound {
		return nil
	}

	var langFile, highlightsFile, indentsFile, foldsFile, localsFile string
	for _, file := range f.files {
		switch filepath.Base(file) {
		case ParserFilename:
			langFile = file
		case HighlightsFilename:
			highlightsFile = file
		case IndentsFilename:
			indentsFile = file
		case FoldsFilename:
			foldsFile = file
		case LocalsFilename:
			localsFile = file
		}
	}
	if langFile == "" {
		msg := fmt.Sprintf("parser file not found in language %q package", f.langID)
		t.log(log.InfoLevel, "language not available: %s", msg)
		t.notifyNotAvail(f.langID)
		return errors.New(msg)
	}
	err := t.initParser(ctx, f.langID, langFile,
		highlightsFile, indentsFile, foldsFile, localsFile)
	if err != nil {
		t.log(log.ErrorLevel, "initialize language %s: %v", f.langID, err)
		t.notifyNotAvail(f.langID)
		return err
	}
	t.log(log.DebugLevel, "successfully initialized parser")
	return nil
}

func (t *Tree) initParser(
	ctx context.Context, langID,
	langfile, highlightsfile, indentsFile, foldsFile, localsFile string,
) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}

	lib, err := sysDlopen(langfile, dlNow|dlGlobal)
	if err != nil {
		return fmt.Errorf("dlopen %q: %w", langfile, err)
	}
	t.lib = lib

	parserID := fmt.Sprintf("tree_sitter_%s", langID)
	t.log(log.TraceLevel, "loading parser %q", parserID)

	var lang func() uintptr
	sym, err := sysDlsym(lib, parserID)
	if err != nil {
		_ = sysDlclose(t.lib)
		return fmt.Errorf("load symbol %q: %w", parserID, err)
	}
	purego.RegisterFunc(&lang, sym)

	language := tree_sitter.NewLanguage(unsafe.Pointer(lang()))
	t.parser = tree_sitter.NewParser()
	if err := t.parser.SetLanguage(language); err != nil {
		_ = sysDlclose(t.lib)
		t.parser.Close()
		return fmt.Errorf("set parser language: %v", err)
	}

	if highlightsfile != "" {
		if err := t.initHighlights(language, highlightsfile); err != nil {
			_ = sysDlclose(t.lib)
			t.parser.Close()
			return fmt.Errorf("initialize highlights: %w", err)
		}
	} else {
		t.log(log.InfoLevel, "highlights file not found, some features will be disabled")
	}

	if indentsFile != "" {
		if err := t.initIndents(language, indentsFile); err != nil {
			t.log(log.ErrorLevel, "initialize indents: %v", err)
		}
	} else {
		t.log(log.InfoLevel, "indents file not found, some features will be disabled")
	}

	if foldsFile != "" {
		if err := t.initFolds(language, foldsFile); err != nil {
			t.log(log.ErrorLevel, "initialize folds: %v", err)
		}
	} else {
		t.log(log.InfoLevel, "folds file not found, some features will be disabled")
	}

	if localsFile != "" {
		if err := t.initLocals(language, localsFile); err != nil {
			t.log(log.ErrorLevel, "initialize locals: %v", err)
		}
	} else {
		t.log(log.InfoLevel, "locals file not found, some features will be disabled")
	}

	// build the syntax tree
	t.persistCells()
	t.parseTree(nil, "initial parse error")
	t.updateCurrentState(langID)

	// set ready to true, even if tree is nil
	t.ready = true

	if t.tree == nil {
		t.log(log.ErrorLevel, "parsing failed: nil tree")
		return nil
	}

	if err := t.highlight(); err != nil {
		t.log(log.ErrorLevel, "highlight: %v", err)
	}

	if err := t.interrupter.Interrupt(ctx); err != nil {
		t.log(log.WarnLevel, "interrupt: %v", err)
	}
	// do not return highlight or interrupt error so we don't
	// show initialize SO failure notification to user.
	return nil
}

func (t *Tree) initHighlights(
	language *tree_sitter.Language, highlightsfile string,
) error {
	// for files coming from LibDir, we can use
	// os.ReadFile since packages are installed on the local fs
	// and paths are always absolute.
	data, err := os.ReadFile(highlightsfile)
	if err != nil {
		return fmt.Errorf("read highlights file: %v", err)
	}

	// compile the highlights query for this language.
	highlights, qerr := tree_sitter.NewQuery(language, string(data))
	if qerr != nil {
		return fmt.Errorf("compile query: %v", qerr)
	}
	t.highlights = highlights
	t.log(log.DebugLevel, "highlights initialized")
	return nil
}

func (t *Tree) initIndents(
	language *tree_sitter.Language, indentsFile string,
) error {
	data, err := os.ReadFile(indentsFile)
	if err != nil {
		return fmt.Errorf("read indents file: %v", err)
	}

	indents, qerr := tree_sitter.NewQuery(language, string(data))
	if qerr != nil {
		return fmt.Errorf("compile query: %v", qerr)
	}

	t.indents = indents
	t.log(log.DebugLevel, "indents initialized")
	return nil
}

func (t *Tree) initFolds(
	language *tree_sitter.Language, foldsFile string,
) error {
	data, err := os.ReadFile(foldsFile)
	if err != nil {
		return fmt.Errorf("read folds file: %v", err)
	}

	folds, qerr := tree_sitter.NewQuery(language, string(data))
	if qerr != nil {
		return fmt.Errorf("compile query: %v", qerr)
	}

	t.folds = folds
	t.log(log.DebugLevel, "folds initialized")
	return nil
}

func (t *Tree) initLocals(
	language *tree_sitter.Language, localsFile string,
) error {
	data, err := os.ReadFile(localsFile)
	if err != nil {
		return fmt.Errorf("read locals file: %v", err)
	}

	locals, qerr := tree_sitter.NewQuery(language, string(data))
	if qerr != nil {
		return fmt.Errorf("compile locals query: %v", qerr)
	}

	t.locals = locals
	t.log(log.DebugLevel, "locals initialized")
	return nil
}

func (t *Tree) notifyNotAvail(ext string) {
	_, _ = t.n.Notify(browserapi.LevelWarn,
		"syntax tree parser for language (%q) is not available", ext)
	if err := t.interrupter.Interrupt(context.Background()); err != nil {
		t.log(log.WarnLevel, "interrupt: %v", err)
	}
}

func (t *Tree) reparse() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.ready || t.closed {
		return
	}
	t.doReparse()
}

func (t *Tree) doReparse() {
	t.log(log.TraceLevel, "reparsing tree after flush")
	if t.tree != nil {
		t.tree.Close() // dealloc previous tree
	}
	t.persistCells()
	t.parseTree(nil, "re-parse error")
	t.streamState()
	if t.tree == nil {
		t.log(log.ErrorLevel, "parse failed: nil tree")
		return
	}
	if err := t.highlight(); err != nil {
		t.log(log.ErrorLevel, "highlight: %v", err)
	}
}

func (t *Tree) incrementalParse(start, end, from, to term.Coordinates, content string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.ready || t.tree == nil || t.closed {
		t.log(log.TraceLevel, "incremental parse aborted: ready: %t, nil tree: %t, closed: %t",
			t.ready, t.tree == nil, t.closed)
		return
	}
	// use old cells to convert coordinates
	newCells := t.buf.RawCells()
	edit, ok := editToTreesitterEdit(t.cellBytes, newCells, start, end, from, to, content)
	if !ok {
		t.log(log.WarnLevel, "convert edit to tree-sitter coordinates failed, "+
			"re-parsing enabled: %t", t.config.ReparseOnErrors)
		if t.config.ReparseOnErrors {
			t.doReparse()
		}
		return
	}
	t.log(log.TraceLevel, "converted edit(start=%v,end=%v,from=%v,to=%v)  "+
		"into tree sitter edit: %+v", start, end, from, to, edit)

	changedTree := t.tree.Clone()
	changedTree.Edit(&edit)
	t.tree.Edit(&edit)

	// get new cells to re-parse
	t.persistCells()
	t.parseTree(t.tree, "incremental parse error")
	t.streamState()
	if t.tree == nil {
		changedTree.Close()
		t.log(log.DebugLevel, "incremental parsing failed: "+
			"nil tree, re-parse on errors: %t", t.config.ReparseOnErrors)
		if t.config.ReparseOnErrors {
			t.doReparse()
		}
		return
	}
	if t.currState.ParserError != "" {
		t.log(log.DebugLevel, "%s, re-parse on errors: %t",
			t.currState.ParserError, t.config.ReparseOnErrors)
		if t.config.ReparseOnErrors {
			changedTree.Close()
			t.doReparse()
			return
		}
		// best effort continue
	}
	changed := changedTree.ChangedRanges(t.tree)
	changedTree.Close()
	if err := t.highlightIncremental(edit, changed, end, to); err != nil {
		t.log(log.ErrorLevel, "highlight: %v", err)
	}
}

func (t *Tree) persistCells() {
	raw := t.buf.RawCells()
	t.cellBytes = cell.NewByteCounts(t.cellBytes, raw)
	t.contentBuf.Reset()
	cell.CellsToBytesBuffer(&t.contentBuf, raw)
	t.content = t.contentBuf.Bytes()
}

func (t *Tree) highlight() error {
	if t.highlights == nil {
		return nil
	}
	t.lineStarts = lineStarts(t.lineStarts, t.content)
	highlights := t.getHighlights(t.cellBytes, t.content)
	clear(t.locations)
	t.locations = t.locations[:0]
	var ok bool
	t.locations, ok = highlightsToByteLocations(
		t.locations, highlights, t.cellBytes, t.lineStarts)
	if !ok {
		return errors.New("convert full highlight locations to byte ranges")
	}
	t.publishHighlights(highlights)

	t.log(log.TraceLevel, "set %d highlights", len(highlights))

	return nil
}

type highlightLocation struct {
	location textapi.Location
	start    uint
	end      uint
}

type highlightRange struct {
	from  term.Coordinates
	to    term.Coordinates
	start uint
	end   uint
}

func (t *Tree) highlightIncremental(
	edit tree_sitter.InputEdit, changed []tree_sitter.Range,
	oldEnd, newEnd term.Coordinates,
) error {
	if t.highlights == nil {
		return nil
	}
	if t.tree.RootNode().HasError() {
		return t.highlight()
	}
	t.lineStarts = lineStarts(t.lineStarts, t.content)

	window, ok := highlightWindow(edit, changed, t.content)
	if !ok || len(t.locations) == 0 || highlightWindowTooLarge(window, len(t.content)) {
		return t.highlight()
	}
	windowRange, err := highlightRangeFromBytes(window, t.cellBytes, t.lineStarts)
	if err != nil {
		return t.highlight()
	}

	updated, ok := t.expandHighlightWindow(&windowRange)
	if !ok || !highlightLocationsOrdered(t.locations) || !highlightLocationsOrdered(updated) {
		return t.highlight()
	}

	clear(t.locationsBuf)
	t.locationsBuf = t.locationsBuf[:0]
	for _, location := range t.locations {
		location, keep := shiftHighlightLocation(location, edit, oldEnd, newEnd)
		if keep && !byteRangesIntersect(location.byteRange(), windowRange.byteRange()) {
			t.locationsBuf = append(t.locationsBuf, location)
		}
	}

	clear(t.locationsMergeBuf)
	t.locationsMergeBuf = t.locationsMergeBuf[:0]
	if !mergeHighlightLocations(&t.locationsMergeBuf, t.locationsBuf, updated) {
		return t.highlight()
	}

	t.locations, t.locationsMergeBuf = t.locationsMergeBuf, t.locations
	clear(t.highlightBuf)
	t.highlightBuf = t.highlightBuf[:0]
	for _, location := range t.locations {
		t.highlightBuf = append(t.highlightBuf, location.location)
	}
	t.publishHighlights(t.highlightBuf)
	t.log(log.TraceLevel, "incrementally set %d highlights", len(t.highlightBuf))
	return nil
}

func (t *Tree) publishHighlights(highlights []textapi.Location) {
	t.loc.SetLocationList(textapi.LocationSlice(highlights))
	t.highlightBuf, t.highlightSpareBuf = t.highlightSpareBuf, highlights
}

func (t *Tree) expandHighlightWindow(window *highlightRange) ([]highlightLocation, bool) {
	for range 3 {
		updated, ok := t.getHighlightsInRange(window.byteRange())
		if !ok {
			return nil, false
		}
		if !expandHighlightRange(window, updated) {
			return updated, true
		}
		if highlightWindowTooLarge(window.byteRange(), len(t.content)) {
			return nil, false
		}
	}
	return nil, false
}

func (t *Tree) getHighlightsInRange(byteRange tree_sitter.Range) ([]highlightLocation, bool) {
	clear(t.highlightBuf)
	t.highlightBuf = t.highlightBuf[:0]
	t.highlightBuf = appendHighlights(t.highlightBuf, t.cellBytes, t.content, t.tree,
		t.highlights, t.config.CaptureNamesAttributes, &byteRange)
	clear(t.updatedLocations)
	t.updatedLocations = t.updatedLocations[:0]
	var ok bool
	t.updatedLocations, ok = highlightsToByteLocations(
		t.updatedLocations, t.highlightBuf, t.cellBytes, t.lineStarts)
	if !ok {
		return nil, false
	}
	return t.updatedLocations, true
}

func highlightWindow(edit tree_sitter.InputEdit, changed []tree_sitter.Range, content []byte) (
	tree_sitter.Range, bool,
) {
	contentLen := len(content)
	if edit.StartByte > uint(contentLen) || edit.NewEndByte > uint(contentLen) {
		return tree_sitter.Range{}, false
	}
	window := tree_sitter.Range{StartByte: edit.StartByte, EndByte: edit.NewEndByte}
	if window.StartByte == window.EndByte {
		if window.EndByte < uint(contentLen) {
			window.EndByte++
		} else if window.StartByte > 0 {
			window.StartByte--
		}
	}
	for _, changedRange := range changed {
		if changedRange.StartByte > uint(contentLen) || changedRange.EndByte > uint(contentLen) {
			return tree_sitter.Range{}, false
		}
		if changedRange.StartByte < window.StartByte {
			window.StartByte = changedRange.StartByte
		}
		if changedRange.EndByte > window.EndByte {
			window.EndByte = changedRange.EndByte
		}
	}
	window.StartByte = uint(bytes.LastIndexByte(content[:window.StartByte], '\n') + 1)
	if window.EndByte < uint(contentLen) {
		if nextLine := bytes.IndexByte(content[window.EndByte:], '\n'); nextLine >= 0 {
			window.EndByte += uint(nextLine + 1)
		} else {
			window.EndByte = uint(contentLen)
		}
	}
	return window, window.StartByte <= window.EndByte
}

func highlightWindowTooLarge(window tree_sitter.Range, contentLen int) bool {
	const largestIncrementalFraction = 4
	return contentLen == 0 || int(window.EndByte-window.StartByte) > contentLen/largestIncrementalFraction
}

func highlightsToByteLocations(
	dst []highlightLocation, locations []textapi.Location, counts cell.ByteCounts, starts []int,
) ([]highlightLocation, bool) {
	for _, location := range locations {
		start, startOK := coordinatesToSourceByteOffset(counts, starts, location.From)
		end, endOK := coordinatesToSourceByteOffset(counts, starts, location.To)
		if !startOK || !endOK || start > end {
			return nil, false
		}
		dst = append(dst, highlightLocation{
			location: location,
			start:    uint(start),
			end:      uint(end),
		})
	}
	return dst, true
}

func (l highlightLocation) byteRange() tree_sitter.Range {
	return tree_sitter.Range{StartByte: l.start, EndByte: l.end}
}

func (r highlightRange) byteRange() tree_sitter.Range {
	return tree_sitter.Range{StartByte: r.start, EndByte: r.end}
}

func shiftHighlightLocation(
	location highlightLocation, edit tree_sitter.InputEdit,
	oldEnd, newEnd term.Coordinates,
) (highlightLocation, bool) {
	if location.end <= edit.StartByte {
		return location, true
	}
	if location.start < edit.OldEndByte {
		return highlightLocation{}, false
	}
	delta := int64(edit.NewEndByte) - int64(edit.OldEndByte)
	location.start = uint(int64(location.start) + delta)
	location.end = uint(int64(location.end) + delta)
	location.location.From = shiftHighlightCoordinates(location.location.From, oldEnd, newEnd)
	location.location.To = shiftHighlightCoordinates(location.location.To, oldEnd, newEnd)
	return location, true
}

func shiftHighlightCoordinates(
	position, oldEnd, newEnd term.Coordinates,
) term.Coordinates {
	if position.Y == oldEnd.Y {
		position.X += newEnd.X - oldEnd.X
		position.Y = newEnd.Y
		return position
	}
	position.Y += newEnd.Y - oldEnd.Y
	return position
}

func expandHighlightRange(window *highlightRange, locations []highlightLocation) bool {
	expanded := false
	for _, location := range locations {
		if location.start < window.start {
			window.start = location.start
			window.from = location.location.From
			expanded = true
		}
		if location.end > window.end {
			window.end = location.end
			window.to = location.location.To
			expanded = true
		}
	}
	return expanded
}

func highlightRangeFromBytes(
	window tree_sitter.Range, counts cell.ByteCounts, starts []int,
) (highlightRange, error) {
	from, err := sourceByteOffsetToCoordinates(counts, starts, window.StartByte)
	if err != nil {
		return highlightRange{}, err
	}
	to, err := sourceByteOffsetToCoordinates(counts, starts, window.EndByte)
	if err != nil {
		return highlightRange{}, err
	}
	return highlightRange{from: from, to: to, start: window.StartByte, end: window.EndByte}, nil
}

func coordinatesToSourceByteOffset(
	counts cell.ByteCounts, starts []int, position term.Coordinates,
) (uint, bool) {
	if position.Y < 0 || position.Y >= len(starts) || position.X < 0 {
		return 0, false
	}
	if position.Y >= len(counts) {
		return 0, false
	}
	lineOffset, ok := cell.ByteCounts{counts[position.Y]}.CoordinatesToByteOffset(
		term.Coordinates{X: position.X})
	if !ok {
		return 0, false
	}
	return uint(starts[position.Y] + lineOffset), true
}

func sourceByteOffsetToCoordinates(
	counts cell.ByteCounts, starts []int, offset uint,
) (term.Coordinates, error) {
	if len(starts) == 0 {
		return term.Coordinates{}, errors.New("no source lines")
	}
	row, found := slices.BinarySearch(starts, int(offset))
	if !found {
		row--
	}
	if row < 0 || row >= len(counts) {
		return term.Coordinates{}, fmt.Errorf("byte offset %d exceeds source length", offset)
	}
	position, ok := cell.ByteCounts{counts[row]}.ByteOffsetToCoordinates(int(offset) - starts[row])
	if !ok {
		return term.Coordinates{}, fmt.Errorf("convert byte offset %d on source row %d", offset, row)
	}
	position.Y = row
	return position, nil
}

func mergeHighlightLocations(dst *[]highlightLocation, previous, updated []highlightLocation) bool {
	i, j := 0, 0
	for i < len(previous) || j < len(updated) {
		if j == len(updated) || (i < len(previous) && previous[i].start <= updated[j].start) {
			*dst = append(*dst, previous[i])
			i++
			continue
		}
		*dst = append(*dst, updated[j])
		j++
	}
	return true
}

func highlightLocationsOrdered(locations []highlightLocation) bool {
	for i := 1; i < len(locations); i++ {
		if locations[i-1].start > locations[i].start {
			return false
		}
	}
	return true
}

func (t *Tree) query(queryFile string, captureNames ...string) (iterator.Iterator[Match], error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil, errors.New("tree is closed")
	}

	if !t.ready {
		return newNodesIterator(t, t.waitingReady, queryFile, captureNames...), nil
	}

	if t.tree == nil {
		return nil, errors.New("tree could not be parsed")
	}

	data, err := t.runQuery(queryFile, captureNames)
	if err != nil {
		return nil, err
	}

	return iterator.FromSlice(data), nil
}

func (t *Tree) getHighlights(counts cell.ByteCounts, content []byte) []textapi.Location {
	clear(t.highlightBuf)
	t.highlightBuf = t.highlightBuf[:0]
	t.highlightBuf = appendHighlights(t.highlightBuf, counts, content, t.tree,
		t.highlights, t.config.CaptureNamesAttributes, nil)
	return t.highlightBuf
}

func getHighlights(
	counts cell.ByteCounts, content []byte, tree *tree_sitter.Tree, highlights *tree_sitter.Query,
	captureNamesAttributes map[string]term.Attributes,
) []textapi.Location {
	return appendHighlights(nil, counts, content, tree, highlights, captureNamesAttributes, nil)
}

func appendHighlights(
	locations []textapi.Location,
	counts cell.ByteCounts, content []byte, tree *tree_sitter.Tree, highlights *tree_sitter.Query,
	captureNamesAttributes map[string]term.Attributes, byteRange *tree_sitter.Range,
) []textapi.Location {
	root := tree.RootNode()

	cur := tree_sitter.NewQueryCursor()
	defer cur.Close()

	captureNames := highlights.CaptureNames()
	matches := cur.Matches(highlights, root, content)
	if byteRange != nil {
		matches.SetByteRange(byteRange.StartByte, byteRange.EndByte)
	}
	for {
		m, ok := matches.Next()
		if !ok {
			break
		}
		for _, cap := range m.Captures {
			rng := cap.Node.Range()
			from, to, err := convertRangeToCoordinates(counts, rng)
			if err != nil {
				continue
			}
			/*t.log(log.TraceLevel, "mapped range %v into coords: %v, %v", rng, from, to)*/
			if int(cap.Index) >= len(captureNames) {
				log.Warnf("index %d does not belong capture names %v", cap.Index, captureNames)
				continue
			}
			name := captureNames[cap.Index]
			attr := captureNameAttributes(captureNamesAttributes, name)
			locations = append(locations, textapi.Location{
				Attr: attr,
				From: from,
				To:   to,
			})
		}
	}
	return locations
}

func byteRangesIntersect(a, b tree_sitter.Range) bool {
	if a.StartByte == a.EndByte {
		return a.StartByte >= b.StartByte && a.StartByte < b.EndByte
	}
	if b.StartByte == b.EndByte {
		return b.StartByte >= a.StartByte && b.StartByte < a.EndByte
	}
	return a.StartByte < b.EndByte && b.StartByte < a.EndByte
}

func (t *Tree) streamState() {
	for ch := range t.statesubs {
		select {
		case ch <- t.currState:
		default:
		}
	}
}

func (t *Tree) updateCurrentState(langID string) {
	t.currState.Closed = t.closed
	t.currState.LangID = langID
	t.currState.Highlights = t.highlights != nil
	t.currState.Folds = t.folds != nil
	t.currState.Indents = t.indents != nil
}

func (t *Tree) parseTree(prev *tree_sitter.Tree, errorMsg string) {
	var hasError bool
	opts := tree_sitter.ParseOptions{
		ProgressCallback: func(state tree_sitter.ParseState) bool {
			hasError = state.HasError
			return false
		},
	}
	t.tree = t.parser.ParseWithOptions(func(i int, _ tree_sitter.Point) []byte {
		if i < len(t.content) {
			return t.content[i:]
		}
		return []byte{}
	}, prev, &opts)
	// Tree-sitter returns a new tree; the previous tree fed into the
	// incremental parse is a native allocation with no finalizer and
	// must be deleted or it leaks on every edit.
	if prev != nil {
		prev.Close()
	}
	if hasError || (t.tree != nil &&
		t.tree.RootNode().HasError() && t.config.StrictErrors) {
		t.currState.ParserError = errorMsg
	} else {
		t.currState.ParserError = ""
	}
	t.currState.Progress = 1
}

func (t *Tree) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithFields(log.Fields{
		logging.KeyClass: "syntax.tree",
		"uri":            t.uri,
	}).Logf(level, msg, args...)
}

// this is needed to handle multi width characters
func convertRangeToCoordinates(counts cell.ByteCounts, n tree_sitter.Range) (
	from, to term.Coordinates, err error,
) {
	start, end := n.StartPoint, n.EndPoint
	from, ok := counts.RunePosToCoordinates(int(start.Row), int(start.Column))
	if !ok {
		err = fmt.Errorf("convert points: failed to convert sitter 'start point "+
			" to term 'from' coordinates: point: %v", start)
		return
	}
	to, ok = counts.RunePosToCoordinates(int(end.Row), int(end.Column))
	if !ok {
		err = fmt.Errorf("convert points: failed to convert sitter 'end' point "+
			" to term 'to' coordinates: point: %v", end)
		return
	}
	return
}

// lineStarts returns the byte offset of the first byte of each line in
// content. starts[r] is the start of row r, so a tree-sitter Point's byte
// Column can be resolved to an absolute offset without a cell.Buffer.
// buf's capacity is recycled when provided; per-file callers that keep no
// buffer pass nil and get one exact-sized allocation.
func lineStarts(buf []int, content []byte) []int {
	if cap(buf) == 0 {
		buf = make([]int, 0, bytes.Count(content, []byte{'\n'})+2)
	}
	starts := append(buf[:0], 0)
	for i, b := range content {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// pointToCoordinates converts a tree-sitter Point (row + byte column) to
// term.Coordinates whose X is the rune column, matching the cell-buffer
// conversion for source text without multi-rune grapheme clusters.
func pointToCoordinates(content []byte, starts []int, p tree_sitter.Point) term.Coordinates {
	lineStart := starts[int(p.Row)]
	return term.Coordinates{
		Y: int(p.Row),
		X: utf8.RuneCount(content[lineStart : lineStart+int(p.Column)]),
	}
}

func editToTreesitterEdit(
	before cell.ByteCounts, after [][]term.Cell, start, end, from, to term.Coordinates, content string,
) (tree_sitter.InputEdit, bool) {
	startByte, sok := before.CoordinatesToByteOffset(start)
	oldEndByte, eok := before.CoordinatesToByteOffset(end)

	y, x, spok := before.CoordinatesToRunePos(start)
	startPos := tree_sitter.Point{Row: uint(y), Column: uint(x)}
	y, x, epok := before.CoordinatesToRunePos(end)
	oldEndPos := tree_sitter.Point{Row: uint(y), Column: uint(x)}
	y, x, tpok := cell.ConvertCoordinatesToRunePos(after, to)
	newEndPos := tree_sitter.Point{Row: uint(y), Column: uint(x)}

	if !sok || !eok || !spok || !epok || !tpok {
		/*logrus.Errorf("convert edit to tree-sitter coordinates failed: "+
		"sok=%t, eok=%t, spok=%t, epok=%t, tpok=%t",
		sok, eok, spok, epok, tpok) */
		return tree_sitter.InputEdit{}, false
	}

	return tree_sitter.InputEdit{
		StartByte:      uint(startByte),
		OldEndByte:     uint(oldEndByte),
		NewEndByte:     uint(startByte + int(len([]byte(content)))),
		StartPosition:  startPos,
		OldEndPosition: oldEndPos,
		NewEndPosition: newEndPos,
	}, true
}
