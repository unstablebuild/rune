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

package workspace

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

func checkURIRelative(a, b workspaceapi.URI) error {
	if a.Scheme() != b.Scheme() {
		return fmt.Errorf("unexpected different schemes: %s vs %s",
			a.String(), b.String())
	}
	if a.Scheme() == FileScheme {
		return nil
	}
	if a.Host() != b.Host() {
		return fmt.Errorf("unexpected different hosts: %s vs %s",
			a.String(), b.String())
	}
	if a.User() != b.User() {
		return fmt.Errorf("unexpected different users: %s vs %s",
			a.String(), b.String())
	}
	return nil
}

// URIUnderPrefix reports whether uri is prefix itself or a resource
// nested under it. Unlike workspaceapi.HasPrefix it only matches on
// path-segment boundaries, so a namespace like "memory:///gitshow"
// does not swallow an unrelated "memory:///gitshowcase".
func URIUnderPrefix(uri, prefix workspaceapi.URI) bool {
	if uri.Scheme() != prefix.Scheme() ||
		uri.Host() != prefix.Host() || uri.User() != prefix.User() {
		return false
	}
	base := strings.TrimSuffix(prefix.Path(), "/")
	return uri.Path() == base || strings.HasPrefix(uri.Path(), base+"/")
}

// DefaultSwapFile returns the swap entry a file gets inside swapDir,
// on the same host as the file. swapDir must name a directory on that
// host: see SwapDirectory.
func DefaultSwapFile(swapDir workspaceapi.URI, file workspaceapi.URI) (workspaceapi.URI, error) {
	err := checkURIRelative(swapDir, file)
	if err != nil {
		return workspaceapi.URI{}, err
	}
	_, swapFilePath := swapFileName(swapDir.Path(), file.Path())
	return workspaceapi.WithPath(file, swapFilePath)
}

// DefaultSwapDirectory returns the swap directory of the sibling
// layout: the file's own directory, in the local or remote workspace.
func DefaultSwapDirectory(file workspaceapi.URI) (workspaceapi.URI, error) {
	swapDir, _ := swapFileName(filepath.Dir(file.Path()), file.Path())
	return workspaceapi.WithPath(file, swapDir)
}

// CanWorkspaceURI returns whether this uri can be managed by
// the given workspace. For Scheme implementations that
// do not support user and host/port, this method returns
// true if the URI schemes of the workspace and the supplied uri
// are the same.
func CanWorkspaceURI(workspace Workspace, uri workspaceapi.URI) (bool, error) {
	uriAtWorkspace, err := workspace.URI(uri.Path())
	if err != nil {
		return false, err
	}
	return uri.Scheme() == uriAtWorkspace.Scheme() &&
		uri.Hostname() == uriAtWorkspace.Hostname() &&
		uri.Port() == uriAtWorkspace.Port() &&
		uri.User() == uriAtWorkspace.User(), nil
}

// IsWorkspaceURI returns whether uri belongs under the given workspace root.
// For non-file schemes, capability and containment are equivalent.
func IsWorkspaceURI(workspace Workspace, uri workspaceapi.URI) (bool, error) {
	is, err := CanWorkspaceURI(workspace, uri)
	if err != nil || !is {
		return is, err
	}
	if uri.Scheme() != FileScheme {
		return true, nil
	}
	workspaceURI, err := workspace.URI(".")
	if err != nil {
		return false, err
	}
	return strings.HasPrefix(uri.Path(), workspaceURI.Path()), nil
}

// NewWorkspaceURI expands the given path with the given workspaceapi.URI
// and returns its corresponding URI. See ExpandPathWithURI for more details.
func NewWorkspaceURI(workspace workspaceapi.URI, path string) (workspaceapi.URI, error) {
	absPath, err := workspaceapi.ExpandPathWithURI(path, workspace)
	if err != nil {
		return workspaceapi.URI{}, err
	}

	var uriStr string
	if workspace.User() != "" {
		uriStr = fmt.Sprintf("%s://%s@%s%s", workspace.Scheme(),
			workspace.User(), workspace.Host(), absPath)
	} else {
		uriStr = fmt.Sprintf("%s://%s%s", workspace.Scheme(), workspace.Host(), absPath)
	}
	return workspaceapi.ParseURI(uriStr)
}
