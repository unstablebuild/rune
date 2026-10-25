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

package ideauthorizer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
)

const (
	pluginPermissionDecisionAllow    = "allow"
	pluginPermissionDecisionDeny     = "deny"
	pluginPermissionOnceTTL          = 10 * time.Minute
	pluginPermissionStoragePrefix    = "extensionv2:plugin-permissions:"
	extensionPermissionStoragePrefix = "extensionv2:extension-permissions:"
	executableStoragePrefix          = "extensionv2:executables:"
)

type storedPermissionDecision struct {
	Key        string
	Decision   string
	Path       string
	Args       []string
	Permission extensionapi.Permission
	Command    pluginPermissionCommandDetail
	// Scope describes the commands a command decision covers, such as
	// "git commit *". Decisions saved before scopes existed have none.
	Scope string
	// Executables are the files the commands of Scope run.
	Executables []executableRef
}

// legacyCommand reports a command decision saved by a version of Rune
// that didn't bind decisions to executables, which covered every program
// with the same name.
func (d storedPermissionDecision) legacyCommand() bool {
	return d.Command.Path != "" && d.Scope == ""
}

type executableRef struct {
	Name string
	Host string
	Path string
}

// storedExecutable is the fingerprint an executable had when the user
// last approved a command that runs it.
type storedExecutable struct {
	Key     string
	Host    string
	Path    string
	SHA256  string
	Size    int64
	ModTime time.Time
	Updated time.Time
}

func executableStorageKey(host, path string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(host))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(path))
	return executableStoragePrefix + hex.EncodeToString(h.Sum(nil))
}

// commandScopeKey returns the storage key of a command scope, under the
// key prefix of an extension or plugin's execute permission.
func commandScopeKey(prefix string, parts ...string) string {
	h := sha256.New()
	_, _ = h.Write([]byte("v2"))
	_, _ = h.Write([]byte{0})
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return prefix + ":" + hex.EncodeToString(h.Sum(nil))
}

type pluginPermissionOnceDecision struct {
	Decision   string
	Path       string
	Args       []string
	Permission extensionapi.Permission
	Command    pluginPermissionCommandDetail
	Expires    time.Time
}

type pluginPermissionIdentity struct {
	PID          int
	UID          uint32
	Path         string
	Args         []string
	LauncherPath string
	LauncherArgs []string
}

type pluginPermissionCommandDetail struct {
	Path string
	Args []string
	Dir  string
	// Env holds the variables the command sets to values other than the
	// IDE's own.
	Env []string
}

func pluginPermissionStorageKey(
	path string, args []string, perm extensionapi.Permission,
) string {
	return strings.Join([]string{
		"extensionv2",
		"plugin-permissions",
		pluginProgramHash(path, args),
		url.PathEscape(string(perm)),
	}, ":")
}

// pluginPermissionProgramStorageKey returns the storage key for a
// program-scoped API-access decision. Unlike pluginPermissionStorageKey it
// ignores argv so that an "Always" grant for an executable persists across
// invocations launched with different arguments.
func pluginPermissionProgramStorageKey(
	path string, perm extensionapi.Permission,
) string {
	return strings.Join([]string{
		"extensionv2",
		"plugin-permissions",
		pluginProgramHash(path, nil),
		url.PathEscape(string(perm)),
	}, ":")
}

func extensionPermissionStorageKey(
	ext Extension, perm extensionapi.Permission,
) string {
	return strings.Join([]string{
		"extensionv2",
		"extension-permissions",
		pluginProgramHash(ext.ExtensionID, []string{
			ext.DeveloperID,
			ext.DeveloperKey,
			ext.ExtensionName,
		}),
		url.PathEscape(string(perm)),
	}, ":")
}

func pluginPermissionOnceKey(
	identity pluginPermissionIdentity, perm extensionapi.Permission,
	command *pluginPermissionCommandDetail,
) string {
	if identity.PID == 0 {
		return ""
	}
	parts := []string{
		"extensionv2",
		"plugin-permission-once",
		pluginProgramHashWithProcess(identity.PID, identity.UID, identity.Path, identity.Args),
		url.PathEscape(string(perm)),
	}
	if command != nil {
		parts = append(parts, pluginPermissionCommandHash(*command))
	}
	return strings.Join(parts, ":")
}

func stablePermissionOnceKey(
	identity pluginPermissionIdentity, perm extensionapi.Permission,
	command *pluginPermissionCommandDetail,
) string {
	if identity.Path == "" {
		return ""
	}
	parts := []string{
		"extensionv2",
		"extension-permission-once",
		pluginProgramHash(identity.Path, identity.Args),
		url.PathEscape(string(perm)),
	}
	if command != nil {
		parts = append(parts, pluginPermissionCommandHash(*command))
	}
	return strings.Join(parts, ":")
}

