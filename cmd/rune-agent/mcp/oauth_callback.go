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

package mcp

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/llm/authcallback"
)

const callbackPath = "/callback"

// callbackResult is the outcome of the authorization redirect.
type callbackResult struct {
	code  string
	state string
	iss   string
	err   error
}

// callbackServer receives the authorization server's redirect on a
// loopback port. It accepts exactly one authorization response, either
// from the browser or pasted by the user when the browser runs on
// another machine.
type callbackServer struct {
	ln    net.Listener
	srv   *http.Server
	state string
	ch    chan callbackResult
	once  sync.Once
}

// startCallbackServer listens on 127.0.0.1:port, or on a free port when
// port is zero. Only IPv4 loopback is bound, so the listener is exactly
// the address RedirectURL names.
func startCallbackServer(port int) (*callbackServer, error) {
	ln, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("listen for the sign-in redirect: %w", err)
	}
	cb := &callbackServer{
		ln:    ln,
		state: rand.Text(),
		ch:    make(chan callbackResult, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, cb.handle)
	cb.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go debug.CapturePanicReport(func() {
		_ = cb.srv.Serve(ln)
	})
	return cb, nil
}

// RedirectURL is the redirect URI to register and authorize with. It
// names the loopback IP literal rather than localhost, as RFC 8252
// recommends: where localhost resolves to ::1 first, or another program
// holds the same port on ::1, a browser sent to localhost never reaches
// this listener and the sign-in hangs.
func (c *callbackServer) RedirectURL() string {
	port := c.ln.Addr().(*net.TCPAddr).Port
	return "http://127.0.0.1:" + strconv.Itoa(port) + callbackPath
}

func (c *callbackServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	res, err := c.parse(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !c.deliver(res) {
		http.Error(w, "this sign-in has already completed", http.StatusConflict)
		return
	}
	if res.err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "<!doctype html><title>Sign-in failed — Rune</title>"+
			"<p>Sign-in failed: %s</p><p>You can return to Rune.</p>",
			html.EscapeString(res.err.Error()))
		return
	}
	authcallback.WriteSuccess(w, authcallback.Page{
		Label:   "rune",
		Title:   "Signed in — Rune",
		Heading: "Done",
		Message: "You are signed in to the MCP server. You can return to Rune.",
	})
}

// parse validates an authorization response. A response for another
// sign-in is rejected without consuming this one.
func (c *callbackServer) parse(q url.Values) (callbackResult, error) {
	if q.Get("state") != c.state {
		return callbackResult{}, errors.New("the sign-in state does not match; start the sign-in again")
	}
	if e := q.Get("error"); e != "" {
		if desc := q.Get("error_description"); desc != "" {
			e += ": " + desc
		}
		return callbackResult{err: fmt.Errorf("authorization server: %s", e)}, nil
	}
	code := q.Get("code")
	if code == "" {
		return callbackResult{}, errors.New("the redirect carries no authorization code")
	}
	return callbackResult{code: code, state: q.Get("state"), iss: q.Get("iss")}, nil
}

// Deliver completes the sign-in with a redirect URL the user copied
// from the browser's address bar.
func (c *callbackServer) Deliver(redirectURL string) error {
	u, err := url.Parse(redirectURL)
	if err != nil {
		return fmt.Errorf("parse redirect URL: %w", err)
	}
	if u.Path != callbackPath {
		return fmt.Errorf("redirect URL path is %q, want %q", u.Path, callbackPath)
	}
	res, err := c.parse(u.Query())
	if err != nil {
		return err
	}
	if !c.deliver(res) {
		return errors.New("this sign-in has already completed")
	}
	return res.err
}

func (c *callbackServer) deliver(res callbackResult) bool {
	delivered := false
	c.once.Do(func() {
		c.ch <- res
		delivered = true
	})
	return delivered
}

// wait blocks until the authorization response arrives or ctx ends.
func (c *callbackServer) wait(ctx context.Context) (callbackResult, error) {
	select {
	case res := <-c.ch:
		return res, res.err
	case <-ctx.Done():
		return callbackResult{}, ctx.Err()
	}
}

// Close stops listening.
func (c *callbackServer) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return c.srv.Shutdown(ctx)
}
