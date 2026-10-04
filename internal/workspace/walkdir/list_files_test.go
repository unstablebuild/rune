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

package walkdir

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// statNotExistReader reports every path as nonexistent, as a closed
// or torn-down scheme does.
type statNotExistReader struct{}

func (statNotExistReader) URI(string) (workspaceapi.URI, error) {
	return workspaceapi.URI{}, nil
}

func (statNotExistReader) OpenFile(string, int, os.FileMode) (workspaceapi.File, error) {
	return nil, os.ErrNotExist
}

func (statNotExistReader) Stat(string) (os.FileInfo, error) {
	return nil, os.ErrNotExist
}

func (statNotExistReader) ReadDir(string) ([]os.DirEntry, error) {
	return nil, os.ErrNotExist
}

func TestResolveRootDirTerminatesWhenNothingExists(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		_, err := resolveRootDir(context.Background(),
			statNotExistReader{}, "/a/b/c")
		done <- err
	}()
	select {
	case err := <-done:
		require.ErrorIs(t, err, os.ErrNotExist)
	case <-time.After(10 * time.Second):
		t.Fatal("resolveRootDir never returned for a reader with no " +
			"existing directories")
	}
}
