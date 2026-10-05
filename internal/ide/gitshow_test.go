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

package ide

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/workspace"
)

// gitshowStubService stands in for the workspace git service so a test
// can pin exactly what :gitshow is asked to render.
type gitshowStubService struct {
	vctrl.Service
	diff vctrl.FileDiff
	err  error
}

func (s gitshowStubService) Diff(
	context.Context, workspaceapi.URI,
) (vctrl.FileDiff, error) {
	return s.diff, s.err
}

// gitshowRefService records which resource the branch lookup asked
// about, since the popup's own URI belongs to no repository.
type gitshowRefService struct {
	vctrl.Service
	ref  string
	seen []workspaceapi.URI
}

func (s *gitshowRefService) ShortRef(
	_ context.Context, uri workspaceapi.URI,
) (string, error) {
	s.seen = append(s.seen, uri)
	return s.ref, nil
}

type gitshowCommitService struct {
	vctrl.Service
	sha string
	err error
}

func (s gitshowCommitService) CurrentCommit(
	context.Context, workspaceapi.URI,
) (string, error) {
	return s.sha, s.err
}

func TestGitShowSeek(t *testing.T) {
	hunks := []vctrl.UnifiedHunk{
		{NewStart: 3, NewLines: 4},
		{NewStart: 20, NewLines: 3},
		{NewStart: 40, NewLines: 2},
	}
	for _, tc := range []struct {
		name string
		line int
		want int
	}{
		{"before every hunk", 1, 0},
		{"inside the first hunk", 4, 0},
		{"on the first hunk's last line", 6, 0},
		{"between hunks picks the one below", 10, 1},
		{"inside the second hunk", 21, 1},
		{"inside the last hunk", 41, 2},
		{"past every hunk falls back to the last", 500, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, gitshowSeek(hunks, tc.line))
		})
	}
}

// gitshowFixture opens relPath in a real file workspace with the given
// on-disk content and installs svc as the git service.
func gitshowFixture(
	t *testing.T, content string, svc vctrl.Service,
) (testEx, workspaceapi.URI) {
	t.Helper()
	e, fileScheme, tempDir := newExForTestingFileWorkspace(t, clipboard.NewInMemory())
	t.Cleanup(func() { require.NoError(t, fileScheme.Close()) })
	t.Cleanup(func() { require.NoError(t, e.Close()) })
	e.svc = svc

	require.NoError(t, os.WriteFile(
		filepath.Join(tempDir, "hello.go"), []byte(content), 0o644))
	uri, err := e.workspace.URI("hello.go")
	require.NoError(t, err)
	_, err = e.editFileURI(uri, e.invokeWindow(), false)
	require.NoError(t, err)
	e.Resize(80, 24)
	return e, uri
}

// gitshowDiff builds the FileDiff the workspace service would report for
// a file that went from committed to current.
func gitshowDiff(t *testing.T, uri workspaceapi.URI, committed, current string) vctrl.FileDiff {
	t.Helper()
	return vctrl.ConvertChangesToFileDiff(
		uri, vctrl.Diff(t.Context(), committed, current))
}

