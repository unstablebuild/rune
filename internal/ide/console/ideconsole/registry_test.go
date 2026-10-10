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

package ideconsole

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/sh"
)

const testWidth = 200

type mockCmdHandler struct {
	handleFn func(context.Context, repl.Command, repl.ProgressWriter) (
		iterator.Iterator[component.Responsive], error,
	)
	completeFn func(context.Context, string, []string) (
		iterator.Iterator[string], error,
	)
	helpFn func(context.Context, []string) (
		iterator.Iterator[component.Responsive], error,
	)
}

func (m *mockCmdHandler) HandleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if m.handleFn != nil {
		return m.handleFn(ctx, cmd, pw)
	}
	return iterator.FromSlice[component.Responsive](nil), nil
}

func (m *mockCmdHandler) Complete(
	ctx context.Context, cmd string, args []string,
) (iterator.Iterator[string], error) {
	if m.completeFn != nil {
		return m.completeFn(ctx, cmd, args)
	}
	return iterator.Empty[string](), nil
}

func (m *mockCmdHandler) Help(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if m.helpFn != nil {
		return m.helpFn(ctx, args)
	}
	return toLines("mock help"), nil
}

// sigHelperCmd is a CommandHandler that also implements SignatureHelper,
// used to verify registryFallback forwards signature-help requests to
// the hosted language REPL.
type sigHelperCmd struct {
	mockCmdHandler
	label   string
	ok      bool
	gotLine string
	gotCol  int
}

func (s *sigHelperCmd) SignatureHelp(
	_ context.Context, line string, col int,
) (string, bool) {
	s.gotLine = line
	s.gotCol = col
	return s.label, s.ok
}

func collectText(
	t *testing.T,
	iter iterator.Iterator[component.Responsive],
) []string {
	t.Helper()
	ctx := context.Background()
	var lines []string
	for {
		item, ok := iter.Next(ctx)
		if !ok {
			break
		}
		h := item.Height(testWidth)
		if h <= 0 {
			continue
		}
		w := term.NewStringWriter(testWidth, h)
		item.Resize(testWidth, h)
		item.Draw(w)
		_ = w.Flush()
		lines = append(lines, w.String())
	}
	require.NoError(t, iter.Err())
	return lines
}

func TestHandleCommandDispatches(t *testing.T) {
	r := NewRegistry()
	called := false
	r.Register("foo", "do foo", &mockCmdHandler{
		handleFn: func(_ context.Context, cmd repl.Command, _ repl.ProgressWriter) (
			iterator.Iterator[component.Responsive], error,
		) {
			called = true
			assert.Equal(t, "foo", cmd.Name)
			assert.Equal(t, []string{"bar"}, cmd.Args)
			return iterator.FromSlice([]component.Responsive{
				toResponsive("ok"),
			}), nil
		},
	})

	ctx := context.Background()
	iter, err := r.HandleCommand(ctx, repl.Command{
		Name: "foo", Args: []string{"bar"},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := collectText(t, iter)
	assert.True(t, called)
	assert.Len(t, out, 1)
}

func TestHandleCommandUnknown(t *testing.T) {
	r := NewRegistry()
	ctx := context.Background()
	_, err := r.HandleCommand(ctx, repl.Command{Name: "nope"}, repl.NopProgressWriter())
	assert.True(t, errors.Is(err, repl.ErrNotFound))
}

func TestCompleteCommandNames(t *testing.T) {
	r := NewRegistry()
	r.Register("alpha", "a", &mockCmdHandler{})
	r.Register("beta", "b", &mockCmdHandler{})
	r.Register("apex", "a2", &mockCmdHandler{})

	ctx := context.Background()

	// Prefix "a" matches alpha and apex.
	iter, err := r.Complete(ctx, "a", nil)
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()
	got, err := iterator.ToSlice(ctx, iter)
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "apex"}, got)

	// Prefix "b" matches beta.
	iter2, err := r.Complete(ctx, "b", nil)
	require.NoError(t, err)
	defer func() { _ = iter2.Close() }()
	got2, err := iterator.ToSlice(ctx, iter2)
	require.NoError(t, err)
	assert.Equal(t, []string{"beta"}, got2)

	// Empty prefix matches all, sorted.
	iter3, err := r.Complete(ctx, "", nil)
	require.NoError(t, err)
	defer func() { _ = iter3.Close() }()
	got3, err := iterator.ToSlice(ctx, iter3)
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "apex", "beta"}, got3)
}

