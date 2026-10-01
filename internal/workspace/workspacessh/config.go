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

package workspacessh

import (
	"fmt"
	"os"
	"time"

	multierr "github.com/ernestrc/go-multierror"
	"github.com/unstablebuild/rune-go-sdk/api/config"
)

const (
	defSSHTimeout = 5 * time.Second
)

type sshConfig struct {
	privateKeys []string
	timeout     time.Duration
	command     string
	shell       string
	insecure    bool
	// kbdInteractive enables PAM-style keyboard-interactive auth. Off by
	// default because the Go ssh client surfaces a confusing "unexpected
	// message type 51" error when the server advertises kbd-interactive
	// but does not actually configure any challenges.
	kbdInteractive bool
	// knownHostsPath overrides the default ~/.ssh/known_hosts path. Empty
	// means use the default.
	knownHostsPath string
	// skipPreflight disables the `which rune` and `ls <path>` checks
	// that connectScheme runs before spawning the workspace server.
	// Useful when the remote sshd is configured with a tight
	// MaxSessions budget (the pre-flight uses one extra channel each)
	// or when the user wants the slowest path on the connect critical
	// path. The pre-flight is purely diagnostic: it surfaces friendlier
	// errors when the binary is missing or the path doesn't exist; if
	// skipped, those failures will instead manifest as a less-helpful
	// startup error from the rune worker itself.
	skipPreflight bool
	// provisionPackages controls whether the local IDE mirrors its
	// in-use toolchain packages onto the remote `rune -x` server (the
	// `--install` manifest) before it starts serving. Defaults to true;
	// set to false to skip provisioning entirely, e.g. when the remote
	// already has a managed toolchain or when installs are unwanted on
	// the connect critical path.
	provisionPackages bool
	// strictHostKeyChecking controls how unknown or changed host keys are
	// handled. Defaults to true, matching OpenSSH's `ask` semantics: an
	// unknown host prompts trust-on-first-use and a changed key prompts a
	// strong warning, persisting the accepted key to known_hosts. When
	// false the accepted key is recorded without a prompt (still safer
	// than insecure, which bypasses verification entirely).
	strictHostKeyChecking bool
}

func fromConfig(cfg config.Config) (ret sshConfig, retErr error) {
	timeout, err := getTimeout(cfg)
	if err != nil && err != config.ErrNotFound {
		retErr = multierr.Append(retErr, err)
	}
	command, err := getCommand(cfg)
	if err != nil && err != config.ErrNotFound {
		retErr = multierr.Append(retErr, err)
	}
	shell, err := getShell(cfg)
	if err != nil && err != config.ErrNotFound {
		retErr = multierr.Append(retErr, err)
	}
	privateKeys, err := getPrivateKeys(cfg)
	if err != nil && err != config.ErrNotFound {
		retErr = multierr.Append(retErr, err)
	}
	insecure, err := getInsecure(cfg)
	if err != nil && err != config.ErrNotFound {
		retErr = multierr.Append(retErr, err)
	}
	kbdInteractive, err := cfg.GetBool("kbd_interactive")
	if err != nil && err != config.ErrNotFound {
		retErr = multierr.Append(retErr, err)
	}
	knownHosts, err := cfg.GetString("known_hosts")
	if err != nil && err != config.ErrNotFound {
		retErr = multierr.Append(retErr, err)
	}
	skipPreflight, err := cfg.GetBool("skip_preflight")
	if err != nil && err != config.ErrNotFound {
		retErr = multierr.Append(retErr, err)
	}
	provisionPackages, err := getProvisionPackages(cfg)
	if err != nil && err != config.ErrNotFound {
		retErr = multierr.Append(retErr, err)
	}
	strictHostKeyChecking, err := getStrictHostKeyChecking(cfg)
	if err != nil && err != config.ErrNotFound {
		retErr = multierr.Append(retErr, err)
	}
	if retErr != nil {
		retErr = fmt.Errorf("could not load ssh config: %s", retErr)
		return
	}

	ret.timeout = timeout
	ret.command = command
	ret.privateKeys = privateKeys
	ret.shell = shell
	ret.insecure = insecure
	ret.kbdInteractive = kbdInteractive
	ret.knownHostsPath = knownHosts
	ret.skipPreflight = skipPreflight
	ret.provisionPackages = provisionPackages
	ret.strictHostKeyChecking = strictHostKeyChecking
	return
}

// getProvisionPackages reads workspace.ssh.provision_packages, defaulting to
// true when unset so remote toolchain provisioning is on out of the box.
func getProvisionPackages(cfg config.Config) (bool, error) {
	v, err := cfg.GetBool("provision_packages")
	if err == config.ErrNotFound {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return v, nil
}

// getStrictHostKeyChecking reads workspace.ssh.strict_host_key_checking,
// defaulting to true when unset so unknown/changed host keys prompt before
// being trusted.
func getStrictHostKeyChecking(cfg config.Config) (bool, error) {
	v, err := cfg.GetBool("strict_host_key_checking")
	if err == config.ErrNotFound {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return v, nil
}

func getTimeout(cfg config.Config) (ret time.Duration, err error) {
	ret = defSSHTimeout

	sshTimeout, err := config.GetDuration(cfg, "timeout", defSSHTimeout)
	if err != nil {
		return
	}

	ret = sshTimeout
	return
}

func getInsecure(cfg config.Config) (ret bool, err error) {
	ret, err = cfg.GetBool("insecure")
	return
}

func getCommand(cfg config.Config) (string, error) {
	cmd, err := cfg.GetString("command")
	if err != nil {
		return "", err
	}

	return cmd, nil
}

func getShell(cfg config.Config) (string, error) {
	cmd, err := cfg.GetString("shell")
	if err != nil {
		return "", err
	}

	return cmd, nil
}

func getPrivateKeys(cfg config.Config) (ret []string, err error) {
	keyIfcs, err := cfg.GetSlice("private_keys")
	if err != nil {
		return nil, err
	}

	for _, keyIfc := range keyIfcs {
		key, ok := keyIfc.(string)
		if !ok {
			err = multierr.Append(err, fmt.Errorf("slice of strings expected for 'private_keys' but found %v", key))
			continue
		}
		ret = append(ret, os.ExpandEnv(key))
	}
	if err != nil {
		return nil, err
	}
	return ret, err
}
