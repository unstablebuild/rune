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

// Command crosshostd serves git repositories over HTTPS inside the crosshost
// e2e container, so the `rune -x` under test installs git packages from a
// URL it resolves like any other. git http-backend does the git side; this
// only terminates TLS and runs it as CGI.
//
// It prints readyLine once it accepts connections, which the suite waits for
// instead of polling.
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/cgi"
	"os"
	"time"
)

const readyLine = "crosshostd: ready"

func main() {
	addr := flag.String("addr", ":443", "address to listen on")
	root := flag.String("root", "/srv/git", "directory holding the repositories")
	cert := flag.String("cert", "", "PEM certificate chain")
	key := flag.String("key", "", "PEM private key")
	backend := flag.String("backend", "/usr/lib/git-core/git-http-backend",
		"path of git http-backend")
	flag.Parse()

	pair, err := tls.LoadX509KeyPair(*cert, *key)
	if err != nil {
		log.Fatalf("load certificate: %v", err)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen on %s: %v", *addr, err)
	}
	handler := &cgi.Handler{
		Path: *backend,
		Env: []string{
			"GIT_PROJECT_ROOT=" + *root,
			"GIT_HTTP_EXPORT_ALL=1",
		},
		Stderr: os.Stderr,
	}
	fmt.Println(readyLine)
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	err = srv.Serve(tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{pair}}))
	log.Fatalf("serve: %v", err)
}
