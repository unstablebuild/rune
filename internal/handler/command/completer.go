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

package command

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"mvdan.cc/sh/v3/shell"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/walkdir"
)

// PartialCandidateSuffix marks a completion candidate that only advances
// the current argument instead of terminating it. Candidates are URI
// paths, so it is "/" on every platform.
const PartialCandidateSuffix = "/"

// separatorCutset also accepts the host separator so a natively typed
// Windows path classifies like an emitted candidate.
var separatorCutset = func() string {
	if filepath.Separator == '/' {
		return PartialCandidateSuffix
	}
	return PartialCandidateSuffix + string(filepath.Separator)
}()

// IsPartialCandidate reports whether candidate only advances the current
// argument. Quoted candidates are accepted.
func IsPartialCandidate(candidate string) bool {
	unquoted := UnquoteToken(candidate)
	if unquoted == "" {
		return false
	}
	return strings.ContainsRune(separatorCutset, rune(unquoted[len(unquoted)-1]))
}

// MarkPartialCandidate returns candidate as a partial candidate,
// preserving its quoting. Already-partial candidates are unchanged.
func MarkPartialCandidate(candidate string) string {
	if IsPartialCandidate(candidate) {
		return candidate
	}
	return ShellQuote(UnquoteToken(candidate) + PartialCandidateSuffix)
}

// TrimPartialCandidateSuffix returns the argument value s denotes. A
// path of only separators is the root and is returned as is.
func TrimPartialCandidateSuffix(s string) string {
	trimmed := strings.TrimRight(s, separatorCutset)
	if trimmed == "" {
		return s
	}
	return trimmed
}

// PartialCompleter returns a Completer that marks every candidate from c
// as partial.
func PartialCompleter(c Completer) Completer {
	return FuncCompleter(func(ctx context.Context, args []string) (
		iterator.Iterator[string], string, error,
	) {
		it, last, err := c.Complete(ctx, args)
		if err != nil || it == nil {
			return it, last, err
		}
		return iterator.Map(it, MarkPartialCandidate), last, nil
	})
}

// Completer abstracts the ability to complete command arguments.
type Completer interface {
	// Complete takes the given command and arguments and returns an iterator
	// over an expanded list of options for the last argument. It also returns
	// an expanded version of the last argument, if there is one, or an empty
	// string if the last argument could/should not be automatically expanded.
	//
	// A candidate ending in PartialCandidateSuffix only advances the last
	// argument; every other candidate terminates it.
	Complete(ctx context.Context, args []string) (
		iterator.Iterator[string], string, error,
	)
}

// FuncCompleter returns a Completer that calls fn every time Complete is called.
func FuncCompleter(
	fn func(context.Context, []string) (iterator.Iterator[string], string, error),
) Completer {
	return fnCompleter{fn: fn}
}

// NopCompleter returns a Completer that does nothing.
func NopCompleter() Completer {
	return fnCompleter{}
}

type fnCompleter struct {
	fn func(context.Context, []string) (iterator.Iterator[string], string, error)
}

func (d fnCompleter) Complete(
	ctx context.Context, args []string,
) (iterator.Iterator[string], string, error) {
	if d.fn != nil {
		return d.fn(ctx, args)
	}
	return iterator.Empty[string](), "", nil
}

// FilePathCompleter returns a files path completer with the given directory reader.
func FilePathCompleter(reader walkdir.Reader) Completer {
	return walkDirCompleter(reader, false)
}

// DirsCompleter returns a files path completer with the given directory reader.
func DirsCompleter(reader walkdir.Reader) Completer {
	return walkDirCompleter(reader, true)
}

// NonRecursiveDirsCompleter returns a directory path completer that lists
// only the immediate children of the directory implied by the last argument.
func NonRecursiveDirsCompleter(reader walkdir.Reader) Completer {
	return nonRecursiveDirsCompleter(reader)
}

