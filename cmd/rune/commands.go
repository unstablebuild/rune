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
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ernestrc/go-multierror"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/cmd/rune/ide/apiclient"
	"unstable.build/rune/cmd/rune/loginshell"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/term/gui"
	"unstable.build/rune/internal/text"
)

const (
	cmdSetOpacity        = "guiopacity"
	cmdSetBackgroundBlur = "guibackgroundblur"
	cmdSetTheme          = "guitheme"
	cmdPlaySound         = "guisound"
	cmdSetVolume         = "guivolume"
)

var fullscreen bool

func subscribeCommands(
	g *gui.GUI, soundOutput *gui.Sounds, c *apiclient.Client,
	i *ide.IDE, transparentEnabled bool, configPath string,
	launchCmd []string,
) (ret error) {
	if err := subscribeGUICommands(g, soundOutput, i, transparentEnabled, launchCmd); err != nil {
		ret = multierror.Append(ret, err)
	}
	if err := subscribeOtherCommands(i, c, configPath); err != nil {
		ret = multierror.Append(ret, err)
	}
	return ret
}

func subscribeOtherCommands(
	i *ide.IDE, c *apiclient.Client, configPath string,
) (ret error) {
	for _, register := range []func(loginshell.Client) (textapi.CommandManual, textapi.REPLHandler){
		loginshell.Login,
		loginshell.Logout,
	} {
		cmd, handler := register(c)
		if err := i.RegisterREPLCommand(cmd, handler); err != nil {
			ret = multierror.Append(ret,
				fmt.Errorf("register repl '%s': %v", cmd.Name, err))
		}
	}
	var commands = []struct {
		cmd           textapi.CommandManual
		handleCommand func(context.Context, textapi.Command) (err error)
		completer     func(context.Context, textapi.Command) (
			iterator.Iterator[string], string, error,
		)
	}{
		{
			cmd: textapi.CommandManual{
				Name:    "config",
				Summary: "Opens the current configuration file for editing.",
			},
			handleCommand: func(ctx context.Context, cmd textapi.Command) error {
				uri, err := workspaceapi.CurrentUserHostURI(configPath)
				if err != nil {
					err = fmt.Errorf("parse config uri: %w", err)
					return err
				}
				return i.Open(uri)
			},
		},
	}
	for _, man := range commands {
		handler := text.FuncCommandHandler(man.handleCommand, man.completer)
		err := i.SubscribeCommand(man.cmd, handler)
		if err != nil {
			ret = multierror.Append(ret, fmt.Errorf("subscribe '%s': %v", man.cmd.Name, err))
		}
	}
	return
}

