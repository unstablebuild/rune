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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"

	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	yaml "gopkg.in/yaml.v3"
	"unstable.build/rune/internal/ide/hostenv"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/starlarkconfig"
)

// includeKey is the top-level key of a YAML config file that lists the
// files to layer under the file's own keys.
const includeKey = "include"

// maxIncludeDepth bounds how deep included files nest.
const maxIncludeDepth = 16

// includeLoader layers config files onto a tree. Each file first layers the
// files it includes, in order, then its own keys, so a later layer wins
// over an earlier one. A relative include resolves from the directory of
// the including file; /Users/ernestrc/.rune and ~ expand. A file under the data
// directory's etc directory is the config of the package named by its
// path there: its top-level requirements are dropped, its $RUNE_PKG_ID and
// $RUNE_PKG_VERSION expand to the package and the version in use, and a
// .star one gets the package config globals.
type includeLoader struct {
	dataDir string
	// warn records a problem that does not stop the load under key.
	warn func(key string, err error)
	// stack holds the absolute paths of the files being loaded, outermost
	// first, to detect cycles and name them.
	stack []string
	// files holds the absolute path of every file read, in the order they
	// were read, the root config first.
	files []string
}

func newIncludeLoader(dataDir string, warn func(key string, err error)) *includeLoader {
	if abs, err := filepath.Abs(dataDir); err == nil && dataDir != "" {
		dataDir = abs
	}
	return &includeLoader{dataDir: dataDir, warn: warn}
}

// resolve returns the absolute path of the include path named in the file
// from, or relative to the working directory when from is empty.
func (l *includeLoader) resolve(path, from string) (string, error) {
	if path == "" {
		return "", errors.New("include path must not be empty")
	}
	path = hostenv.ExpandDataDir(path, l.dataDir)
	cwd := os.Getwd
	if from != "" {
		cwd = func() (string, error) { return filepath.Dir(from), nil }
	}
	abs, err := workspaceapi.ExpandPath(path, homeUser, cwd)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// homeUser is the user ~ expands to: the one $HOME names, as the rest of
// the config does.
func homeUser() (*user.User, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &user.User{HomeDir: home}, nil
}

// overlayRoot layers the config file at path, the user's config, onto
// base. A missing file leaves base as it is.
func (l *includeLoader) overlayRoot(path string, base map[string]any) (map[string]any, error) {
	abs, err := l.resolve(path, "")
	if err != nil {
		return nil, err
	}
	src, err := os.ReadFile(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return base, nil
		}
		return nil, err
	}
	return l.overlayBytes(src, abs, base, "")
}

// overlayFile layers the file at path, included from the file from, onto
// base. editorMode is the editor mode a package .star config is told. A
// missing file is recorded with warn and leaves base as it is.
func (l *includeLoader) overlayFile(
	path, from string, base map[string]any, editorMode string,
) (map[string]any, error) {
	abs, err := l.resolve(path, from)
	if err != nil {
		return nil, includeError(path, from, err)
	}
	if slices.Contains(l.stack, abs) {
		return nil, includeError(path, from, fmt.Errorf("include cycle: %s",
			strings.Join(append(slices.Clone(l.stack), abs), " -> ")))
	}
	if len(l.stack) >= maxIncludeDepth {
		return nil, includeError(path, from,
			fmt.Errorf("includes nest deeper than %d files", maxIncludeDepth))
	}
	src, err := os.ReadFile(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			l.warn(includeKey+" "+path,
				fmt.Errorf("%s: not found (included from %s)", abs, from))
			return base, nil
		}
		return nil, includeError(path, from, err)
	}
	cfg, err := l.overlayBytes(src, abs, base, editorMode)
	if err != nil {
		return nil, includeError(path, from, err)
	}
	return cfg, nil
}

func includeError(path, from string, err error) error {
	return fmt.Errorf("include %q (from %s): %w", path, from, err)
}

// overlayBytes layers src, the contents of the file at the absolute path
// file, onto base.
func (l *includeLoader) overlayBytes(
	src []byte, file string, base map[string]any, editorMode string,
) (map[string]any, error) {
	l.stack = append(l.stack, file)
	defer func() { l.stack = l.stack[:len(l.stack)-1] }()
	l.files = append(l.files, file)

	pkg, isPkg := l.packageOf(file)
	if isStarlarkConfigFilename(file) {
		return l.overlayStarlark(src, file, base, pkg, isPkg, editorMode)
	}
	return l.overlayYAML(src, file, base, pkg, isPkg)
}

func (l *includeLoader) overlayYAML(
	src []byte, file string, base map[string]any, pkg pkgInclude, isPkg bool,
) (map[string]any, error) {
	var layer map[string]any
	if err := yaml.Unmarshal(src, &layer); err != nil {
		return nil, err
	}
	if layer == nil {
		layer = map[string]any{}
	}
	includes, err := popIncludes(layer)
	if err != nil {
		return nil, err
	}
	cur := base
	if len(includes) > 0 {
		mode := includeEditorMode(cur, layer)
		for _, path := range includes {
			cur, err = l.overlayFile(path, file, cur, mode)
			if err != nil {
				return nil, err
			}
		}
	}
	if isPkg {
		pkg.normalize(layer)
	}
	foldVimSection(layer, nil)
	overlayLayer(cur, layer)
	return cur, nil
}