func TestGitShowErrors(t *testing.T) {
	t.Run("no file in focus", func(t *testing.T) {
		e := newExForTesting(t, nil)
		defer e.Close()
		e.Resize(80, 24)
		assert.ErrorContains(t, e.gitshow(t.Context()), "no file in focus")
	})

	t.Run("unsaved changes", func(t *testing.T) {
		e, uri := gitshowFixture(t, "one\ntwo\n", vctrl.NopService())
		e.svc = gitshowStubService{
			Service: vctrl.NopService(),
			diff:    gitshowDiff(t, uri, "one\n", "one\ntwo\n"),
		}
		// vi: enter insert mode and type, so the buffer diverges
		// from what the diff was computed against.
		e.Handle(term.Event{Type: term.EventKey, Ch: 'i'})
		e.Handle(term.Event{Type: term.EventKey, Ch: 'x'})
		dirty, ok := e.comp.IsDirty(uri)
		require.True(t, ok)
		require.True(t, dirty, "the fixture must leave the buffer dirty")

		assert.ErrorContains(t, e.gitshow(t.Context()), "unsaved changes")
	})

	t.Run("no changes", func(t *testing.T) {
		// NopService reports an empty FileDiff without an error, so
		// "no changes" has to be detected from the empty hunk list.
		e, _ := gitshowFixture(t, "one\ntwo\n", vctrl.NopService())
		assert.ErrorContains(t, e.gitshow(t.Context()), "no changes in hello.go")
	})

	t.Run("service failure", func(t *testing.T) {
		e, _ := gitshowFixture(t, "one\ntwo\n", gitshowStubService{
			Service: vctrl.NopService(),
			err:     errors.New("not a git repository"),
		})
		assert.ErrorContains(t, e.gitshow(t.Context()), "not a git repository")
	})
}

func TestGitShowOpensAtNearestHunk(t *testing.T) {
	const current = "one\ntwo\nthree\nfour\nfive\nsix\nseven\n" +
		"eight\nnine\nten\neleven\nTWELVE\n"
	const committed = "ONE\ntwo\nthree\nfour\nfive\nsix\nseven\n" +
		"eight\nnine\nten\neleven\ntwelve\n"

	e, uri := gitshowFixture(t, current, vctrl.NopService())
	e.svc = gitshowStubService{
		Service: vctrl.NopService(),
		diff:    gitshowDiff(t, uri, committed, current),
	}

	hunks := vctrl.UnifiedHunks(
		e.svc.(gitshowStubService).diff, current, gitshowContextLines)
	require.Len(t, hunks, 2, "the two changes must not coalesce")
	_, anchors := vctrl.UnifiedBuffer(hunks, "hello.go", "hello.go")

	// Park the cursor on the last line, next to the second change.
	require.NoError(t, e.moveFocusCursor(11))
	before := e.comp.Browser().FloatingWindows()
	popup, err := e.openGitshow(t.Context())
	require.NoError(t, err)
	require.Equal(t, before+1, e.comp.Browser().FloatingWindows())

	focus, err := e.Browser().Focus()
	require.NoError(t, err)
	require.True(t, focus.IsFloating())

	assert.Equal(t, anchors[1].Row, popup.CursorAtScroll().Y,
		"the popup must open on the hunk nearest the cursor")
	assert.Contains(t, gitshowFrame(t, e, 100, 30), "hunk 2/2",
		"the popup must name the hunk the cursor landed on, "+
			"not leave the editor's own mode indicator as the only status")

	e.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
	assert.Equal(t, anchors[1].Row+1, popup.CursorAtScroll().Y,
		"the wrapped editor must keep handling motion keys")

	e.Handle(term.Event{Type: term.EventKey, Ch: 'w', Mod: term.ModCtrl})
	assert.Equal(t, before, e.comp.Browser().FloatingWindows(),
		"<c-w> must close the popup")
}

// gitshowFrame renders the whole IDE, popup included.
func gitshowFrame(t *testing.T, e testEx, width, height int) string {
	t.Helper()
	e.Resize(width, height)
	w := term.NewStringWriter(width, height)
	e.Draw(w)
	require.NoError(t, w.Flush())
	return w.String()
}