func subscribeGUICommands(
	g *gui.GUI, soundOutput *gui.Sounds, i *ide.IDE, transparentEnabled bool,
	launchCmd []string,
) (ret error) {
	var commands = []struct {
		cmd           textapi.CommandManual
		handleCommand func(context.Context, textapi.Command) (err error)
		completer     func(context.Context, textapi.Command) (
			iterator.Iterator[string], string, error,
		)
	}{
		{
			cmd: textapi.CommandManual{
				Name: "screenshot",
				Summary: fmt.Sprintf("Takes a screenshot and saves it to %s.",
					filepath.Join(*flagDataPath, "screenshots")),
			},
			handleCommand: func(_ context.Context, cmd textapi.Command) error {
				if len(cmd.Args) != 0 {
					return errors.New("screenshot expects no arguments")
				}
				path, err := g.Screenshot()
				if err != nil {
					return err
				}
				_, err = i.Notifications().Notify(browserapi.LevelInfo, "Screenshot saved to %s", path)
				return err
			},
		},
		{
			cmd: textapi.CommandManual{
				Name:    "guiminimize",
				Summary: "Minimizes the OS window",
			},
			handleCommand: func(ctx context.Context, cmd textapi.Command) (err error) {
				g.MinimizeWindow()
				return
			},
		},
		{
			cmd: textapi.CommandManual{
				Name: cmdSetOpacity,
				Summary: "Sets the background and optionally foreground opacity. " +
					"Expects at least one float between 0 and 1. It requires " +
					"gui.enable_transparent_window to be set to true in configuration. " +
					"To make this changes permanent, you can set `gui.window_opacity` " +
					"in your configuration.",
				Synopsis: "<background> [<foreground>]",
			},
			handleCommand: func(ctx context.Context, cmd textapi.Command) (err error) {
				if len(cmd.Args) == 0 {
					err = errors.New("expected at least one float between " +
						"0 and 1 with the background opacity")
					return
				}
				background, err := strconv.ParseFloat(cmd.Args[0], 64)
				if err != nil {
					return fmt.Errorf("parse background opacity float: %w", err)
				}
				var foreground float64 = 1
				if len(cmd.Args) > 1 {
					foreground, err = strconv.ParseFloat(cmd.Args[1], 64)
					if err != nil {
						return fmt.Errorf("parse foreground opacity float: %w", err)
					}
				}
				if background < 0 || background > 1 || foreground < 0 || foreground > 1 {
					return errors.New("expected opacity values to be between 0 and 1")
				}
				g.SetOpacity(background, foreground)
				return
			},
		},
		{
			cmd: textapi.CommandManual{
				Name: cmdSetBackgroundBlur,
				Summary: "Sets the window background blur. " +
					"Expects one argument with the blur radius. " +
					"gui.enable_transparent_window to be set to true in configuration, " +
					"and background opacity must be less than 1 for the effect to be visible. " +
					"To make this changes permanent, you can set `gui.window_blur_radius` " +
					"in your configuration.",
				Synopsis: "<radius>",
			},
			handleCommand: func(ctx context.Context, cmd textapi.Command) (err error) {
				if len(cmd.Args) == 0 {
					err = errors.New("expected one argument with the blur radius")
					return
				}
				radius, err := strconv.Atoi(cmd.Args[0])
				if err != nil {
					return fmt.Errorf("parse blur radius integer: %w", err)
				}
				if radius < 0 {
					return errors.New("expected positive blur radius value")
				}
				if runtime.GOOS != "darwin" {
					return errors.New("this feature is only supported on MacOS")
				}
				g.SetBackgroundBlur(radius)
				return
			},
		},
		{
			cmd: textapi.CommandManual{
				Name:    "guimaximize",
				Summary: "Maximizes the OS window",
			},
			handleCommand: func(ctx context.Context, cmd textapi.Command) (err error) {
				g.MaximizeWindow()
				return
			},
		},
		{
			cmd: textapi.CommandManual{
				Name: "guifullscreen",
				Summary: "Toggles the current mode to fullscreen or not. " +
					"When on, the window is automatically enlarged " +
					"to fit the available space. " +
					"This command does nothing on macOS when the window is set to full screen" +
					"natively by the OS, rather than via this command. ",
			},
			handleCommand: func(ctx context.Context, cmd textapi.Command) (err error) {
				fullscreen = !fullscreen
				g.SetFullscreen(fullscreen)
				return
			},
		},
		{
			cmd: textapi.CommandManual{
				Name:     "guiposition",
				Synopsis: "<xoffset> <yoffset>",
				Summary: "Sets the window position as an offset from " +
					"the upper-left corner of the current monitor, " +
					"in device-independent pixels. " +
					"If the application is running on full screen mode set via guiToggleFullscren, " +
					"it will set the original window size. ",
			},
			handleCommand: func(ctx context.Context, cmd textapi.Command) (err error) {
				if len(cmd.Args) != 2 {
					return errors.New("expected two integer arguments with the x " +
						"offset and y offset in pixels")
				}
				x, err := strconv.Atoi(cmd.Args[0])
				if err != nil {
					return fmt.Errorf("parse x offset integer: %w", err)
				}
				y, err := strconv.Atoi(cmd.Args[1])
				if err != nil {
					return fmt.Errorf("parse y offset integer: %w", err)
				}
				g.SetWindowPosition(x, y)
				return
			},
		},
		{
			cmd: textapi.CommandManual{
				Name:     "guisize",
				Synopsis: "<width> <height>",
				Summary: "Sets the window size in pixels. " +
					"If the application is running in fullscreen mode, set via guiToggleFullscreen, " +
					"it will set the original window size. ",
			},
			handleCommand: func(ctx context.Context, cmd textapi.Command) (err error) {
				if len(cmd.Args) != 2 {
					return errors.New("expected two integer arguments with the width " +
						"and height in pixels")
				}
				width, err := strconv.Atoi(cmd.Args[0])
				if err != nil {
					return fmt.Errorf("parse width integer: %w", err)
				}
				height, err := strconv.Atoi(cmd.Args[1])
				if err != nil {
					return fmt.Errorf("parse height integer: %w", err)
				}

				if width < 0 || height < 0 {
					return errors.New("width and height must be a positive integer")
				}
				g.SetWindowSize(width, height)
				return
			},
		},
		{
			cmd: textapi.CommandManual{
				Name:     "guifont",
				Synopsis: "[<family>]",
				Summary: "Sets the font collection identified by the given family name. " +
					"If family is set to an empty string, the default system font is used. " +
					"If the family is set to 'builtin', the GUI's builtin fallback font is used. ",
			},
			handleCommand: func(ctx context.Context, cmd textapi.Command) (err error) {
				err = g.SetFont(strings.Join(cmd.Args, " "))
				return
			},
			completer: func(ctx context.Context, cmd textapi.Command) (
				iterator.Iterator[string], string, error,
			) {
				it, err := g.AvailableFontFamilies()
				if err != nil {
					return nil, "", err
				}
				return it, "", nil
			},
		},
		{
			cmd: textapi.CommandManual{
				Name:     cmdSetTheme,
				Synopsis: "[<name>]",
				Summary: "Sets the color theme. If the name argument is omitted, " +
					"the theme is reset. If a name is passed, this name must be " +
					"one of the themes configured via `gui.themes`.",
			},
			handleCommand: func(ctx context.Context, cmd textapi.Command) (err error) {
				if len(cmd.Args) == 0 {
					cmd.Args = append(cmd.Args, "") // reset theme
				}
				theme, err := g.SetTheme(cmd.Args[0])
				if err == nil {
					i.SetDefaultAttributes(term.Attributes{
						Fg: term.FromTcellColor(theme.Foreground),
						Bg: term.FromTcellColor(theme.Background),
					})
				}
				return
			},
			completer: func(ctx context.Context, cmd textapi.Command) (
				iterator.Iterator[string], string, error,
			) {
				themes := g.Themes()
				sort.Strings(themes)
				return iterator.FromSlice(themes), "", nil
			},
		},
		{
			cmd: textapi.CommandManual{
				Name:     cmdPlaySound,
				Synopsis: "<name>",
				Summary: "Plays the named sound: one of the built-in sounds " +
					"(error, warning, info, success) or a sound defined under " +
					"`sounds` in your configuration.",
			},
			handleCommand: func(ctx context.Context, cmd textapi.Command) error {
				if len(cmd.Args) != 1 {
					return errors.New("expected exactly one argument with the sound name")
				}
				if !slices.Contains(i.SoundNames(), cmd.Args[0]) {
					return fmt.Errorf("unknown sound '%s'", cmd.Args[0])
				}
				i.Sounds().Play(cmd.Args[0])
				return nil
			},
			completer: func(ctx context.Context, cmd textapi.Command) (
				iterator.Iterator[string], string, error,
			) {
				return iterator.FromSlice(i.SoundNames()), "", nil
			},
		},
		{
			cmd: textapi.CommandManual{
				Name:     cmdSetVolume,
				Synopsis: "increase|decrease|mute",
				Summary: "Raises or lowers the sound volume by 10%, which also unmutes " +
					"sounds, or toggles mute. To make the volume permanent, set " +
					"`gui.sound_volume` in your configuration.",
			},
			handleCommand: func(ctx context.Context, cmd textapi.Command) error {
				if len(cmd.Args) != 1 {
					return errors.New("expected exactly one argument with " +
						"'increase', 'decrease' or 'mute'")
				}
				switch cmd.Args[0] {
				case "increase":
					soundOutput.IncreaseVolume()
				case "decrease":
					soundOutput.DecreaseVolume()
				case "mute":
					soundOutput.SetMuted(!soundOutput.Muted())
				default:
					return errors.New("expected exactly one argument with " +
						"'increase', 'decrease' or 'mute'")
				}
				return nil
			},
			completer: func(ctx context.Context, cmd textapi.Command) (
				iterator.Iterator[string], string, error,
			) {
				return iterator.FromSlice([]string{"increase", "decrease", "mute"}), "", nil
			},
		},
		{
			cmd: textapi.CommandManual{
				Name: "guifontsize",
				Summary: "Increase or decrease the size of the rendered font." +
					"To make changes permanent, update the 'gui.font_size' configuration.",
			},
			handleCommand: func(ctx context.Context, cmd textapi.Command) (err error) {
				if len(cmd.Args) != 1 {
					return errors.New("expected exactly one argument " +
						"with 'increase' or 'decrease'")
				}
				switch cmd.Args[0] {
				case "increase":
					err = g.IncreaseFontSize()
				case "decrease":
					err = g.DecreaseFontSize()
				default:
					return errors.New("expected exactly one argument " +
						"with 'increase' or 'decrease'")
				}
				return
			},
			completer: func(ctx context.Context, cmd textapi.Command) (
				iterator.Iterator[string], string, error,
			) {
				return iterator.FromSlice([]string{"increase", "decrease"}), "", nil
			},
		},
		{
			cmd: textapi.CommandManual{
				Name: "guicellwidth",
				Summary: "Increases or decreases the cell width of the rendered font. " +
					"To make changes permanent, update the 'gui.column_width_offset' configuration.",
				Synopsis: "(increase|decrease)",
			},
			handleCommand: func(ctx context.Context, cmd textapi.Command) (err error) {
				if len(cmd.Args) != 1 {
					return errors.New("expected exactly one argument " +
						"with 'increase' or 'decrease'")
				}
				switch cmd.Args[0] {
				case "increase":
					err = g.IncreaseCellWidth()
				case "decrease":
					err = g.DecreaseCellWidth()
				default:
					return errors.New("expected exactly one argument " +
						"with 'increase' or 'decrease'")
				}
				return
			},
			completer: func(ctx context.Context, cmd textapi.Command) (
				iterator.Iterator[string], string, error,
			) {
				return iterator.FromSlice([]string{"increase", "decrease"}), "", nil
			},
		},
		{
			cmd: textapi.CommandManual{
				Name: "guilineheight",
				Summary: "Increases or decreases the line height of the rendered font. " +
					"To make changes permanent, update the 'gui.line_height_offset' configuration.",
				Synopsis: "(increase|decrease)",
			},
			handleCommand: func(ctx context.Context, cmd textapi.Command) (err error) {
				if len(cmd.Args) != 1 {
					return errors.New("expected exactly one argument " +
						"with 'increase' or 'decrease'")
				}
				switch cmd.Args[0] {
				case "increase":
					err = g.IncreaseLineHeight()
				case "decrease":
					err = g.DecreaseLineHeight()
				default:
					return errors.New("expected exactly one argument " +
						"with 'increase' or 'decrease'")
				}
				return
			},
			completer: func(ctx context.Context, cmd textapi.Command) (
				iterator.Iterator[string], string, error,
			) {
				return iterator.FromSlice([]string{"increase", "decrease"}), "", nil
			},
		},
		{
			cmd: textapi.CommandManual{
				Name:    "guiwindownew",
				Summary: "Opens a new OS-level window by spawning a new Rune process.",
			},
			handleCommand: func(ctx context.Context, cmd textapi.Command) (err error) {
				if len(launchCmd) == 0 {
					return errors.New("unable to spawn new window: launch command not captured")
				}
				c := exec.Command(launchCmd[0], launchCmd[1:]...)
				c.Env = append(os.Environ(), "EBITENGINE_COCOA_HIDE_DOCK=1")
				if err := c.Start(); err != nil {
					return fmt.Errorf("spawn new window: %w", err)
				}
				go debug.CapturePanicReport(func() {
					_ = c.Wait()
				})
				return nil
			},
		},
	}

	for _, man := range commands {
		man := man
		err := i.SubscribeCommand(man.cmd,
			text.FuncCommandHandler(func(ctx context.Context, cmd textapi.Command) error {
				switch cmd.Name {
				case cmdSetOpacity, cmdSetBackgroundBlur:
					if !transparentEnabled {
						return errors.New("cannot change opacity if " +
							"transparent window is not enabled. Enable it by setting" +
							"`gui.enable_transparent_window` to `true` in your configuration")
					}
					fallthrough
				default:
					return man.handleCommand(ctx, cmd)
				}
			}, man.completer))
		if err != nil {
			ret = multierror.Append(ret, fmt.Errorf("subscribe '%s': %v", man.cmd.Name, err))
		}
	}
	return ret
}
