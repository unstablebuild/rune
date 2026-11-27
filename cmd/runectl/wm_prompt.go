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
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi/browserrpc"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/termrpc"
)

var (
	errPromptDismissed = errors.New("prompt dismissed")
	errPromptOpen      = errors.New("a prompt with this message is already open")
)

// promptAnswer is the option the user picked, and the answers to the
// fields in their order.
type promptAnswer struct {
	Index   int
	Option  string
	Answers []string `json:",omitempty"`
}

func newWMPromptCmd(a *app) *cobra.Command {
	var format string
	var keys string
	var maxWidth int
	var fields promptFields

	cmd := &cobra.Command{
		Use:   "prompt <message> <option>...",
		Short: "Ask the user to pick an option",
		Long: "Ask the user to pick an option in a prompt window, and print the one they pick.\n" +
			"The message is markdown. The command waits for the answer, and fails if the\n" +
			"user dismisses the prompt or one with the same message is already open.\n" +
			"Stopping the command closes the prompt.\n\n" +
			"--field and --secret ask questions the user answers by typing, in the order\n" +
			"given. --mandatory keeps the options from being picked while the answer to\n" +
			"the question before it is blank, and --placeholder hints at that answer while\n" +
			"it is empty. The answers are printed a line each after the option, or as\n" +
			"Answers with -F json. Secret answers are hidden as typed, but are printed to\n" +
			"stdout like the others.",
		Example: "  runectl wm prompt 'Log in to *db*' OK Cancel --field User=me --secret Password --mandatory",
		Args:    cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			cfg := browserapi.PromptConfig{
				Message:  args[0],
				Options:  args[1:],
				Fields:   fields,
				MaxWidth: maxWidth,
			}
			if keys != "" {
				bindings, err := term.ParseKeys(keys)
				if err != nil {
					return fmt.Errorf("invalid --keys %q: %w", keys, err)
				}
				cfg.Bindings = bindings
			}
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			wm := browserrpc.NewWindowManagerClient(w.RawConn())
			answer, err := prompt(cmd.Context(), wm, cfg)
			if err != nil {
				return err
			}
			columns := []string{"Index", "Option"}
			if len(fields) > 0 {
				columns = append(columns, "Answers")
			}
			return printResult(
				cmd.Context(), format, answer,
				func(v promptAnswer) {
					fmt.Println(v.Option)
					for _, a := range v.Answers {
						fmt.Println(a)
					}
				},
				columns,
			)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)
	cmd.Flags().StringVarP(
		&keys, "keys", "k", "",
		"Keys that pick each option in order, such as yn or <enter><esc>",
	)
	cmd.Flags().IntVarP(
		&maxWidth, "max-width", "w", 0,
		"Size the message to its widest line, wrapping at this many cells",
	)
	cmd.Flags().Var(
		promptFieldFlag{fields: &fields},
		"field", "Ask `QUESTION[=ANSWER]`, the answer being the text the user starts from",
	)
	cmd.Flags().Var(
		promptFieldFlag{fields: &fields, secret: true},
		"secret", "Ask `QUESTION[=ANSWER]` with the answer hidden as typed",
	)
	cmd.Flags().VarPF(
		promptMandatoryFlag{fields: &fields}, "mandatory", "",
		"Keep the options from being picked while the answer to the field before is blank",
	).NoOptDefVal = "true"
	cmd.Flags().Var(
		promptPlaceholderFlag{fields: &fields}, "placeholder",
		"Hint at the answer to the field before with `TEXT` while it is empty",
	)

	return cmd
}

// promptFields are the fields of --field, --secret, --mandatory and
// --placeholder in the order given.
type promptFields []browserapi.Field

// promptFieldFlag adds a field per --field or --secret.
type promptFieldFlag struct {
	fields *promptFields
	secret bool
}