func TestGitShowPopupURI(t *testing.T) {
	e, _ := gitshowFixture(t, "one\ntwo\n", vctrl.NopService())
	base := gitshowBaseURI()
	require.Equal(t, workspace.MemoryScheme, base.Scheme(),
		"the namespace must use a scheme registered by the IDE")

	uri, err := e.gitshowURI(filepath.Join("internal", "text", "statusbar.go"))
	require.NoError(t, err)
	assert.True(t, workspace.URIUnderPrefix(uri, base),
		"%s must be skippable through the %s namespace", uri, base)
	assert.Equal(t, "internal/text/statusbar.go.diff",
		workspaceapi.RelPath(base, uri),
		"the status bar resolves the popup path against the namespace")

	second, err := e.gitshowURI("hello.go")
	require.NoError(t, err)
	third, err := e.gitshowURI("hello.go")
	require.NoError(t, err)
	assert.NotEqual(t, second.String(), third.String(),
		"reopening the same file must not collide with a live popup")
	assert.Equal(t, second.Path(), third.Path(),
		"the disambiguator must stay out of the reported path")
}

func TestGitShowPopupWidth(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changed string
		capped  bool
	}{
		{"long line is capped", strings.Repeat("x", 400), true},
		{"short line keeps its natural width", "two", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := "one\n" + tc.changed + "\nthree\n"
			e, uri := gitshowFixture(t, current, vctrl.NopService())
			e.svc = gitshowStubService{
				Service: vctrl.NopService(),
				diff:    gitshowDiff(t, uri, "one\nshort\nthree\n", current),
			}
			e.Resize(500, 20)
			_, err := e.openGitshow(t.Context())
			require.NoError(t, err)

			focus, err := e.Browser().Focus()
			require.NoError(t, err)
			require.True(t, focus.IsFloating())
			// Width counts the window frame on both sides.
			frame := gitshowMaxWidth + 2
			if tc.capped {
				assert.Equal(t, frame, focus.Width())
				return
			}
			assert.Less(t, focus.Width(), frame)
			assert.Positive(t, focus.Width())
		})
	}
}

func TestGitShowStatusBarService(t *testing.T) {
	file, err := workspaceapi.ParseURI("file:///repo/hello.go")
	require.NoError(t, err)
	popup, err := workspaceapi.ParseURI(gitshowBase + "/hello.go.diff?n=1")
	require.NoError(t, err)

	upstream := &gitshowRefService{Service: vctrl.NopService(), ref: "main"}
	diff := gitshowDiff(t, file, "one\n", "one\ntwo\n")
	svc := gitshowService{Service: upstream, file: file, diff: diff}

	got, err := svc.Diff(t.Context(), popup)
	require.NoError(t, err)
	assert.Equal(t, diff, got)

	ref, err := svc.ShortRef(t.Context(), popup)
	require.NoError(t, err)
	assert.Equal(t, "main", ref)
	assert.Equal(t, []workspaceapi.URI{file}, upstream.seen,
		"the branch must be resolved against the file, not the popup")
}

func TestGitShowLabel(t *testing.T) {
	uri, err := workspaceapi.ParseURI("file:///repo/hello.go")
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		svc  vctrl.Service
		want string
	}{
		{
			name: "abbreviates the commit",
			svc:  gitshowCommitService{sha: "0123456789abcdef"},
			want: "vs HEAD 0123456",
		},
		{
			name: "keeps a shorter ref whole",
			svc:  gitshowCommitService{sha: "0123"},
			want: "vs HEAD 0123",
		},
		{
			name: "no commit yet",
			svc:  gitshowCommitService{},
			want: "vs HEAD",
		},
		{
			name: "lookup failure",
			svc:  gitshowCommitService{err: errors.New("not a git repository")},
			want: "vs HEAD",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, gitshowLabel(t.Context(), tc.svc, uri))
		})
	}
}

func TestGitShowReadFailure(t *testing.T) {
	e, uri := gitshowFixture(t, "one\ntwo\n", vctrl.NopService())
	e.svc = gitshowStubService{
		Service: vctrl.NopService(),
		diff:    gitshowDiff(t, uri, "one\n", "one\ntwo\n"),
	}
	require.NoError(t, os.Remove(uri.Path()))
	assert.ErrorContains(t, e.gitshow(t.Context()), "gitshow: read hello.go")
}
