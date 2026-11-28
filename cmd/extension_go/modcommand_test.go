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

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

type executeCaptureLSP struct {
	noopLSP
	executed chan semanticapi.ExecuteCommandParams
	result   string
	err      error
}

func (l *executeCaptureLSP) Initialize(
	_ context.Context, _ semanticapi.InitializeParams,
) (semanticapi.InitializeResult, error) {
	return semanticapi.InitializeResult{}, nil
}

func (l *executeCaptureLSP) ExecuteCommand(
	_ context.Context, params semanticapi.ExecuteCommandParams,
) (string, error) {
	l.executed <- params
	return l.result, l.err
}

func TestModCommandHandlerSendsGoplsArgs(t *testing.T) {
	t.Parallel()

	const goMod = "module example.com/test\n\ngo 1.22\n\n" +
		"require golang.org/x/text v0.3.7\n\n" +
		"require golang.org/x/mod v0.20.0 // indirect\n"
	tests := []struct {
		name         string
		goplsCommand string
		buildArgs    func(goModURI string) (any, error)
		// want is the argument with goModURI substituted for %s.
		want string
	}{
		{
			name:         "tidy",
			goplsCommand: "gopls.tidy",
			buildArgs:    tidyArgs,
			want:         `{"URIs":["%s"]}`,
		},
		{
			name:         "vendor",
			goplsCommand: "gopls.vendor",
			buildArgs:    vendorArgs,
			want:         `{"URI":"%s"}`,
		},
		{
			name:         "check upgrades",
			goplsCommand: "gopls.check_upgrades",
			buildArgs:    checkUpgradesArgs(newTestScheme()),
			want:         `{"URI":"%s","Modules":["golang.org/x/text","golang.org/x/mod"]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			goModPath := filepath.Join(dir, "go.mod")
			require.NoError(t, os.WriteFile(goModPath, []byte(goMod), 0o644))
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "pkg"), 0o755))
			uri, err := workspaceapi.ParseURI("file://" + filepath.Join(dir, "pkg", "main.go"))
			require.NoError(t, err)

			lsp := &executeCaptureLSP{executed: make(chan semanticapi.ExecuteCommandParams, 1)}
			h := modCommandHandler(lsp, &mockNotifications{}, tt.goplsCommand, tt.buildArgs)
			require.NoError(t, h.HandleCommand(t.Context(), textapi.Command{
				URI:      uri,
				Resource: &stubResource{uri: uri},
			}))

			select {
			case params := <-lsp.executed:
				assert.Equal(t, tt.goplsCommand, params.Command)
				require.Len(t, params.Arguments, 1)
				assert.JSONEq(t, fmt.Sprintf(tt.want, "file://"+goModPath), string(params.Arguments[0]))
			case <-time.After(5 * time.Second):
				t.Fatal("ExecuteCommand never ran")
			}
		})
	}
}

func TestCheckUpgradesArgsFailsWithoutGoMod(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	uri, err := workspaceapi.ParseURI("file://" + filepath.Join(dir, "main.go"))
	require.NoError(t, err)

	lsp := &executeCaptureLSP{executed: make(chan semanticapi.ExecuteCommandParams, 1)}
	h := modCommandHandler(lsp, &mockNotifications{}, "gopls.check_upgrades",
		checkUpgradesArgs(newTestScheme()))
	err = h.HandleCommand(t.Context(), textapi.Command{
		URI:      uri,
		Resource: &stubResource{uri: uri},
	})
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.Contains(t, err.Error(), "gopls.check_upgrades")
	assert.Empty(t, lsp.executed)
}

func TestAddImportCmdComplete(t *testing.T) {
	t.Parallel()

	mainGo := "file:///work/pkg/main.go"
	tests := []struct {
		name     string
		focused  []string
		args     []string
		goplsErr error
		// wantURI is the file list_known_packages is asked about; empty
		// when gopls must not be called.
		wantURI string
		want    []string
		wantErr bool
	}{
		{
			name:    "prefix narrows the focused file's known packages",
			focused: []string{mainGo},
			args:    []string{"str"},
			wantURI: mainGo,
			want:    []string{"strconv", "strings"},
		},
		{
			name:    "empty prefix after a space lists every known package",
			focused: []string{mainGo},
			args:    []string{""},
			wantURI: mainGo,
			want:    []string{"fmt", "strconv", "strings"},
		},
		{
			name:    "focusing a non-Go file keeps the last Go file",
			focused: []string{mainGo, "file:///work/go.mod", "memory:///console"},
			args:    []string{"f"},
			wantURI: mainGo,
			want:    []string{"fmt"},
		},
		{
			name:    "the last focused Go file wins",
			focused: []string{mainGo, "file:///work/other/other.go"},
			args:    []string{""},
			wantURI: "file:///work/other/other.go",
			want:    []string{"fmt", "strconv", "strings"},
		},
		{
			name:    "no Go file focused",
			focused: []string{"file:///work/go.mod"},
			args:    []string{""},
		},
		{
			name:    "subcommand name itself is being completed",
			focused: []string{mainGo},
			args:    nil,
		},
		{
			name:    "add-import takes a single package",
			focused: []string{mainGo},
			args:    []string{"fmt", ""},
		},
		{
			name:     "gopls error is reported",
			focused:  []string{mainGo},
			args:     []string{""},
			goplsErr: errors.New("expected 1 input arguments, got 0"),
			wantURI:  mainGo,
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lsp := &executeCaptureLSP{
				executed: make(chan semanticapi.ExecuteCommandParams, 1),
				result:   `{"Packages":["fmt","strconv","strings"]}`,
				err:      tt.goplsErr,
			}
			me := newMockEditor()
			h, err := addImportHandler(lsp, me, &mockNotifications{})
			require.NoError(t, err)
			for _, f := range tt.focused {
				me.fireEvent(t.Context(), textapi.Event{
					Type: textapi.EventTypeFocus,
					URI:  parseTestURI(t, f),
				})
			}

			it, err := h.Complete(t.Context(), "add-import", tt.args)
			if tt.wantErr {
				require.ErrorIs(t, err, tt.goplsErr)
			} else {
				require.NoError(t, err)
				got, err := iterator.ToSlice(t.Context(), it)
				require.NoError(t, err)
				assert.ElementsMatch(t, tt.want, got)
			}

			if tt.wantURI == "" {
				assert.Empty(t, lsp.executed)
				return
			}
			params := <-lsp.executed
			assert.Equal(t, "gopls.list_known_packages", params.Command)
			require.Len(t, params.Arguments, 1)
			assert.JSONEq(t, fmt.Sprintf(`{"URI":%q}`, tt.wantURI), string(params.Arguments[0]))
		})
	}
}