func walkDirCompleter(reader walkdir.Reader, dirOnly bool) Completer {
	traverse := func(
		ctx context.Context, w walkdir.Reader, root string,
	) (iterator.Iterator[string], error) {
		// Skipping ~/Library (macOS) avoids the system "access data from
		// other apps" prompt. In dirOnly mode dot directories are pruned
		// at the source too: the post-iter filter would drop them anyway,
		// but descending into e.g. .git first floods the system with
		// ReadDir/Open syscalls during completion.
		filter := vctrl.ProtectedDirMatcher(w)
		if dirOnly {
			filter = vctrl.AnyMatcher(filter, vctrl.HiddenBaseMatcher())
		}
		ctx = walkdir.WithContextFilter(ctx, filter)
		fn := walkdir.ListFiles
		if dirOnly {
			fn = walkdir.ListDirs
		}
		it, err := fn(ctx, w, root)
		if err != nil {
			return nil, err
		}
		it = iterator.Filter(it, func(val string) bool {
			return !strings.HasSuffix(val, workspace.SwapFileExtensionName)
		})
		if dirOnly {
			it = iterator.Filter(it, func(val string) bool {
				return !strings.HasPrefix(val, ".")
			})
		}
		return it, nil
	}
	return FuncCompleter(func(
		ctx context.Context, args []string,
	) (iterator.Iterator[string], string, error) {
		if len(args) == 0 || args[len(args)-1] == "" {
			it, err := traverse(ctx, reader, ".")
			if err != nil {
				return nil, "", err
			}
			return iterator.Map(it, ShellQuote), "", nil
		}

		var modifiedLast string
		last := UnquoteToken(args[len(args)-1])

		// take ~ as the home of the user using the editor.
		// rather than the home directory of the user at the workspace.
		// do not always expand without making sure that we are not
		// erasing trailing /, which prevents user from editing files
		// in folders.
		var err error
		if last == "~" || last == "/~" ||
			strings.HasPrefix(last, "~/") || strings.HasPrefix(last, "/~/") {
			last, err = workspaceapi.ExpandPath(last, user.Current,
				func() (string, error) {
					// do not really expand to cwd,
					// let parseURIOrWorkspaceURI take care of that
					return ".", nil
				})
			if err != nil {
				return nil, "", fmt.Errorf("expand path: %v", err)
			}
			modifiedLast = last
		}

		uri, err := parseURIOrWorkspaceURI(reader, last)
		if err != nil {
			return nil, "", err
		}

		cwd, err := reader.URI(".")
		if err != nil {
			return nil, "", err
		}
		if uri.Scheme() != cwd.Scheme() || uri.Host() != cwd.Host() || uri.User() != cwd.User() {
			return iterator.Empty[string](), modifiedLast, nil
		}

		// if filter is absolute path and it happens to be the current working
		// directory of the given reader, the iterator returned by walkdir.ListFiles
		// will return paths relative to it, but filter will be absolute, machting
		// no results.
		needsExpand := workspaceapi.HasPrefix(uri, cwd) && filepath.IsAbs(last)

		it, err := traverse(ctx, reader, uri.Path())
		if err != nil {
			return nil, "", err
		}
		if needsExpand {
			it = iterator.Map(it, func(val string) string {
				return workspaceapi.Join(cwd, val).Path()
			})
		}
		return iterator.Map(it, ShellQuote), modifiedLast, nil
	})
}

func nonRecursiveDirsCompleter(reader walkdir.Reader) Completer {
	return FuncCompleter(func(
		_ context.Context, args []string,
	) (iterator.Iterator[string], string, error) {
		last := ""
		if len(args) != 0 {
			last = UnquoteToken(args[len(args)-1])
		}

		var (
			results     []string
			initialized bool
			initErr     error
			idx         int
		)
		return iterator.FromFunc(func(ctx context.Context) (string, bool, error) {
			if !initialized {
				results, initErr = listNonRecursiveDirCompletions(ctx, reader, last)
				initialized = true
			}
			if initErr != nil {
				return "", false, initErr
			}
			if idx >= len(results) {
				return "", false, nil
			}
			result := results[idx]
			idx++
			return result, true, nil
		}, func() error { return nil }), "", nil
	})
}

func listNonRecursiveDirCompletions(
	ctx context.Context, reader walkdir.Reader, last string,
) ([]string, error) {
	cwd, err := reader.URI(".")
	if err != nil {
		return nil, err
	}

	// Candidates are URI paths, which are slash-separated on every
	// platform, so a natively typed Windows path is folded to slashes up
	// front and all the arithmetic below stays slash-based. Emitting the
	// host separator instead would also collide with the shell-escape
	// layer, which treats a backslash as a metacharacter.
	last = filepath.ToSlash(last)

	pathURI := cwd
	completeURI := false
	if last != "" {
		pathURI, err = workspaceapi.ParseURI(last)
		if err == nil {
			completeURI = true
		} else {
			pathURI, err = reader.URI(last)
			if err != nil {
				return nil, err
			}
		}
	}
	if pathURI.Scheme() != cwd.Scheme() || pathURI.Host() != cwd.Host() ||
		pathURI.User() != cwd.User() {
		return nil, nil
	}

	enteredDir := last == "" || last == "~" || last == "/~" ||
		strings.HasSuffix(last, PartialCandidateSuffix)
	dirPath := pathURI.Path()
	if !enteredDir {
		dirPath = path.Dir(dirPath)
	}

	entries, err := reader.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}
	filter := vctrl.AnyMatcher(
		vctrl.ProtectedDirMatcher(reader), vctrl.HiddenBaseMatcher())
	outputBase := path.Dir(last)
	if enteredDir {
		outputBase = path.Clean(last)
	}

	results := make([]string, 0, len(entries))
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if !entry.Type().IsDir() {
			continue
		}
		childURI, err := workspaceapi.WithPath(
			pathURI, path.Join(dirPath, entry.Name()))
		if err != nil {
			return nil, err
		}
		if filter.Match(childURI, true) {
			continue
		}

		var result string
		switch {
		case completeURI:
			result = workspaceapi.RelPath(cwd, childURI)
		case outputBase == "." || outputBase == "":
			result = entry.Name()
		default:
			result = path.Join(outputBase, entry.Name())
		}
		results = append(results, ShellQuote(result+PartialCandidateSuffix))
	}
	return results, nil
}

