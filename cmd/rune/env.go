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
	"os/exec"
	"os/user"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/Xuanwo/go-locale"
	log "github.com/sirupsen/logrus"
	"golang.org/x/text/language"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/hostenv"
)

const fallbackLocale = "UTF-8"

// loginPathTimeout bounds the login-shell probe so a wedged shell can never
// block startup.
const loginPathTimeout = 10 * time.Second

var darwinRe = regexp.MustCompile("UserShell: (/[^ ]+)\n")

// startLoginShellPATHResolve probes the user's login shell PATH in the
// background and makes it the base PATH of host. The channel receives the
// outcome once.
func startLoginShellPATHResolve(host *hostenv.Host) <-chan error {
	done := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		ctx, cancel := context.WithTimeout(context.Background(), loginPathTimeout)
		defer cancel()
		login, err := hostenv.ProbeLoginPATH(ctx, userShell)
		if err != nil {
			done <- err
			return
		}
		done <- host.SetBasePATH(login)
	})
	return done
}

func setEnvForGUI() {
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