func pluginProgramHash(path string, args []string) string {
	h := sha256.New()
	writePluginProgramHash(h, path, args)
	return hex.EncodeToString(h.Sum(nil))
}

func pluginProgramHashWithProcess(pid int, uid uint32, path string, args []string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(strconv.Itoa(pid)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.FormatUint(uint64(uid), 10)))
	_, _ = h.Write([]byte{0})
	writePluginProgramHash(h, path, args)
	return hex.EncodeToString(h.Sum(nil))
}

func pluginPermissionCommandHash(command pluginPermissionCommandDetail) string {
	h := sha256.New()
	writePluginPermissionCommandHash(h, command)
	return hex.EncodeToString(h.Sum(nil))
}

func writePluginPermissionCommandHash(
	h hashWriter, command pluginPermissionCommandDetail,
) {
	_, _ = h.Write([]byte(command.Path))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(command.Dir))
	_, _ = h.Write([]byte{0})
	for _, arg := range command.Args {
		_, _ = h.Write([]byte(arg))
		_, _ = h.Write([]byte{0})
	}
	if len(command.Env) > 0 {
		_, _ = h.Write([]byte("env"))
		_, _ = h.Write([]byte{0})
		for _, kv := range command.Env {
			_, _ = h.Write([]byte(kv))
			_, _ = h.Write([]byte{0})
		}
	}
}

func pluginPermissionExactCommandLabel(command pluginPermissionCommandDetail) string {
	parts := []string{command.Path}
	if len(command.Args) > 0 {
		parts = append(parts, strings.Join(command.Args, " "))
	}
	if command.Dir != "" {
		parts = append(parts, "@ "+command.Dir)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func copyPluginPermissionCommandDetail(
	command pluginPermissionCommandDetail,
) pluginPermissionCommandDetail {
	return pluginPermissionCommandDetail{
		Path: command.Path,
		Args: append([]string(nil), command.Args...),
		Dir:  command.Dir,
		Env:  append([]string(nil), command.Env...),
	}
}

func writePluginProgramHash(h hashWriter, path string, args []string) {
	_, _ = h.Write([]byte(path))
	_, _ = h.Write([]byte{0})
	for _, arg := range args {
		_, _ = h.Write([]byte(arg))
		_, _ = h.Write([]byte{0})
	}
}

type hashWriter interface {
	Write([]byte) (int, error)
}

// PermissionActionText returns the phrase used to describe the action
// granted by an extension permission, e.g. "access the editor".
func PermissionActionText(permission extensionapi.Permission) string {
	switch permission {
	case extensionapi.PermissionFileSystem:
		return "access workspace files"
	case extensionapi.PermissionExecute:
		return "execute commands"
	case extensionapi.PermissionTerminal:
		return "manage terminals"
	case extensionapi.PermissionBrowserWindowManager:
		return "manage the window manager"
	case extensionapi.PermissionBrowserResourceOpener:
		return "open files and links"
	case extensionapi.PermissionNotifications:
		return "show notifications"
	case extensionapi.PermissionInterrupt:
		return "render TUIs asynchronously"
	case extensionapi.PermissionEditor:
		return "access editor buffers and file events"
	case extensionapi.PermissionCommands:
		return "register prompt and console commands"
	case extensionapi.PermissionStorage:
		return "use persistent storage"
	case extensionapi.PermissionSyntaxTree:
		return "inspect syntax trees"
	case extensionapi.PermissionConfig:
		return "read user configuration"
	case extensionapi.PermissionLSP:
		return "communicate with LSP servers"
	case extensionapi.PermissionDebugger:
		return "communicate with DAP servers"
	case extensionapi.PermissionLLM:
		return "use your configured LLM providers"
	case extensionapi.PermissionPackages:
		return "find and install Rune packages"
	default:
		return fmt.Sprintf("access permission %s", permission)
	}
}

func extensionPermissionIdentity(ext Extension) pluginPermissionIdentity {
	return pluginPermissionIdentity{
		Path: ext.ExtensionID,
		Args: []string{ext.DeveloperID, ext.DeveloperKey, ext.ExtensionName},
	}
}

func pluginPermissionIdentityFromContext(
	ctx context.Context, ext Extension,
) pluginPermissionIdentity {
	ret := pluginPermissionIdentity{
		Path: ext.Path,
		Args: append([]string(nil), ext.Args...),
	}
	process, ok := peerProcessFromContext(ctx)
	if !ok {
		return ret
	}
	path := process.ProgramPath()
	if path == "" {
		return ret
	}
	ret.Path = path
	ret.Args = process.ProgramArgs()
	ret.PID = process.PID
	ret.UID = process.UID
	if ret.Path != ext.Path || !sameStringSlice(ret.Args, ext.Args) {
		ret.LauncherPath = ext.Path
		ret.LauncherArgs = append([]string(nil), ext.Args...)
	}
	return ret
}
