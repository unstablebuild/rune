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
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"unstable.build/rune/internal/smartware"
)

func init() {
	exCommands["smartwareoverlay"] = commandAll{
		man: textapi.CommandManual{
			Summary: "Draft a Smartware overlay run against an installed jacket. " +
				"Does not execute the jacket. Requires SMARTWARE_CORE to point at smartware-core.",
			Synopsis: "<jacket-id> <job...>",
		},
		handler: (*ex).smartwareOverlay,
	}
}

func (e *ex) smartwareOverlay(ctx context.Context, args ...string) error {
	if len(args) < 2 {
		return errors.New("usage: smartwareoverlay <jacket-id> <job...>")
	}
	core := strings.TrimSpace(os.Getenv("SMARTWARE_CORE"))
	if core == "" {
		return errors.New("SMARTWARE_CORE is not set")
	}
	built, err := smartware.BuildOverlayCmd(smartware.OverlayArgs{
		CoreDir: core,
		Jacket:  args[0],
		Job:     strings.Join(args[1:], " "),
	})
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, built.Name, built.Args...)
	cmd.Dir = built.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	out := strings.TrimSpace(stdout.String() + "\n" + stderr.String())
	if runErr != nil {
		if out != "" {
			return errors.New(out)
		}
		return runErr
	}
	_ = e.sendNotificationInfo(ctx, out)
	return nil
}