// OutputLinesCompleter returns a files path completer with the given
// directory reader. lookup resolves any $VAR references inside
// cmdAndArgs; pass nil to use os.Getenv alone. Callers that want to
// expand Rune-managed variables on top of os.Getenv should pass the
// function returned by text/cmdenv.Lookup(src).
func OutputLinesCompleter(
	w schemeapi.Executor, cmdAndArgs []string, lookup func(string) string,
) Completer {
	return FuncCompleter(func(
		ctx context.Context, args []string,
	) (iterator.Iterator[string], string, error) {
		if len(cmdAndArgs) == 0 {
			return nil, "", errors.New("expected at least one argument with the name " +
				"of the executable to run")
		}
		ch := make(chan error)
		cmdAndArgsStr := strings.Join(cmdAndArgs, " ")
		if lookup == nil {
			lookup = os.Getenv
		}
		cmdAndArgs, err := shell.Fields(cmdAndArgsStr, lookup)
		if err != nil {
			return nil, "", fmt.Errorf("expand shell arguments: %w", err)
		}
		cmd := workspaceapi.Cmd{
			Path:    cmdAndArgs[0],
			Watcher: workspaceapi.ChanProcessWatcher(ch),
		}
		if len(cmdAndArgs) > 1 {
			cmd.Args = cmdAndArgs[1:]
		}

		pr, pw := io.Pipe()
		cmd.Stdout = pw
		scanner := bufio.NewScanner(pr)
		pid, err := w.StartCommand(ctx, cmd)
		if err != nil {
			return nil, "", err
		}

		go debug.CapturePanicReport(func() {

			select {
			case <-ctx.Done():
			case <-ch:
			}
			_ = pr.Close()

		})
		return iterator.FromFunc(func(ctx context.Context) (string, bool, error) {
			if scanner.Scan() {
				return scanner.Text(), true, nil
			}
			if err := scanner.Err(); err != nil {
				return "", false, err
			}
			return "", false, nil // EOF
		}, func() (ret error) {
			if err := w.Signal(pid, syscall.SIGINT); err != nil {
				ret = errors.Join(ret, err)
			}
			if err := pr.Close(); err != nil {
				ret = errors.Join(ret, err)
			}
			return ret
		}), "", nil
	})
}

func parseURIOrWorkspaceURI(reader walkdir.Reader, path string) (workspaceapi.URI, error) {
	uri, err := workspaceapi.ParseURI(path)
	if err != nil {
		uri, err = reader.URI(path)
	}
	return uri, err
}

// HistoryAccessor exposes the persisted command history as a stream of
// raw command-and-args lines. Implementations return the iterator and
// true when any history is available, or a nil iterator and false when
// no history exists. The args parameter is the same one passed to a
// Completer's Complete; implementations are free to ignore it (the
// canonical implementation does — filtering happens in HistoryCompleter,
// see its docs).
type HistoryAccessor interface {
	HistoryIterator(ctx context.Context, args []string) (iterator.Iterator[string], bool)
}