func (f promptFieldFlag) Set(v string) error {
	question, answer, _ := strings.Cut(v, "=")
	if question == "" {
		return errors.New("the question is empty")
	}
	*f.fields = append(*f.fields, browserapi.Field{
		Question: question, Answer: answer, Secret: f.secret,
	})
	return nil
}

func (f promptFieldFlag) String() string { return "" }

func (f promptFieldFlag) Type() string { return "string" }

// promptMandatoryFlag makes the field given last mandatory.
type promptMandatoryFlag struct {
	fields *promptFields
}

func (f promptMandatoryFlag) Set(v string) error {
	mandatory, err := strconv.ParseBool(v)
	if err != nil {
		return err
	}
	if len(*f.fields) == 0 {
		return errors.New("--mandatory must follow a --field or --secret")
	}
	(*f.fields)[len(*f.fields)-1].Mandatory = mandatory
	return nil
}

func (f promptMandatoryFlag) String() string { return "false" }

func (f promptMandatoryFlag) Type() string { return "bool" }

// promptPlaceholderFlag sets the placeholder of the field given last.
type promptPlaceholderFlag struct {
	fields *promptFields
}

func (f promptPlaceholderFlag) Set(v string) error {
	if len(*f.fields) == 0 {
		return errors.New("--placeholder must follow a --field or --secret")
	}
	(*f.fields)[len(*f.fields)-1].Placeholder = v
	return nil
}

func (f promptPlaceholderFlag) String() string { return "" }

func (f promptPlaceholderFlag) Type() string { return "string" }

// prompt opens cfg and waits for the user to answer it. The SDK's
// Prompter can't tell a prompt it opened from one already open for the
// same message, so it drives the stream itself. A host that predates
// fields opens a prompt with them without them, so prompt closes it at
// once and fails with an error that wraps errors.ErrUnsupported.
func prompt(
	ctx context.Context, wm browserrpc.WindowManagerClient, cfg browserapi.PromptConfig,
) (promptAnswer, error) {
	if err := cfg.Validate(); err != nil {
		return promptAnswer{}, err
	}
	req := browserrpc.PromptRequest{
		Message:  cfg.Message,
		Options:  cfg.Options,
		MaxWidth: uint32(cfg.MaxWidth),
	}
	for _, k := range cfg.Bindings {
		ev := new(termrpc.Event)
		if err := ev.FromModel(term.Event{
			Type: term.EventKey, Mod: k.Mod, Key: k.Key, Ch: k.Ch,
		}); err != nil {
			return promptAnswer{}, fmt.Errorf("prompt binding %s: %w", k, err)
		}
		req.Bindings = append(req.Bindings, ev)
	}
	for _, f := range cfg.Fields {
		req.Fields = append(req.Fields, &browserrpc.PromptField{
			Question:    f.Question,
			Answer:      f.Answer,
			Secret:      f.Secret,
			Mandatory:   f.Mandatory,
			Placeholder: f.Placeholder,
		})
	}

	// Ending the stream before the prompt closes closes it.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := wm.Prompt(ctx, &req)
	if err != nil {
		return promptAnswer{}, fmt.Errorf("prompt: %w", err)
	}
	var answer *promptAnswer
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return promptAnswer{}, errPromptOpen
		}
		if err != nil {
			return promptAnswer{}, fmt.Errorf("prompt: %w", err)
		}
		if opened := ev.GetOpened(); opened != nil && len(req.Fields) > 0 && !opened.GetSupportsFields() {
			return promptAnswer{}, fmt.Errorf(
				"prompt fields: %w; update Rune to ask with fields", errors.ErrUnsupported)
		}
		if sel := ev.GetSelected(); sel != nil {
			answer = &promptAnswer{
				Index: int(sel.GetIndex()), Option: sel.GetOption(), Answers: sel.GetAnswers(),
			}
		}
		if ev.GetClosed() != nil {
			if answer == nil {
				return promptAnswer{}, errPromptDismissed
			}
			return *answer, nil
		}
	}
}
