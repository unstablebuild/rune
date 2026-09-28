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

// Package loginshell exposes the `login` and `logout` REPL commands in
// the IDE shell. login streams the OAuth authorization URL inline as a
// markdown component and then reports the result, replacing the
// notification-based feedback used by the old command-prompt commands.
package loginshell

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/auth"
	"unstable.build/rune/cmd/rune/ide/apiclient"
	"unstable.build/rune/internal/component/markdown"
)

// Client is the subset of *apiclient.Client the login shell needs.
type Client interface {
	Login(ctx context.Context) apiclient.LoginSession
	Logout(ctx context.Context) error
	AccountStatus(ctx context.Context) (auth.RPCUser, bool, error)
}

const (
	loginSummary  = "Authenticate your Rune client."
	logoutSummary = "Log out from the current session so you can login with a different account."
)

// Login returns the command manual and REPL handler for the `login`
// command, which streams the OAuth authorization URL inline and then
// reports the result.
func Login(client Client) (textapi.CommandManual, textapi.REPLHandler) {
	return textapi.CommandManual{Name: "login", Summary: loginSummary},
		loginHandler{client: client}
}

// Logout returns the command manual and REPL handler for the `logout`
// command, which clears the current authentication credentials.
func Logout(client Client) (textapi.CommandManual, textapi.REPLHandler) {
	return textapi.CommandManual{Name: "logout", Summary: logoutSummary},
		logoutHandler{client: client}
}

type loginHandler struct {
	client Client
}

func (h loginHandler) HandleCommand(
	ctx context.Context, _ repl.Command, _ repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	session := h.client.Login(ctx)
	urlEmitted := false
	waited := false
	return iterator.FromFunc(
		func(ctx context.Context) (component.Responsive, bool, error) {
			if !urlEmitted {
				urlEmitted = true
				select {
				case <-ctx.Done():
					return nil, false, ctx.Err()
				case u, ok := <-session.URL:
					if ok && u != nil {
						return markdownResponsive(formatLoginStart(u)), true, nil
					}
				}
			}
			if waited {
				return nil, false, nil
			}
			waited = true
			select {
			case <-ctx.Done():
				return nil, false, ctx.Err()
			case err := <-session.Done:
				if err != nil {
					return markdownResponsive(
						fmt.Sprintf("Login did not complete: `%v`.", err)), true, nil
				}
				user, ok, err := h.client.AccountStatus(ctx)
				if err != nil {
					return nil, false, err
				}
				if !ok {
					return nil, false, errors.New(
						"login completed but no account token was stored")
				}
				return markdownResponsive(formatAccountStatus(user)), true, nil
			}
		},
		func() error { return nil },
	), nil
}

func (loginHandler) Complete(
	context.Context, string, []string,
) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

func (loginHandler) Help(
	context.Context, []string,
) (iterator.Iterator[component.Responsive], error) {
	return markdownOutput(loginSummary), nil
}

type logoutHandler struct {
	client Client
}

func (h logoutHandler) HandleCommand(
	ctx context.Context, _ repl.Command, _ repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if err := h.client.Logout(ctx); err != nil {
		return nil, err
	}
	return markdownOutput("Logged out."), nil
}

func (logoutHandler) Complete(
	context.Context, string, []string,
) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

func (logoutHandler) Help(
	context.Context, []string,
) (iterator.Iterator[component.Responsive], error) {
	return markdownOutput(logoutSummary), nil
}

func formatLoginStart(u *url.URL) string {
	var b strings.Builder
	b.WriteString("Open this URL in your browser to complete login:\n\n")
	b.WriteByte('<')
	b.WriteString(u.String())
	b.WriteString(">\n")
	return b.String()
}

func formatAccountStatus(u auth.RPCUser) string {
	var b strings.Builder
	b.WriteString("## Login successful\n\n")
	if u.Email != "" {
		fmt.Fprintf(&b, "- **Account**: `%s`\n", u.Email)
	}
	fmt.Fprintf(&b, "- **Plan**: %s\n", planLabel(u.Role))
	if u.Role == auth.RoleOneOff && !u.PlanEnds.IsZero() {
		fmt.Fprintf(&b, "- **Upgrades covered through**: %s\n",
			u.PlanEnds.Format("2006-01-02"))
	} else if u.Role >= auth.RolePaid && !u.PlanEnds.IsZero() {
		fmt.Fprintf(&b, "- **Renews**: %s\n", u.PlanEnds.Format("2006-01-02"))
	}
	if u.Role != auth.RoleOneOff && u.Role < auth.RolePaid {
		b.WriteString("\nUpgrade to Rune Pro to access Rune networking features and premium support\n")
	}
	return b.String()
}

func planLabel(role auth.Role) string {
	switch {
	case role == auth.RoleOneOff:
		return "One-off"
	case role >= auth.RolePaid:
		return "Rune Pro"
	default:
		return "Rune"
	}
}

func markdownOutput(content string) iterator.Iterator[component.Responsive] {
	md, err := markdown.New(content)
	if err != nil {
		r := component.NewResponsiveString(content, component.StringResponsiveConfig{})
		return iterator.FromSlice([]component.Responsive{r})
	}
	return iterator.FromSlice([]component.Responsive{md})
}

func markdownResponsive(content string) component.Responsive {
	it := markdownOutput(content)
	v, _ := it.Next(context.Background())
	return v
}
