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

package runenet

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	multierr "github.com/ernestrc/go-multierror"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"unstable.build/rune/internal/debug"
)

// DefaultPort is the tailnet-side TCP port the workspace server listens
// on. It is only reachable from the mesh, never from the host's other
// interfaces.
const DefaultPort = 7473

// Environment overrides for the coordination server and pre-auth key.
// They are only read by debug builds, so a release binary always joins
// with the credentials the account server hands out.
const (
	EnvControlURL = "RUNE_NETWORK_CONTROL_URL"
	EnvAuthKey    = "RUNE_NETWORK_AUTH_KEY"
)

// CredentialsFunc returns the coordination server and the pre-auth key
// this machine joins the mesh with.
type CredentialsFunc func(ctx context.Context) (controlURL, authKey string, err error)

// Config is the `network` section of the Rune configuration.
type Config struct {
	// AutoJoin joins the network at startup. The node is assembled
	// either way, so the `network up` console command can join later
	// without a restart; AutoJoin only decides whether boot does it
	// unprompted.
	AutoJoin bool
	// Hostname is the name this machine advertises to its peers and
	// the name used in rune://<hostname> workspace URIs. Defaults to
	// the OS hostname.
	Hostname string
	// ControlURL is the coordination server. Empty uses Tailscale's.
	// See CustomControl for whether it is the user's own.
	ControlURL string
	// CustomControl is set when ControlURL is the user's own Tailscale
	// or Headscale server, which `rune --headless --control-url` joins.
	// Such a node has no Rune account behind it: it signs in by URL,
	// never with a key, and keeps its identity apart from the one it
	// holds on Rune's network.
	CustomControl bool
	// AuthKey pre-authorizes this node so the first start does not
	// need an interactive browser login. Empty falls back to the login
	// URL reported by `network status`.
	AuthKey string
	// Credentials mints ControlURL and AuthKey when the node joins.
	// The account server decides which mesh a machine may join, and
	// how many machines the account's plan covers, so neither value
	// is user-configurable. A nil Credentials joins with the static
	// ControlURL and AuthKey above.
	Credentials CredentialsFunc
	// OnNeedsLogin is called when the coordination server stops
	// accepting this machine's node key, which is what being removed
	// from the account looks like from here. It runs on a background
	// goroutine and must not block.
	OnNeedsLogin func()
	// OnAuthURL is called with every new sign-in URL the coordination
	// server issues, including one already pending when the node
	// starts. A URL is reported once. It runs on a background
	// goroutine and must not block.
	OnAuthURL func(url string)
	// Port is the tailnet-side listening port. See DefaultPort.
	Port int
	// Dir is the data directory the node persists its identity under.
	Dir string
}

// FromConfig parses the `network` config section. dir is the data
// directory the node persists its identity under.
func FromConfig(cfg config.Config, dir string) (ret Config, retErr error) {
	autoJoin, err := cfg.GetBool("auto_join")
	if err != nil && err != config.ErrNotFound {
		retErr = multierr.Append(retErr, err)
	}
	hostname, err := cfg.GetString("hostname")
	if err != nil && err != config.ErrNotFound {
		retErr = multierr.Append(retErr, err)
	}
	port, err := cfg.GetInt("port")
	if err == config.ErrNotFound || port == 0 {
		port = DefaultPort
	} else if err != nil {
		retErr = multierr.Append(retErr, err)
	}
	if retErr != nil {
		return ret, fmt.Errorf("could not load network config: %s", retErr)
	}

	if hostname == "" {
		// A node without a name is unreachable by rune://<peer>, so
		// fall back to the OS name rather than tsnet's default (the
		// binary name), which would be identical on every machine.
		hostname, _ = os.Hostname()
	}
	ret.AutoJoin = autoJoin
	ret.Hostname = hostname
	ret.Port = port
	ret.Dir = dir
	if debug.DebugBuild == "true" {
		ret.ControlURL = os.Getenv(EnvControlURL)
		ret.AuthKey = os.Getenv(EnvAuthKey)
	}
	return ret, nil
}

// ParseControlURL normalizes the URL of the user's own coordination
// server: an http:// or https:// URL with a host, without a trailing
// slash.
func ParseControlURL(raw string) (string, error) {
	raw = strings.TrimSuffix(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("control URL %q: %w", raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf(
			"control URL %q: want an http:// or https:// URL with a host", raw)
	}
	return raw, nil
}
