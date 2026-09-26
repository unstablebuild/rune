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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/Xuanwo/go-locale"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"golang.org/x/text/language"
	"unstable.build/rune/internal/debug"
)

const fallbackLocale = "UTF-8"

// runeShellEnvMarker is printed by the login-shell probe immediately before the
// shell's environment dump so we can ignore any banner chatter rc files emit on
// stdout before our probe runs, and parse only the env that follows it.
const runeShellEnvMarker = "RUNE_SHELL_ENV_START"

// loginPathTimeout bounds the login-shell probe so a wedged shell can never
// block startup.
const loginPathTimeout = 10 * time.Second

var darwinRe = regexp.MustCompile("UserShell: (/[^ ]+)\n")

func setupRuneBinPATH(dataDir string) error {
	if err := makePkgDirs(dataDir); err != nil {
		return err
	}
	return setRuneBinPATH(dataDir, os.Getenv("PATH"))
}

func startLoginShellPATHResolve(dataDir string) <-chan error {
	done := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		login, err := resolveLoginPath(loginPathTimeout, userShell)
		if err != nil {
			done <- err
			return
		}
		done <- setRuneBinPATH(dataDir, login)
	})
	return done
}

func setRuneBinPATH(dataDir, base string) error {
	binDir := filepath.Join(dataDir, "bin")
	if err := os.Setenv("PATH", prependPATH(binDir, base, os.PathListSeparator)); err != nil {
		return fmt.Errorf("set env PATH: %w", err)
	}
	return nil
}

// prependPATH returns base with dir in front of it, joined by the list
// separator sep.
func prependPATH(dir, base string, sep rune) string {
	return dir + string(sep) + base
}

func makePkgDirs(dataDir string) error {
	for _, sub := range []string{"bin", "lib"} {
		dir := filepath.Join(dataDir, sub)
		if err := os.MkdirAll(dir, 0o777); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	return nil
}

// pathFromMarkerEnv extracts PATH from a NUL-delimited `/usr/bin/env -0` dump
// that follows the env marker. Everything before the last marker occurrence is
// banner chatter and is ignored, so rc-file output that happens to look like
// KEY=VALUE cannot shadow the real environment.
func pathFromMarkerEnv(out string) string {
	if idx := strings.LastIndex(out, runeShellEnvMarker); idx >= 0 {
		out = out[idx+len(runeShellEnvMarker):]
	}
	for entry := range strings.SplitSeq(out, "\x00") {
		if v, ok := strings.CutPrefix(entry, "PATH="); ok {
			return v
		}
	}
	return ""
}

func setEnvForGUI(dataPath string) {
	os.Setenv("RUNE_DATADIR", dataPath)

	// set vte vars
	os.Setenv("TERM", "xterm-256color")
	os.Setenv("COLORTERM", "truecolor")

	// https://specifications.freedesktop.org/startup-notification-spec/startup-notification-0.1.txt
	os.Unsetenv("DESKTOP_STARTUP_ID")
	// https://wayland.app/protocols/xdg-activation-v1
	os.Unsetenv("XDG_ACTIVATION_TOKEN")

	if os.Getenv("SHELL") == "" {
		shell, err := userShell()
		if err != nil {
			log.Errorf("shell detect: %v", err)
			shell = "bash"
		}
		os.Setenv("SHELL", shell)
	}

	u, err := user.Current()
	if err == nil {
		os.Setenv("USER", u.Username)
		os.Setenv("HOME", u.HomeDir)
	} else {
		log.Errorf("user detect: %v", err)
	}

	tag, err := locale.Detect()
	if err != nil {
		log.Errorf("locale detect: %v", err)
	} else {
		base, baseConfidence := tag.Base()
		region, regionConfidence := tag.Region()
		if baseConfidence != language.No && regionConfidence != language.No {
			value := fmt.Sprintf("%s_%s.UTF-8", base.String(), region.String())
			log.Debugf("setting LC_ALL to %q", value)
			os.Setenv("LC_ALL", value)
			return
		}
	}

	log.Debugf("setting LC_CTYPE to %q", fallbackLocale)
	os.Setenv("LC_CTYPE", fallbackLocale)
}

func userShell() (string, error) {
	switch runtime.GOOS {
	case "plan9":
		return plan9Shell()
	case "linux":
		return nixShell()
	case "openbsd":
		return nixShell()
	case "freebsd":
		return nixShell()
	case "darwin":
		return darwinShell()
	case "windows":
		return windowsShell()
	}
	return "", fmt.Errorf("undefined GOOS: %s", runtime.GOOS)
}

func plan9Shell() (string, error) {
	if _, err := os.Stat("/dev/osversion"); err != nil {
		if os.IsNotExist(err) {
			return "", err
		} else {
			return "", errors.New("/dev/osversion check failed")
		}
	}

	return "/bin/rc", nil
}

func nixShell() (string, error) {
	user, err := user.Current()
	if err != nil {
		return "", err
	}

	out, err := exec.Command("getent", "passwd", user.Uid).Output()
	if err != nil {
		return "", err
	}

	ent := strings.Split(strings.TrimSuffix(string(out), "\n"), ":")
	return ent[6], nil
}

func darwinShell() (string, error) {
	dir := "Local/Default/Users/" + os.Getenv("USER")
	out, err := exec.Command("dscl", "localhost", "-read", dir, "UserShell").Output()
	if err != nil {
		return "", err
	}

	matched := darwinRe.FindStringSubmatch(string(out))
	shell := matched[1]
	if shell == "" {
		return "", fmt.Errorf("invalid output: %s", string(out))
	}

	return shell, nil
}

func windowsShell() (string, error) {
	consoleApp := os.Getenv("COMSPEC")
	if consoleApp == "" {
		consoleApp = "cmd.exe"
	}

	return consoleApp, nil
}

// applyGUIEnvVars applies the resolved gui.env config block to the local
// process environment with os.Setenv, expanding values against a stable
// baseline so repeated applications do not duplicate self-referential entries.
// It captures the baseline on first use.
func applyGUIEnvVars(env config.Config) error {
	return applyGUIEnvVarsWithLookup(env, os.Getenv)
}

// applyGUIEnvVarsWithLookup applies env to the local process environment,
// expanding string values with the given lookup. It preserves evalVar
// semantics: strings expand via os.Expand, non-strings use fmt.Sprintf("%v").
func applyGUIEnvVarsWithLookup(env config.Config, lookup func(string) string) error {
	var err error
	env.Iterate(func(k string, value any) {
		if setErr := os.Setenv(k, evalVarWithLookup(value, lookup)); setErr != nil && err == nil {
			err = fmt.Errorf("set env %s: %w", k, setErr)
		}
	})
	return err
}

// evalVarWithLookup mirrors evalVar but expands string values against the
// given lookup instead of the live environment.
func evalVarWithLookup(value any, lookup func(string) string) string {
	str, ok := value.(string)
	if !ok {
		return fmt.Sprintf("%v", value)
	}
	return os.Expand(str, lookup)
}