func TestCompleteDelegatesToHandler(t *testing.T) {
	r := NewRegistry()
	r.Register("foo", "do foo", &mockCmdHandler{
		completeFn: func(
			_ context.Context, cmd string, args []string,
		) (iterator.Iterator[string], error) {
			assert.Equal(t, "foo", cmd)
			assert.Equal(t, []string{"ba"}, args)
			return iterator.FromSlice([]string{"bar", "baz"}), nil
		},
	})

	ctx := context.Background()
	iter, err := r.Complete(ctx, "foo", []string{"ba"})
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()
	got, err := iterator.ToSlice(ctx, iter)
	require.NoError(t, err)
	assert.Equal(t, []string{"bar", "baz"}, got)
}

func TestCompleteUnknownCommandReturnsEmpty(t *testing.T) {
	r := NewRegistry()
	ctx := context.Background()
	iter, err := r.Complete(ctx, "nope", []string{"x"})
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()
	got, err := iterator.ToSlice(ctx, iter)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestHelpListsCommands(t *testing.T) {
	r := NewRegistry()
	r.Register("alpha", "does alpha", &mockCmdHandler{})
	require.NoError(t, r.RegisterREPLCommand(textapi.CommandManual{
		Name:     "beta",
		Synopsis: "[path]",
		Summary:  "does beta",
	}, &mockCmdHandler{}))

	ctx := context.Background()
	iter, err := r.Help(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := collectText(t, iter)
	require.Len(t, out, 1)
	assert.Contains(t, out[0], "alpha")
	assert.Contains(t, out[0], "does alpha")
	assert.Contains(t, out[0], "beta [path]")
	assert.Contains(t, out[0], "does beta")
}

func TestHelpDelegatesToHandler(t *testing.T) {
	r := NewRegistry()
	r.Register("foo", "do foo", &mockCmdHandler{
		helpFn: func(_ context.Context, args []string) (
			iterator.Iterator[component.Responsive], error,
		) {
			assert.Equal(t, []string{"sub"}, args)
			return toLines("foo sub help"), nil
		},
	})

	ctx := context.Background()
	iter, err := r.Help(ctx, []string{"foo", "sub"})
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := collectText(t, iter)
	require.Len(t, out, 1)
	assert.Contains(t, out[0], "foo sub help")
}

func TestHelpUnknownCommand(t *testing.T) {
	r := NewRegistry()
	ctx := context.Background()
	_, err := r.Help(ctx, []string{"nope"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown command: nope")
}

func TestNewRegistersHelp(t *testing.T) {
	_, r := New(
		func(func()) bool { return false },
		term.NopInterrupter(),
		stubEditor{},
		Config{},
	)

	ctx := context.Background()

	// "help" should be registered.
	iter, err := r.Complete(ctx, "help", nil)
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()
	got, err := iterator.ToSlice(ctx, iter)
	require.NoError(t, err)
	assert.Equal(t, []string{"help"}, got)
}

func TestHelpCommandOutput(t *testing.T) {
	r := NewRegistry()
	registerBaseCommands(r)
	r.Register("foo", "does foo things", &mockCmdHandler{})

	ctx := context.Background()

	// "help" with no args lists all commands.
	iter, err := r.HandleCommand(ctx, repl.Command{
		Name: "help",
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()
	out := collectText(t, iter)
	require.Len(t, out, 1)
	assert.Contains(t, out[0], "foo")
	assert.Contains(t, out[0], "help")
}

func TestHelpCommandOutputUsesMarkdownList(t *testing.T) {
	r := NewRegistry()
	registerBaseCommands(r)
	require.NoError(t, r.RegisterREPLCommand(textapi.CommandManual{
		Name:    "status",
		Summary: "show status",
	}, &mockCmdHandler{}))

	ctx := context.Background()
	iter, err := r.HandleCommand(ctx, repl.Command{Name: "help"}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := collectText(t, iter)
	require.Len(t, out, 1)
	assert.Contains(t, out[0], "• help — Show available commands")
	assert.Contains(t, out[0], "• status — show status")
}

func TestHelpCommandDelegates(t *testing.T) {
	r := NewRegistry()
	registerBaseCommands(r)
	r.Register("foo", "does foo", &mockCmdHandler{
		helpFn: func(_ context.Context, args []string) (
			iterator.Iterator[component.Responsive], error,
		) {
			assert.Empty(t, args)
			return toLines("foo detailed help"), nil
		},
	})

	ctx := context.Background()
	iter, err := r.HandleCommand(ctx, repl.Command{
		Name: "help", Args: []string{"foo"},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := collectText(t, iter)
	require.Len(t, out, 1)
	assert.Contains(t, out[0], "foo detailed help")
}

func TestHelpCommandComplete(t *testing.T) {
	r := NewRegistry()
	registerBaseCommands(r)
	r.Register("foo", "f", &mockCmdHandler{})
	r.Register("far", "f", &mockCmdHandler{})

	ctx := context.Background()

	// Complete "help f<TAB>" should suggest foo and far.
	iter, err := r.Complete(ctx, "help", []string{"f"})
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()
	got, err := iterator.ToSlice(ctx, iter)
	require.NoError(t, err)
	assert.Equal(t, []string{"far", "foo"}, got)
}

func TestHelpCommandUsesFallback(t *testing.T) {
	fallback := &mockCmdHandler{
		helpFn: func(_ context.Context, args []string) (
			iterator.Iterator[component.Responsive], error,
		) {
			assert.Empty(t, args)
			return toLines("import \"<path>\" add an import"), nil
		},
	}
	r := NewRegistry()
	registerBaseCommands(r)
	r.SetHelpFallback(fallback)

	ctx := context.Background()
	iter, err := r.HandleCommand(ctx, repl.Command{Name: "help"}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := collectText(t, iter)
	require.Len(t, out, 1)
	assert.Contains(t, out[0], "import")
	assert.NotContains(t, out[0], "Show available commands")
}

func TestHelpCommandFallbackWiredByNew(t *testing.T) {
	fallback := &mockCmdHandler{
		helpFn: func(_ context.Context, _ []string) (
			iterator.Iterator[component.Responsive], error,
		) {
			return toLines("language repl help"), nil
		},
	}
	h, r := New(
		func(func()) bool { return false },
		term.NopInterrupter(),
		stubEditor{},
		Config{DisableShellInterpreter: fallback},
	)
	t.Cleanup(func() { _ = h.Close() })

	iter, err := r.HandleCommand(
		context.Background(), repl.Command{Name: "help"}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := collectText(t, iter)
	require.Len(t, out, 1)
	assert.Contains(t, out[0], "language repl help")
}

type recordingProgressWriter struct {
	mu      sync.Mutex
	samples []progressSample
}

type progressSample struct {
	progress, total int64
	units           string
}

func (w *recordingProgressWriter) Progress(progress, total int64, units string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.samples = append(w.samples, progressSample{progress, total, units})
}

func (w *recordingProgressWriter) get() []progressSample {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]progressSample(nil), w.samples...)
}

func TestShellHandlerForwardsProgressToRegisteredCommand(t *testing.T) {
	shellHandler, r := New(
		func(func()) bool { return false },
		term.NopInterrupter(),
		stubEditor{},
		Config{},
	)
	t.Cleanup(func() { _ = shellHandler.Close() })

	r.Register("dl", "download", &mockCmdHandler{
		handleFn: func(
			_ context.Context, _ repl.Command, pw repl.ProgressWriter,
		) (iterator.Iterator[component.Responsive], error) {
			require.NotNil(t, pw)
			pw.Progress(0, 0, "B")
			pw.Progress(50, 100, "B")
			pw.Progress(100, 100, "B")
			return iterator.FromSlice[component.Responsive](nil), nil
		},
	})

	// sh wraps the registry; the repl.Handler uses sh as its underlying
	// CommandHandler. Drive a command end-to-end through that path with
	// our recording ProgressWriter.
	shCmd := sh.New(r, workspaceapi.URI{})
	pw := &recordingProgressWriter{}
	ctx := context.Background()
	iter, err := shCmd.HandleCommand(ctx, repl.Command{Name: "dl"}, pw)
	require.NoError(t, err)
	t.Cleanup(func() { _ = iter.Close() })

	_, err = iterator.ToSlice(ctx, iter)
	require.NoError(t, err)

	samples := pw.get()
	require.NotEmpty(t, samples,
		"expected ProgressWriter to receive updates through sh -> registry")
	last := samples[len(samples)-1]
	assert.Equal(t, int64(100), last.progress)
	assert.Equal(t, int64(100), last.total)
	assert.Equal(t, "B", last.units)
}

func TestDisableShellInterpreterBypassesSh(t *testing.T) {
	var fallbackLines []string
	fallback := &mockCmdHandler{
		handleFn: func(
			_ context.Context, cmd repl.Command, _ repl.ProgressWriter,
		) (iterator.Iterator[component.Responsive], error) {
			line := cmd.Name
			if len(cmd.Args) > 0 {
				line += " " + strings.Join(cmd.Args, " ")
			}
			fallbackLines = append(fallbackLines, line)
			return toLines("fellthrough"), nil
		},
	}
	h, r := New(
		func(func()) bool { return false },
		term.NopInterrupter(),
		stubEditor{},
		Config{DisableShellInterpreter: fallback},
	)
	t.Cleanup(func() { _ = h.Close() })

	// The shim talks to the registry+fallback, not the sh layer.
	_, ok := h.shim.underlying.(*registryFallback)
	require.True(t, ok,
		"DisableShellInterpreter must make the registry+fallback the underlying handler, got %T",
		h.shim.underlying)

	ran := false
	r.Register("ping", "ping", &mockCmdHandler{
		handleFn: func(
			_ context.Context, _ repl.Command, _ repl.ProgressWriter,
		) (iterator.Iterator[component.Responsive], error) {
			ran = true
			return toLines("pong"), nil
		},
	})

	ctx := context.Background()

	iter, err := h.shim.HandleCommand(
		ctx, repl.Command{Name: "ping"}, repl.NopProgressWriter())
	require.NoError(t, err)
	out := collectText(t, iter)
	require.Len(t, out, 1)
	require.Contains(t, out[0], "pong")
	require.True(t, ran, "registered command must run")
	require.Empty(t, fallbackLines, "registered command must not reach the fallback")

	// An unregistered line is not shell-parsed nor PATH-resolved; it
	// is routed to the fallback handler with the whole line intact.
	iter, err = h.shim.HandleCommand(
		ctx, repl.Command{Name: "x", Args: []string{":=", "5"}},
		repl.NopProgressWriter())
	require.NoError(t, err)
	out = collectText(t, iter)
	require.Len(t, out, 1)
	require.Contains(t, out[0], "fellthrough")
	require.Equal(t, []string{"x := 5"}, fallbackLines)
}

func TestDefaultShellInterpreterEnabled(t *testing.T) {
	h, _ := New(
		func(func()) bool { return false },
		term.NopInterrupter(),
		stubEditor{},
		Config{},
	)
	t.Cleanup(func() { _ = h.Close() })

	_, ok := h.shim.underlying.(*CommandRegistry)
	require.False(t, ok,
		"default config must wrap the registry with the sh interpreter, got %T",
		h.shim.underlying)
}

func TestRegistryFallbackForwardsSignatureHelp(t *testing.T) {
	helper := &sigHelperCmd{label: "Println(a ...any)", ok: true}
	f := &registryFallback{registry: NewRegistry(), fallback: helper}

	label, ok := f.SignatureHelp(context.Background(), "fmt.Println(", 12)
	require.True(t, ok)
	require.Equal(t, "Println(a ...any)", label)
	require.Equal(t, "fmt.Println(", helper.gotLine)
	require.Equal(t, 12, helper.gotCol)

	plain := &registryFallback{registry: NewRegistry(), fallback: &mockCmdHandler{}}
	_, ok = plain.SignatureHelp(context.Background(), "fmt.Println(", 12)
	require.False(t, ok, "non-SignatureHelper fallback yields no help")
}
