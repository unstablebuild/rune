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

package command

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"text/template"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/markdown"
)

// Manual represents a command's manual and documentation.
type Manual struct {
	Name string

	// Summary is a short 80-100 character description.
	Summary string

	// Synopsis is a single line synopsis of how
	// this CLI is to be used. It should ONLY include
	// the semantic information about how arguments are parsed.
	//
	// Example: [<options>] [<revision-range>] [[--] <path>...]
	Synopsis string

	// Commands is a list of accepted commands or nil
	// if no commands are expected.
	Commands []Manual

	// AliasOf defines this command as an alias of the
	// given command or sequence of commands.
	AliasOf []string

	// RawArgs makes the prompt dispatch the arguments as typed, quotes
	// and escapes included, so that joining them with spaces gives back
	// the typed line. Commands that parse that line themselves, as a
	// shell does, set it; the others receive the unquoted values.
	RawArgs bool
}

const manualStringTemplate = `USAGE
{{ .Name }} {{ .Synopsis }}

DESCRIPTION{{ with .AliasOf }}{{ $length := len . }} {{if eq $length 1 }}
Alias of {{ index . 0 }}{{else}}
Alias of the following sequence of commands:

{{ range . }}- {{ . }}
{{ end }}{{ end }}{{end}}
{{ .Summary }}{{ with .Commands }}

SUB-COMMANDS{{ range . }}
    - {{ .Name }}{{ end }}
{{ end }}`

const manualMarkdownTemplate = "## Usage\n" +
	"`{{ .Name }} {{ .Synopsis }}`\n" +
	"## Description\n" +
	"{{ with .AliasOf }}{{ $length := len . }} {{if eq $length 1 }}\n" +
	"Alias of {{ index . 0 }}{{else}}\n" +
	"Alias of the following sequence of commands:\n" +
	"\n" +
	"{{ range . }}- {{ . }}\n" +
	"{{ end }}{{ end }}{{end}}\n" +
	"{{ .Summary }}{{ with .Commands }}\n" +
	"## Subcommands {{ range . }}\n" +
	" - {{ .Name }}{{ end }}\n" +
	"{{ end }}\n"

var (
	strTemplate      *template.Template
	markdownTemplate *template.Template
)

func init() {
	strTemplate = template.Must(template.New("command-str").
		Parse(manualStringTemplate))
	markdownTemplate = template.Must(template.New("command-md").
		Parse(manualMarkdownTemplate))
}

func (p *Prompt) makeManualComponent(
	man Manual,
	frameCharSet component.FrameCharSet,
	attr term.Attributes,
) (ret component.Responsive) {
	var builder strings.Builder
	var str string
	var tmpl *template.Template
	if p.config.NoMarkdown {
		tmpl = strTemplate
	} else {
		tmpl = markdownTemplate
	}
	err := writeTemplate(&builder, man, tmpl)
	if err != nil {
		str = fmt.Sprintf("ERROR: build manual: %v", err)
	} else {
		str = builder.String()
	}

	cfg := markdown.DefaultConfig()
	cfg.HeaderPrefix = false
	cfg.ParagraphSpacing = 0
	cfg.InlineCode = term.Attributes{
		Fg: term.ColorSilver,
		Bg: term.ColorGray,
	}
	markdown, err := markdown.NewWithConfig(str, cfg)
	if err != nil || p.config.NoMarkdown {
		// this could be either a programmer error or
		// a third-party command that's injecting incorrect markdown into
		// the command manual.
		if err != nil {
			slog.Error("parse manual markdown", "error", err)
		}
		minWidth := minWidthManualComponent
		if frameCharSet != (component.FrameCharSet{}) {
			minWidth -= 2
		}
		ret = component.NewResponsiveString(str, component.StringResponsiveConfig{
			NoSplitWords: true,
			StringConfig: component.StringConfig{
				Alignment:            component.AlignmentCentered,
				Attributes:           attr,
				BackgroundAttributes: attr,
				PaddingVertical:      2,
				MinWidth:             minWidth,
			},
		})
	} else {
		ret = component.FuncResponsive(markdown, func(width int) int {
			return markdown.Height(width)
		})
	}
	return ret
}

func writeTemplate(w io.Writer, m Manual, tmpl *template.Template) error {
	if err := tmpl.Execute(w, m); err != nil {
		return fmt.Errorf("template execute: %v", err)
	}
	return nil
}

func getSubcommandManual(cmd Manual, input []string) (
	man Manual, ok bool,
) {
	if len(input) == 0 {
		return
	}
	name := input[0]
	for _, subCmd := range cmd.Commands {
		if subCmd.Name == name {
			textSubCmd := subCmd
			subSubCmd, subSubCmdOk := getSubcommandManual(textSubCmd, input[1:])
			if subSubCmdOk {
				return subSubCmd, true
			}
			return textSubCmd, true
		}
	}
	return
}

func manualToName(man Manual) string {
	return man.Name
}
