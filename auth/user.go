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

package auth

import "time"

// Role defines a user role.
type Role uint8

// List of roles.
const (
	RoleBasic Role = iota
	RoleUser
	RolePaid
	RoleAdmin
	RoleSuperAdmin
	// RoleOneOff is a one-off purchase that entitles the user to a
	// fixed window of upgrades rather than a recurring subscription.
	// It is appended last to preserve the wire values of the existing
	// roles in already-issued tokens and Auth0 metadata; it therefore
	// sorts above RolePaid numerically and must be matched explicitly
	// (not via role >= RolePaid) wherever subscription entitlement is
	// checked.
	RoleOneOff
)

// SubscriptionRequiredMessage is the response body the API server's
// paid gate writes with its 403. It is what distinguishes "this
// account holds no plan" from every other 403 the auth middleware
// answers with, so clients must match it before telling a user to
// upgrade.
const SubscriptionRequiredMessage = "subscription required"

// MachineLimitMessage is the response body the API server writes with
// its 403 when an account already has as many machines on the network
// as its plan allows. Like SubscriptionRequiredMessage it is what
// tells this 403 apart from every other one the auth middleware
// answers with, so clients must match it before telling a user to
// remove a machine or upgrade.
const MachineLimitMessage = "machine limit reached"

// Network endpoints a Rune instance talks to for its mesh membership.
// They are spelled here because both ends must agree on them and the
// API server cannot import the client's internals.
const (
	// NetworkNodesPath lists the machines registered to the calling
	// account.
	NetworkNodesPath = "/api/network/nodes"
	// NetworkNodeRemovePath removes one of them, freeing a slot for
	// the next machine that asks for credentials.
	NetworkNodeRemovePath = "/api/network/nodes/remove"
)

// ServeTagPrefix starts the mesh tag the API server puts on an
// account's serve-only machines; the rest of the tag is the account's
// mesh user name. A serve-only machine accepts connections from its
// account's machines and cannot open any, and since a tagged machine
// no longer reports the account as its owner, the tag is also how it
// knows which account it serves.
const ServeTagPrefix = "tag:serve-"

// RPCUser represents a rune user, from an auth point of view.
type RPCUser struct {
	ID      string
	Email   string
	Role    Role
	Account string

	// PlanEnds is the timestamp at which the user's current paid plan
	// period ends. Zero when the user has never had a paid plan. The
	// client uses this to drive the 7-day soft-warn grace window after
	// a subscription lapses without yet hitting hard lockdown.
	PlanEnds time.Time `json:"plan_ends,omitzero"`

	// ServeOnly marks a token issued to a serve-only machine, a
	// `rune --headless` node: the API server mints it only for sign-ins
	// through Config.HeadlessClientID and keeps it on refresh, so a
	// stolen serve-only token never becomes a full-access one. The API
	// server answers it with tagged network keys and denies it every
	// endpoint a headless node does not need.
	ServeOnly bool `json:"serve_only,omitempty"`
}

// String returns the string representation of this role.
func (r Role) String() string {
	switch r {
	case RoleBasic:
		return "basic"
	case RoleUser:
		return "user"
	case RolePaid:
		return "paid"
	case RoleAdmin:
		return "admin"
	case RoleSuperAdmin:
		return "superadmin"
	case RoleOneOff:
		return "oneoff"
	default:
		panic("unknown role")
	}
}

// WebUser represents a website user, provided directly
// from an oauth2 provider claims.
type WebUser struct {
	// ServeOnly is set when the bearer is not a website token but an
	// RPCUser token with RPCUser.ServeOnly, which the API server's web
	// verifier also accepts because both are signed with its keys. The
	// API server refuses such a bearer on every web endpoint.
	ServeOnly bool `json:"serve_only,omitempty"`
}
