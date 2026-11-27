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
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

func newScreenshotCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "screenshot",
		Short: "Capture a screenshot of the local Rune instance",
		Long: `Capture Rune's rendered GUI as a PNG and print its absolute path.

The image is saved in $RUNE_DATADIR/screenshots. The command waits up to five
seconds for it. Screenshots require Rune's GUI (not --tui) and must be taken on
the machine running Rune, not from a terminal in a remote workspace.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pidStr, ok := os.LookupEnv("RUNE_PID")
			if !ok {
				return errors.New("RUNE_PID is not set: run runectl screenshot " +
					"from a terminal and make sure you have the lastest " +
					"version of Rune installed")
			}
			// Rune clears RUNE_PID for commands it starts on a remote host.
			if pidStr == "" {
				return errors.New("runectl screenshot must run on the machine " +
					"running Rune; commands run on a terminal in " +
					"remote workspaces run on the remote host")
			}
			pid, err := strconv.ParseInt(pidStr, 10, 32)
			if err != nil || pid <= 0 {
				return fmt.Errorf("invalid RUNE_PID %q", pidStr)
			}
			datadir := os.Getenv("RUNE_DATADIR")
			if datadir == "" {
				return fmt.Errorf("RUNE_DATADIR is required")
			}
			dir, err := filepath.Abs(filepath.Join(datadir, "screenshots"))
			if err != nil {
				return fmt.Errorf("resolve screenshot directory: %w", err)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			entries, err := readScreenshots(dir)
			if err != nil {
				return err
			}
			baseline := make(map[string]bool, len(entries))
			for _, entry := range entries {
				baseline[entry.Name()] = true
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := syscall.Kill(int(pid), syscall.SIGUSR2); err != nil {
				return fmt.Errorf("signal Rune process %d: %w", pid, err)
			}
			path, err := waitScreenshot(ctx, dir, baseline)
			if errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("%w; screenshots require Rune's GUI (not --tui), "+
					"check Rune's log for errors", err)
			}
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), path)
			return err
		},
	}
}

func readScreenshots(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read screenshot directory: %w", err)
	}
	return entries, nil
}

func waitScreenshot(ctx context.Context, dir string, baseline map[string]bool) (string, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("wait for Rune screenshot: %w", err)
		}
		entries, err := readScreenshots(dir)
		if err != nil {
			return "", err
		}
		for _, entry := range entries {
			name := entry.Name()
			// Rune writes to a hidden temporary file and renames it, so a
			// visible screenshot is complete.
			if !baseline[name] && entry.Type().IsRegular() &&
				strings.HasPrefix(name, "screenshot-") && strings.HasSuffix(name, ".png") {
				return filepath.Join(dir, name), nil
			}
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("wait for Rune screenshot: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
