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

package treesitter

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ernestrc/logd-go/logging"
	log "github.com/sirupsen/logrus"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// grammarFiles are the files of a language package a grammar is loaded
// from. Only parser is required.
type grammarFiles struct {
	langID, parser, highlights, indents, folds, locals, injections string
}

// newGrammarFiles sorts the files a language package lists by the role
// their names give them.
func newGrammarFiles(langID string, files []string) grammarFiles {
	f := grammarFiles{langID: langID}
	for _, file := range files {
		switch filepath.Base(file) {
		case ParserFilename:
			f.parser = file
		case HighlightsFilename:
			f.highlights = file
		case IndentsFilename:
			f.indents = file
		case FoldsFilename:
			f.folds = file
		case LocalsFilename:
			f.locals = file
		case InjectionsFilename:
			f.injections = file
		}
	}
	return f
}

// grammar is a loaded parser library and its compiled queries. A query is
// nil when its file is missing or does not compile, except highlights,
// which fails the load. Nothing may use them once close returns.
type grammar struct {
	langID                                         string
	language                                       *tree_sitter.Language
	highlights, indents, folds, locals, injections *tree_sitter.Query
	close                                          func() error
}

type grammarLoader interface {
	load(f grammarFiles) (*grammar, error)
}

// grammars is the loader of every tree, so the files and sections of a
// language share its grammar across workspaces. Switching it back to
// nativeGrammarLoader loads a grammar per tree again.
var grammars grammarLoader = &sharedGrammarLoader{next: nativeGrammarLoader{}}

// sharedGrammarLoader loads one grammar per set of resolved files through
// next and shares it: each load returns a reference whose close releases
// it, and the grammar is closed with its last reference. A package upgrade
// resolves to other files, so it gets a new grammar while trees on the old
// one keep it alive.
type sharedGrammarLoader struct {
	next grammarLoader

	mu      sync.Mutex
	entries map[string]*sharedGrammar
}

// sharedGrammar is a grammar loaded, or being loaded, by
// sharedGrammarLoader.
type sharedGrammar struct {
	// done is closed once g or err is set.
	done chan struct{}
	g    *grammar
	err  error
	// refs is guarded by sharedGrammarLoader.mu.
	refs int
}

func (l *sharedGrammarLoader) load(f grammarFiles) (*grammar, error) {
	key, err := grammarKey(f)
	if err != nil {
		// next reports the file that is missing.
		return l.next.load(f)
	}

	l.mu.Lock()
	if l.entries == nil {
		l.entries = make(map[string]*sharedGrammar)
	}
	e, ok := l.entries[key]
	if !ok {
		e = &sharedGrammar{done: make(chan struct{})}
		l.entries[key] = e
	}
	e.refs++
	l.mu.Unlock()

	if ok {
		<-e.done
	} else {
		g, err := l.next.load(f)
		l.mu.Lock()
		e.g, e.err = g, err
		if err != nil {
			l.deleteLocked(key, e)
		}
		close(e.done)
		l.mu.Unlock()
	}
	if e.err != nil {
		return nil, e.err
	}

	ref := *e.g
	var once sync.Once
	ref.close = func() (err error) {
		once.Do(func() { err = l.release(key, e) })
		return err
	}
	return &ref, nil
}

// release drops a reference to e, closing its grammar with the last one.
// Holding mu while closing keeps a load from taking a reference to a
// grammar being closed.
func (l *sharedGrammarLoader) release(key string, e *sharedGrammar) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e.refs--
	if e.refs > 0 {
		return nil
	}
	l.deleteLocked(key, e)
	return e.g.close()
}

func (l *sharedGrammarLoader) deleteLocked(key string, e *sharedGrammar) {
	if l.entries[key] == e {
		delete(l.entries, key)
	}
}

// grammarKey identifies the grammar f loads: the language, and the path
// each file resolves to with its modification time and size. Switching
// versions changes the paths; reinstalling a version changes the times.
func grammarKey(f grammarFiles) (string, error) {
	var b strings.Builder
	b.WriteString(f.langID)
	for _, file := range []string{
		f.parser, f.highlights, f.indents, f.folds, f.locals, f.injections,
	} {
		b.WriteByte(0)
		if file == "" {
			continue
		}
		resolved, err := filepath.EvalSymlinks(file)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return "", err
		}
		b.WriteString(resolved)
		b.WriteByte(0)
		b.WriteString(strconv.FormatInt(info.ModTime().UnixNano(), 10))
		b.WriteByte(0)
		b.WriteString(strconv.FormatInt(info.Size(), 10))
	}
	return b.String(), nil
}

// nativeGrammarLoader loads a grammar each time it is asked; closing it
// frees the queries and dlcloses the library.
type nativeGrammarLoader struct{}