// HistoryCompleter returns a Completer backed by the given HistoryAccessor.
// Used by alias chains to expose a per-alias argument history as a regular
// Completer that can be combined with other completers via MultiCompleter.
//
// HistoryCompleter narrows the raw history stream to entries that begin
// with the same command prefix that the user is currently typing — the
// `args` slice received by Complete is "<alias-name>", "<arg1>", …,
// "<partial-last-arg>". Only entries whose first len(args)-1 tokens
// match are kept, and the matching prefix is stripped from each entry
// before being yielded. This mirrors the implicit history fallback the
// command Prompt has always applied (see commandArgsHistoryIterator)
// so the `{history}` placeholder behaves consistently with it.
//
// Panics when acc is nil — the caller must supply a working accessor.
func HistoryCompleter(acc HistoryAccessor) Completer {
	if acc == nil {
		panic("command.HistoryCompleter: nil HistoryAccessor")
	}
	return FuncCompleter(func(
		ctx context.Context, args []string,
	) (iterator.Iterator[string], string, error) {
		it, ok := acc.HistoryIterator(ctx, args)
		if !ok || it == nil {
			return iterator.Empty[string](), "", nil
		}
		// "<cmd> <arg1> … <partialLastArg>" — keep entries that start
		// with the leading "<cmd> <arg1> … " (everything except the
		// trailing partial last arg) and strip that prefix on emit.
		prefixTokens := args
		if len(prefixTokens) > 0 {
			prefixTokens = prefixTokens[:len(prefixTokens)-1]
		}
		prefix := strings.Join(prefixTokens, " ")
		if prefix != "" {
			prefix += " "
		}
		filtered := iterator.Filter(it, func(entry string) bool {
			if prefix == "" {
				return entry != ""
			}
			return strings.HasPrefix(entry, prefix)
		})
		stripped := iterator.Map(filtered, func(entry string) string {
			return strings.TrimPrefix(entry, prefix)
		})
		return iterator.Filter(stripped, func(entry string) bool {
			return entry != ""
		}), "", nil
	})
}

// MultiCompleter returns a Completer that yields the concatenation of
// every child's results, in order. The returned iterator is a pure
// projection — no values are ever buffered on the calling goroutine,
// every Next call forwards directly to the underlying child iterator.
// Duplicate values across children are NOT filtered.
//
// Each child's Complete is called eagerly when the parent Complete is
// called. Complete is intended to be cheap — it just sets up the
// pipeline; the expensive work (walking a directory, reading a slow
// command's stdout, …) only happens as Next is pulled. Calling all
// Complete()s up front lets us collect each child's newLastArg
// synchronously: the first non-empty value wins, in chain order. This
// matters for placeholders like {file}, where an entry such as `~/foo`
// must be expanded to `/home/user/foo` no matter where in the chain
// the file completer lives.
//
// The resulting iterator streams via iterator.Aggregate, which only
// advances to the next child once the current one is exhausted. So a
// slow first child never starves the later ones, and a caller that
// stops iterating early (Close) tears every child down without ever
// pulling from them.
//
// nil entries in completers panic on iteration: an empty slot in a
// completer chain is a programmer error and should fail loudly.
// If a child returns an error from Complete, the error is logged and
// the child is skipped; iteration continues with the remaining
// children.
func MultiCompleter(completers ...Completer) Completer {
	return FuncCompleter(func(
		ctx context.Context, args []string,
	) (iterator.Iterator[string], string, error) {
		if len(completers) == 0 {
			return iterator.Empty[string](), "", nil
		}

		iters := make([]iterator.Iterator[string], 0, len(completers))
		var newLastArg string
		for _, c := range completers {
			if c == nil {
				panic("command.MultiCompleter: nil child completer")
			}
			it, last, err := c.Complete(ctx, args)
			if err != nil {
				log.WithField("class", "command.MultiCompleter").
					Warnf("child completer returned error: %v", err)
				continue
			}
			if it == nil {
				continue
			}
			// First non-empty newLastArg wins, but keep walking so
			// later children also have their Complete invoked and
			// their iterators contribute to the streamed output.
			if newLastArg == "" && last != "" {
				newLastArg = last
			}
			iters = append(iters, errorSwallowingIterator(it))
		}
		if len(iters) == 0 {
			return iterator.Empty[string](), newLastArg, nil
		}
		return iterator.Aggregate(iters...), newLastArg, nil
	})
}

// errorSwallowingIterator wraps it so that Aggregate (which stops on
// the first child error) keeps streaming subsequent children when one
// child reports an iteration error. The error is logged for diagnostics
// instead of bubbling up.
func errorSwallowingIterator(it iterator.Iterator[string]) iterator.Iterator[string] {
	return iterator.FromFunc(func(ctx context.Context) (string, bool, error) {
		v, ok := it.Next(ctx)
		if !ok {
			if err := it.Err(); err != nil && !errors.Is(err, context.Canceled) {
				log.WithField("class", "command.MultiCompleter").
					Warnf("child completer iterator error: %v", err)
			}
			return v, false, nil
		}
		return v, true, nil
	}, it.Close)
}