func (l *includeLoader) overlayStarlark(
	src []byte, file string, base map[string]any, pkg pkgInclude, isPkg bool,
	editorMode string,
) (map[string]any, error) {
	source := starlarkConfigSource{
		src:      src,
		filename: file,
		base:     base,
		include: func(path string, current map[string]any) (map[string]any, error) {
			return l.overlayFile(path, file, current, includeEditorMode(current, nil))
		},
	}
	if isPkg {
		source.params = pkg.starlarkParams(editorMode)
	}
	result, err := decodeStarlarkConfig(source)
	if err != nil {
		return nil, err
	}
	if isPkg {
		pkg.normalize(result)
	}
	foldVimSection(result, base)
	// The script read and wrote the tree as a whole, so what it set to
	// a value chaining on the previous one it concatenated itself.
	overrideConfig(base, result)
	return base, nil
}

// popIncludes removes the include key from layer and returns the paths it
// listed. A null value lists none.
func popIncludes(layer map[string]any) ([]string, error) {
	raw, ok := layer[includeKey]
	if !ok {
		return nil, nil
	}
	delete(layer, includeKey)
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list of file paths, got %T", includeKey, raw)
	}
	paths := make([]string, 0, len(list))
	for _, v := range list {
		s, ok := v.(string)
		if !ok || s == "" {
			return nil, fmt.Errorf("%s entries must be non-empty file paths, got %v (%T)",
				includeKey, v, v)
		}
		paths = append(paths, s)
	}
	return paths, nil
}

// includeEditorMode returns the editor mode the package .star configs
// included by a file are told: that of cur with the file's own editor
// section, if any, layered over it.
func includeEditorMode(cur, layer map[string]any) string {
	editor, _ := cur["editor"].(map[string]any)
	editor = copyConfigValue(editor).(map[string]any)
	if editor == nil {
		editor = map[string]any{}
	}
	if own, ok := layer["editor"].(map[string]any); ok {
		overrideConfig(editor, copyConfigValue(own).(map[string]any))
	}
	c := ideConfig{cfg: map[string]any{"editor": editor}, errors: map[string]error{}}
	return c.pkgEditorMode()
}

// pkgInclude is the package whose config a file under the etc directory is.
type pkgInclude struct {
	id string
	// version is the version in use, empty when none is.
	version string
}

// packageOf returns the package the file at abs is the config of, if it is
// under the data directory's etc directory.
func (l *includeLoader) packageOf(abs string) (pkgInclude, bool) {
	if l.dataDir == "" {
		return pkgInclude{}, false
	}
	rel, err := filepath.Rel(idepkg.EtcDir(l.dataDir), abs)
	if err != nil || rel == "." || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return pkgInclude{}, false
	}
	id := filepath.ToSlash(strings.TrimSuffix(rel, filepath.Ext(rel)))
	pkg := pkgInclude{id: id}
	if target, err := os.Readlink(idepkg.PackageLibDir(l.dataDir, id)); err == nil {
		pkg.version = filepath.Base(target)
	}
	return pkg, true
}

// starlarkParams returns the globals of the package's config.star. Every
// global is defined so a script that reads one runs with a version or mode
// that is not known yet.
func (p pkgInclude) starlarkParams(editorMode string) map[string]any {
	params := idepkg.StarlarkParams(p.id, release.Version(p.version), editorMode)
	params["RUNE_PKG_VERSION"] = p.version
	params["RUNE_EDITOR_MODE"] = editorMode
	return params
}

// normalize makes cfg, the decoded config of the package, a layer of the
// user's config: the requirements it lists are install-time metadata, and
// $RUNE_PKG_ID and $RUNE_PKG_VERSION name this package.
func (p pkgInclude) normalize(cfg map[string]any) {
	delete(cfg, idepkg.RequirementsKey)
	lookup := func(name string) (string, bool) {
		switch name {
		case "RUNE_PKG_ID":
			return p.id, true
		case "RUNE_PKG_VERSION":
			return p.version, p.version != ""
		}
		return "", false
	}
	for k, v := range cfg {
		cfg[k] = expandConfigStrings(v, lookup)
	}
}

// expandConfigStrings expands, in every string under v, the variables
// lookup knows, and leaves every other value as it is.
func expandConfigStrings(v any, lookup func(string) (string, bool)) any {
	switch t := v.(type) {
	case string:
		return hostenv.ExpandVars(t, lookup)
	case map[string]any:
		for k, e := range t {
			t[k] = expandConfigStrings(e, lookup)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = expandConfigStrings(e, lookup)
		}
		return t
	default:
		return v
	}
}

// overlayLayer deep-merges layer onto dst as overrideConfig does, after
// substituting in each gui.env variable layer sets a reference to the
// variable itself with the value dst has for it, so the PATH entries of
// each layer stack on the previous layer's instead of replacing them. A
// value equal to dst's is left alone, so layering the same file twice does
// not stack its entries.
func overlayLayer(dst, layer map[string]any) {
	chainGUIEnv(dst, layer)
	overrideConfig(dst, layer)
}

func chainGUIEnv(dst, layer map[string]any) {
	layerGUI, ok := layer["gui"].(map[string]any)
	if !ok {
		return
	}
	layerEnv, ok := layerGUI["env"].(map[string]any)
	if !ok {
		return
	}
	dstGUI, ok := dst["gui"].(map[string]any)
	if !ok {
		return
	}
	dstEnv, ok := dstGUI["env"].(map[string]any)
	if !ok {
		return
	}
	for name, v := range layerEnv {
		value, ok := v.(string)
		if !ok {
			continue
		}
		prev, ok := dstEnv[name].(string)
		if !ok || prev == value {
			continue
		}
		layerEnv[name] = hostenv.ExpandVars(value, func(ref string) (string, bool) {
			return prev, ref == name
		})
	}
}

var _ = starlarkconfig.IncludeBuiltin