// grammarLoads counts the grammars nativeGrammarLoader loaded, by
// language: map[string]*atomic.Int64.
var grammarLoads sync.Map

// GrammarLoads returns how many times this process loaded the grammar of
// langID: opened its parser library and compiled its queries.
func GrammarLoads(langID string) int64 {
	n, ok := grammarLoads.Load(langID)
	if !ok {
		return 0
	}
	return n.(*atomic.Int64).Load()
}

func (nativeGrammarLoader) load(f grammarFiles) (*grammar, error) {
	lib, err := dlopenGrammar(f.parser)
	if err != nil {
		return nil, err
	}

	parserID := fmt.Sprintf("tree_sitter_%s", f.langID)
	logGrammar(f.langID, log.TraceLevel, "loading parser %q", parserID)

	var lang func() uintptr
	sym, err := purego.Dlsym(lib, parserID)
	if err != nil {
		_ = purego.Dlclose(lib)
		return nil, fmt.Errorf("load symbol %q: %w", parserID, err)
	}
	purego.RegisterFunc(&lang, sym)

	language := tree_sitter.NewLanguage(unsafe.Pointer(lang()))
	// A parser refuses a language of an incompatible ABI version, which
	// would otherwise surface as a query failing to compile.
	parser := tree_sitter.NewParser()
	err = parser.SetLanguage(language)
	parser.Close()
	if err != nil {
		_ = purego.Dlclose(lib)
		return nil, fmt.Errorf("set parser language: %v", err)
	}

	g := &grammar{langID: f.langID, language: language}
	g.close = func() error {
		for _, q := range []*tree_sitter.Query{
			g.highlights, g.folds, g.locals, g.indents, g.injections,
		} {
			if q != nil {
				q.Close()
			}
		}
		return purego.Dlclose(lib)
	}

	if f.highlights != "" {
		if g.highlights, err = compileQueryFile(language, "highlights", f.highlights); err != nil {
			_ = g.close()
			return nil, fmt.Errorf("initialize highlights: %w", err)
		}
		logGrammar(f.langID, log.DebugLevel, "highlights initialized")
	} else {
		logGrammar(f.langID, log.InfoLevel,
			"highlights file not found, some features will be disabled")
	}

	for _, q := range []struct {
		name string
		file string
		dst  **tree_sitter.Query
	}{
		{"indents", f.indents, &g.indents},
		{"folds", f.folds, &g.folds},
		{"locals", f.locals, &g.locals},
	} {
		if q.file == "" {
			logGrammar(f.langID, log.InfoLevel,
				"%s file not found, some features will be disabled", q.name)
			continue
		}
		query, err := compileQueryFile(language, q.name, q.file)
		if err != nil {
			logGrammar(f.langID, log.ErrorLevel, "initialize %s: %v", q.name, err)
			continue
		}
		*q.dst = query
		logGrammar(f.langID, log.DebugLevel, "%s initialized", q.name)
	}

	if f.injections != "" {
		if g.injections, err = compileInjectionsFile(f.langID, language, f.injections); err != nil {
			logGrammar(f.langID, log.ErrorLevel, "initialize injections: %v", err)
		}
	}
	n, _ := grammarLoads.LoadOrStore(f.langID, new(atomic.Int64))
	n.(*atomic.Int64).Add(1)
	return g, nil
}

// compileQueryFile compiles the name query in file against language.
// Files come from a package's lib dir, which is on the local file system.
func compileQueryFile(
	language *tree_sitter.Language, name, file string,
) (*tree_sitter.Query, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read %s file: %v", name, err)
	}
	q, qerr := tree_sitter.NewQuery(language, string(data))
	if qerr != nil {
		return nil, fmt.Errorf("compile query: %v", qerr)
	}
	return q, nil
}

func compileInjectionsFile(
	langID string, language *tree_sitter.Language, file string,
) (*tree_sitter.Query, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read injections file: %v", err)
	}
	injections, disabled, err := newInjectionsQuery(language, string(data))
	if err != nil {
		return nil, err
	}
	for _, i := range disabled {
		logGrammar(langID, log.DebugLevel, "disabling injections pattern %d: unsupported predicate", i)
	}
	logGrammar(langID, log.DebugLevel, "injections initialized")
	return injections, nil
}

// dlopenGrammar loads the grammar at path through the file path resolves
// to. A package in use under another version keeps the paths of its
// files, and glibc's loader hands back the library it already loaded
// under a path, so loading by that path would parse with the old grammar
// for as long as anything holds it open.
func dlopenGrammar(path string) (uintptr, error) {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	lib, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return 0, fmt.Errorf("dlopen %q: %w", path, err)
	}
	return lib, nil
}

func logGrammar(langID string, level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithFields(log.Fields{
		logging.KeyClass: "syntax.grammar",
		"lang":           langID,
	}).Logf(level, msg, args...)
}
