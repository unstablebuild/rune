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

package extension

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"runtime"
	"slices"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/pkgapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/idelsp/languages"
	"unstable.build/rune/internal/workspace/walkdir"
)

// readSymbols streams a "file:line: text" entry for every capture in
// captureNames that the query matches across the workspace, where text
// runs from the capture to the end of its line. Exactly one of libQueryFile
// and query is set: libQueryFile names a query file that each language
// package ships, so every language is searched with its own copy, while
// query is run as is against every language. Languages whose package is not
// installed, does not ship libQueryFile, or rejects the query are skipped.
func readSymbols(
	ctx context.Context, fs workspaceapi.FileSystem, pkgs pkgapi.Manager,
	parser syntaxapi.Parser, libQueryFile, query string, captureNames []string,
) (iterator.Iterator[string], error) {
	langs, err := workspaceLanguages(ctx, fs)
	if err != nil {
		return nil, err
	}
	root, err := fs.URI(".")
	if err != nil {
		return nil, err
	}
	return &symbolIterator{
		fs:           fs,
		root:         root,
		pkgs:         pkgs,
		parser:       parser,
		libQueryFile: libQueryFile,
		query:        query,
		captureNames: captureNames,
		langs:        langs,
		lines:        lineCache{fs: fs},
	}, nil
}

// workspaceLanguages lists the languages of the workspace's files in the
// order the walk first meets them.
func workspaceLanguages(ctx context.Context, fs workspaceapi.FileSystem) ([]string, error) {
	files, err := walkdir.ListFiles(ctx, fs, ".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = files.Close() }()
	var langs []string
	for {
		file, ok := files.Next(ctx)
		if !ok {
			return langs, files.Err()
		}
		lang, err := languages.LanguageForFile(file)
		if err == nil && !slices.Contains(langs, lang) {
			langs = append(langs, lang)
		}
	}
}

// symbolIterator searches one language at a time: Search takes a single
// query, and a query file differs from one language package to the next.
type symbolIterator struct {
	fs           workspaceapi.FileSystem
	root         workspaceapi.URI
	pkgs         pkgapi.Manager
	parser       syntaxapi.Parser
	libQueryFile string
	query        string
	captureNames []string
	langs        []string
	lang         string
	results      sdkiterator.Iterator[syntaxapi.Result]
	lines        lineCache
	err          error
}

func (s *symbolIterator) Next(ctx context.Context) (string, bool) {
	for {
		if s.results == nil {
			if len(s.langs) == 0 || ctx.Err() != nil {
				return "", false
			}
			s.lang, s.langs = s.langs[0], s.langs[1:]
			s.results = s.search(ctx)
			continue
		}
		r, ok := s.results.Next(ctx)
		if !ok {
			s.closeResults(ctx)
			continue
		}
		name := workspaceapi.RelPath(s.root, r.File)
		line, err := s.lines.line(name, r.From.Y)
		if err != nil {
			s.err = errors.Join(s.err, err)
			continue
		}
		return symbolLine(name, line, r.From), true
	}
}

func (s *symbolIterator) search(ctx context.Context) sdkiterator.Iterator[syntaxapi.Result] {
	query := s.query
	if s.libQueryFile != "" {
		var ok bool
		var err error
		query, ok, err = libQuery(ctx, s.pkgs, s.fs, s.lang, s.libQueryFile)
		switch {
		case errors.Is(err, pkgapi.ErrNotInstalled):
			return nil
		case err != nil:
			log.Warnf("searchast: read %s from the %s package: %v", s.libQueryFile, s.lang, err)
			return nil
		case !ok:
			log.Debugf("searchast: the %s package does not ship %s", s.lang, s.libQueryFile)
			return nil
		}
	}
	it, err := s.parser.Search(query, s.captureNames, s.lang)
	if err != nil {
		log.Warnf("searchast: search %s files: %v", s.lang, err)
		return nil
	}
	return it
}

// closeResults logs rather than reports a language's search error, since a
// query that suits one language is commonly invalid for the others.
func (s *symbolIterator) closeResults(ctx context.Context) {
	if err := s.results.Err(); err != nil && ctx.Err() == nil {
		log.Warnf("searchast: search %s files: %v", s.lang, err)
	}
	_ = s.results.Close()
	s.results = nil
}

func (s *symbolIterator) Err() error {
	return s.err
}

func (s *symbolIterator) Close() error {
	if s.results == nil {
		return nil
	}
	return s.results.Close()
}

// libQuery reads the query file name from lang's package. ok is false when
// the package does not ship it.
func libQuery(
	ctx context.Context, pkgs pkgapi.Manager, fs workspaceapi.FileSystem, lang, name string,
) (query string, ok bool, err error) {
	files, err := pkgs.LibDir(ctx, lang)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = files.Close() }()
	for {
		file, more := files.Next(ctx)
		if !more {
			return "", false, files.Err()
		}
		if path.Base(file) == name {
			data, err := readFile(fs, file)
			return string(data), err == nil, err
		}
	}
}

func symbolLine(name, line string, from term.Coordinates) string {
	runes := []rune(line)
	text := string(runes[min(from.X, len(runes)):])
	return fmt.Sprintf("%s:%d: %s", name, from.Y+1, text)
}

var lineCacheSize = 2 * runtime.NumCPU()

// lineCache holds the lines of the files whose captures were formatted
// last. The IDE streams each file's captures together, interleaving only
// the files its workers parse at once, so a few files serve nearly every
// lookup without holding the workspace in memory.
type lineCache struct {
	fs    workspaceapi.FileSystem
	files []fileLines // least recently used first
}

type fileLines struct {
	name  string
	lines []string
}

// line returns line y of the named file, or "" past its end.
func (c *lineCache) line(name string, y int) (string, error) {
	var f fileLines
	if i := slices.IndexFunc(c.files, func(f fileLines) bool { return f.name == name }); i >= 0 {
		f = c.files[i]
		c.files = slices.Delete(c.files, i, i+1)
	} else {
		data, err := readFile(c.fs, name)
		if err != nil {
			return "", err
		}
		f = fileLines{name: name, lines: strings.Split(string(data), "\n")}
		if len(c.files) >= lineCacheSize {
			c.files = slices.Delete(c.files, 0, 1)
		}
	}
	c.files = append(c.files, f)
	if y >= len(f.lines) {
		return "", nil
	}
	return strings.TrimSuffix(f.lines[y], "\r"), nil
}

func readFile(fs workspaceapi.FileSystem, name string) (data []byte, err error) {
	file, err := fs.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	return io.ReadAll(file)
}
