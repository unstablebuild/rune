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

package plugin

import (
	"errors"
	"fmt"
	"strings"
	"text/template/parse"

	"unstable.build/rune/internal/component/template"
)

// ParseBarLayout parses the given layout string into a set of
// BarComponent. The expected format is Go templates.
func ParseBarLayout(layoutStr string) (
	ret []BarComponent, err error,
) {
	ret = nil
	tree, err := parse.Parse("status_bar.layout",
		layoutStr, "{{", "}}", template.AllowedFuncs())
	if err != nil {
		return
	}

	root := tree["status_bar.layout"].Root
	if root == nil {
		err = errors.New("parse template error: nil root")
		return
	}

	var tmpl string
	var alignRight, alignCenter bool
	for _, node := range root.Nodes {
		switch n := node.(type) {
		case *parse.TextNode:
			// suffix attributes
			if len(n.Text) != 0 && len(ret) > 0 {
				all := strings.Split(string(n.Text), "  ")
				if alignRight || alignCenter {
					ret[len(ret)-1].Template += all[0]
					if len(all) > 1 {
						ret[len(ret)-1].Template += "  "
					}
					for i, chunk := range all[1:] {
						tmpl += chunk
						if i < len(all[1:])-1 {
							tmpl += "  "
						}
					}
				} else {
					ret[len(ret)-1].Template += all[0]
					for _, chunk := range all[1:] {
						tmpl += "  "
						tmpl += chunk
					}
				}
			} else {
				tmpl += string(n.Text)
			}

		case *parse.ActionNode:
			act, err := template.ParseAction(n)
			if err != nil {
				return nil, err
			}

			var compType BarComponentType
			switch act.Field {
			case "Command":
				compType = BarCommand
				tmpl += "%s"
			case "StatusIcon":
				compType = BarStatusIcon
				tmpl += "%s"
			case "ExitStatus":
				compType = BarExitStatus
				tmpl += "%s"
			case "Elapsed":
				compType = BarElapsed
				tmpl += "%s"
			case "AlignCenter":
				alignRight = false
				alignCenter = true
				ret = append(ret, BarComponent{
					Type: BarAlignCenter,
				})
				continue
			case "AlignRight":
				alignCenter = false
				alignRight = true
				ret = append(ret, BarComponent{
					Type: BarAlignRight,
				})
				continue
			default:
				err = fmt.Errorf("unknown status bar component: %q", act.Field)
				return nil, err
			}
			ret = append(ret, BarComponent{
				Type:       compType,
				Template:   tmpl,
				Attributes: act.Attributes,
			})
			tmpl = ""

		default:
			err = fmt.Errorf("unsupported node type %T at position %d", n, node.Position())
			return nil, err
		}
	}

	// Trailing text after the last component - append to last component's template
	// This handles cases like "{{ .Language }}  " where there's padding at the end
	if tmpl != "" && len(ret) > 0 {
		ret[len(ret)-1].Template += tmpl
	}

	return
}
