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

package agentools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"io/fs"
	"maps"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"gopkg.in/yaml.v3"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/utf8validate"
	"unstable.build/rune/internal/debug"
)

const (
	runeGUIReplyTimeout = 2 * time.Minute
	runeGUIStopTimeout  = 10 * time.Second
	// runeGUIDefaultSize keeps screenshots, at Rune's default scale of 2,
	// under maxImageEdge, so they reach the model unscaled.
	runeGUIDefaultSize     = "960x600"
	runeGUIDefaultLogLines = 40
	runeGUIMaxLogLines     = 500
	// runeGUIExitLogLines is how much of the log an unexpected exit shows.
	runeGUIExitLogLines = 20
	// runeGUILogCap bounds how much of a log is read or kept.
	runeGUILogCap = 256 << 10
	// runeGUIMaxReply bounds a reply line, as Rune bounds a command line.
	runeGUIMaxReply = 1 << 20
)

// The files of an instance's data directory: Rune reads config.yaml,
// which layers the agent's config over the tool's defaults, and logs to
// debug.log.
const (
	runeGUIConfigFile   = "config.yaml"
	runeGUIDefaultsFile = "defaults.yaml"
	runeGUIAgentFile    = "agent.yaml"
	runeGUILogFile      = "debug.log"
	runeGUIConfig       = "include:\n  - " + runeGUIDefaultsFile + "\n  - " + runeGUIAgentFile + "\n"
)

// runeGUIDefaults is the default config of an instance logging to logPath:
// it turns off the animations that make a screenshot depend on when it is
// taken, and logs what log shows, as Rune logs nothing by default.
func runeGUIDefaults(logPath string) string {
	return "animations:\n  intro: false\n  loading_workspace: false\n  open_workspace: false\n" +
		"log_level: info\nlog_path: " + strconv.Quote(logPath) + "\n"
}

// runeGUISharedDirs are the directories of the user's data directory an
// instance shares, so the HUD assets and staged configs that the user's
// config names under $RUNE_DATADIR resolve in the instance too.
var runeGUISharedDirs = []string{"hud", "etc"}

var runeGUICommands = []string{"start", "config", "exec", "keys", "screenshot", "log", "stop"}

var runeGUISizeRe = regexp.MustCompile(`^[1-9][0-9]*x[1-9][0-9]*(@[0-9]+(\.[0-9]+)?)?$`)

// RuneGUI is the rune_gui tool: it runs Rune without a window, with
// --offscreen and --exec, and drives it over its stdin and stdout. It runs
// on this machine, where the extension and the user's config are, whatever
// the agent's workspace. Every agent that calls it gets its own instance
// and data directory, keyed by the agent's dialogue ID. An instance cannot
// reach the user's Rune.
type RuneGUI struct {
	// local starts the user's Rune. workspaceExec starts a binary the
	// agent names, which the user then approves; it is nil unless the
	// workspace is on this machine.
	local, workspaceExec workspaceapi.Executor

	bin         string
	dir         string
	userDataDir string
	workspace   string
	dialogueID  string
	// replyTimeout bounds the wait for a reply, and stopTimeout the wait
	// for an instance to exit before it is killed.
	replyTimeout, stopTimeout time.Duration

	mu       sync.Mutex
	sessions map[string]*runeGUISession
}

var _ agent.Tool = (*RuneGUI)(nil)

// NewRuneGUI returns a rune_gui tool that runs the Rune executable at bin
// on this machine. dir holds the instances' data directories, and
// userDataDir is the data directory of the user's Rune, whose hud and etc
// directories every instance shares. workspaceExec and workspace are the
// agent's workspace executor and path when the workspace is on this
// machine, and nil and "" otherwise: a binary that start names runs
// through workspaceExec, for the user to approve, and relative paths given
// to start resolve from workspace, so neither works without them.
// dialogueID names the instance of calls whose context carries no dialogue
// ID.
func NewRuneGUI(
	workspaceExec workspaceapi.Executor, bin, dir, userDataDir, workspace, dialogueID string,
) *RuneGUI {
	return &RuneGUI{
		local:         localExecutor{},
		workspaceExec: workspaceExec,
		bin:           bin,
		dir:           dir,
		userDataDir:   userDataDir,
		workspace:     workspace,
		dialogueID:    dialogueID,
		replyTimeout:  runeGUIReplyTimeout,
		stopTimeout:   runeGUIStopTimeout,
		sessions:      make(map[string]*runeGUISession),
	}
}

// LookupRune returns the executable of the Rune that runs this extension,
// which Rune reports in RUNE_EXECUTABLE, or an error when it does not. A
// rune on PATH is not used: it can be another version, without the flags
// the tool needs.
func LookupRune() (string, error) {
	bin := os.Getenv("RUNE_EXECUTABLE")
	if bin == "" {
		return "", errors.New("RUNE_EXECUTABLE is not set: this extension was not started by Rune")
	}
	return bin, nil
}

type runeGUIArgs struct {
	Command []string `json:"command"`
}

// Definition satisfies agent.Tool.
func (t *RuneGUI) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name: "rune_gui",
			Description: `Run and drive a separate Rune instance that has no window, to check
changes to Rune or to its config as a user would see them: run
commands, send keys and take screenshots.

The instance is private to you: it does not touch the user's Rune, its
windows or its clipboard. It runs on the user's machine, whatever your
workspace, with its own data directory, and shares only the user's
$RUNE_DATADIR/hud and $RUNE_DATADIR/etc, so HUD assets and staged
configs resolve as they do for the user. It opens an empty scratch
workspace unless start names one.

Pass one command per call as an array of arguments:
["start"], ["exec", "edit main.go"], ["keys", "ix<esc>"], ["screenshot"].

Typical loop: config (optional), start, exec workspaceready, then exec
commands or send keys, and take a screenshot to see the result.

Commands:
  start [--size WxH[@SCALE]] [--binary PATH] [--workspace PATH] [FILE...]
                      Start Rune. --size defaults to 960x600 at scale 2,
                      which keeps screenshots under 2000 pixels. --binary
                      runs another build, e.g. ./bin/rune after make
                      rune: the user approves it, and it needs your
                      workspace to be on this machine. --workspace opens
                      a project. Relative paths resolve from your
                      workspace, which must then be on this machine.
  config <yaml>       Replace the instance's config, which is layered over
                      defaults that turn animations off. The next
                      screenshot reloads it; keys that Rune does not
                      reload live need stop and start.
  exec <command line> Run a Rune command as typed in the command prompt,
                      e.g. "edit main.go" or "windownew right", and wait
                      for it to finish.
  keys <sequence>     Send keys as the echo command does: ix<esc>, <c-w>l,
                      <space>, <enter>. Waits until they are handled.
  screenshot          Reload the HUD and return a screenshot.
  log [N]             The last N lines (default 40) Rune logged.
  stop                Stop Rune. Its config is kept for the next start.

exec and keys answer ok or the error Rune reported. Run exec
workspaceready before other commands: it waits until the workspace has
opened. When --workspace names a project with a .rune config, Rune
first asks whether to trust it and workspaceready waits for the
answer, so take a screenshot and answer with keys first.`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "The rune_gui command and its arguments, one array element per argument.",
					},
				},
				"required":             []string{"command"},
				"additionalProperties": false,
			},
		},
	}
}

// Summary satisfies agent.Tool.
func (t *RuneGUI) Summary(arguments string) string {
	var args runeGUIArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ""
	}
	return strings.Join(args.Command, " ")
}

// NeedsDeterministicOrder satisfies agent.Tool: commands act on the
// instance's state, so their order matters.
func (t *RuneGUI) NeedsDeterministicOrder() bool { return true }

// Execute satisfies agent.Tool.
func (t *RuneGUI) Execute(ctx context.Context, arguments string) agent.ToolResult {
	var args runeGUIArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return webBrowserError(fmt.Sprintf("invalid arguments: %v", err))
	}
	if err := validateRuneGUICommand(args.Command); err != nil {
		return webBrowserError(err.Error())
	}
	s := t.session(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, rest := args.Command[0], args.Command[1:]
	switch sub {
	case "start":
		return t.start(s, rest)
	case "config":
		return t.config(s, rest[0])
	case "log":
		n := runeGUIDefaultLogLines
		if len(rest) == 1 {
			n, _ = strconv.Atoi(rest[0])
		}
		if text := s.logTail(n); text != "" {
			return agent.ToolResult{Content: text}
		}
		return agent.ToolResult{Content: "(no output)"}
	case "stop":
		if s.inst == nil {
			return agent.ToolResult{Content: "Rune is not running."}
		}
		s.inst.stop(t.stopTimeout)
		s.inst = nil
		return agent.ToolResult{Content: "Stopped Rune."}
	}
	if s.inst == nil {
		return webBrowserError(`Rune is not running: run ["start"] first`)
	}
	if s.inst.hasExited() {
		return t.exited(s)
	}
	switch sub {
	case "exec":
		return t.command(ctx, s, strings.Join(rest, " "))
	case "keys":
		return t.command(ctx, s, "echo "+strings.Join(rest, " "))
	default:
		return t.screenshot(ctx, s)
	}
}

// Close stops every instance the tool started and removes their data
// directories. It returns without waiting for them.
func (t *RuneGUI) Close() {
	t.mu.Lock()
	sessions := slices.Collect(maps.Values(t.sessions))
	clear(t.sessions)
	t.mu.Unlock()
	if len(sessions) == 0 {
		return
	}
	go debug.CapturePanicReport(func() {
		for _, s := range sessions {
			s.mu.Lock()
			if s.inst != nil {
				s.inst.stop(t.stopTimeout)
				s.inst = nil
			}
			s.mu.Unlock()
			// RemoveAll removes the links to the shared directories, not
			// what they link to.
			_ = os.RemoveAll(s.dir)
		}
	})
}

// runeGUISession is an agent's data directory and the instance running on
// it. mu serializes the agent's commands, as each reply answers the
// command before it.
type runeGUISession struct {
	dir string

	mu   sync.Mutex
	inst *runeGUIInstance
	// stderr keeps what the latest instance wrote to stderr, also once it
	// exited: what it printed before its log was set up, or when it
	// crashed.
	stderr *runeGUILog
}

// logTail returns the last n lines of the latest instance's log, and of
// its stderr.
func (s *runeGUISession) logTail(n int) string {
	var parts []string
	if text := lastLines(readLogTail(filepath.Join(s.dir, runeGUILogFile)), n); text != "" {
		parts = append(parts, text)
	}
	if text := s.stderr.tail(n); text != "" {
		parts = append(parts, "stderr:\n"+text)
	}
	return strings.Join(parts, "\n")
}

// readLogTail returns up to the last runeGUILogCap bytes of the file at p,
// or nothing when it cannot be read.
func readLogTail(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err == nil && info.Size() > runeGUILogCap {
		if _, err := f.Seek(info.Size()-runeGUILogCap, io.SeekStart); err != nil {
			return ""
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, runeGUILogCap))
	if err != nil {
		return ""
	}
	return string(data)
}

// lastLines returns the last n lines of text, without the line breaks
// that end it.
func lastLines(text string, n int) string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return utf8validate.Sanitize(strings.Join(lines, "\n"))
}

func (t *RuneGUI) session(ctx context.Context) *runeGUISession {
	name := dialogueSessionName(ctx, t.dialogueID)
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.sessions[name]
	if !ok {
		s = &runeGUISession{dir: filepath.Join(t.dir, name), stderr: &runeGUILog{}}
		t.sessions[name] = s
	}
	return s
}

type runeGUIStartOptions struct {
	size, binary, workspace string
	files                   []string
}

func parseRuneGUIStart(args []string) (runeGUIStartOptions, error) {
	opts := runeGUIStartOptions{size: runeGUIDefaultSize}
	for i := 0; i < len(args); i++ {
		name, value, hasValue := strings.Cut(args[i], "=")
		switch name {
		case "--size", "--binary", "--workspace":
			if !hasValue {
				if i+1 == len(args) {
					return opts, fmt.Errorf("%s needs a value", name)
				}
				i++
				value = args[i]
			}
			if value == "" {
				return opts, fmt.Errorf("%s needs a value", name)
			}
		default:
			if strings.HasPrefix(args[i], "-") {
				return opts, fmt.Errorf("start takes only --size, --binary and --workspace, not %q", args[i])
			}
			opts.files = append(opts.files, args[i])
			continue
		}
		switch name {
		case "--size":
			if !runeGUISizeRe.MatchString(value) {
				return opts, fmt.Errorf("--size takes WxH or WxH@SCALE, e.g. 960x600, not %q", value)
			}
			opts.size = value
		case "--binary":
			opts.binary = value
		case "--workspace":
			opts.workspace = value
		}
	}
	return opts, nil
}

func (t *RuneGUI) start(s *runeGUISession, args []string) agent.ToolResult {
	if s.inst != nil {
		if !s.inst.hasExited() {
			return webBrowserError(`Rune is already running: run ["stop"] first`)
		}
		s.inst.stop(t.stopTimeout)
		s.inst = nil
	}
	opts, err := parseRuneGUIStart(args)
	if err != nil {
		return webBrowserError(err.Error())
	}
	exec, bin := t.local, t.bin
	if opts.binary != "" {
		// Any program could be named, so the user approves it, which only
		// the workspace's executor asks for.
		if t.workspaceExec == nil {
			return webBrowserError("--binary needs your workspace to be on this machine; " +
				"start without it to run the user's Rune")
		}
		if bin, err = t.resolve(opts.binary); err != nil {
			return webBrowserError(err.Error())
		}
		exec = t.workspaceExec
	}
	ws := filepath.Join(s.dir, "workspace")
	if opts.workspace != "" {
		if ws, err = t.resolve(opts.workspace); err != nil {
			return webBrowserError(err.Error())
		}
	}
	if err := t.prepare(s.dir, ws, opts.workspace == ""); err != nil {
		return webBrowserError(err.Error())
	}
	cmdArgs := []string{"-G", "--offscreen", "--geometry", opts.size, "-d", s.dir, "-w", ws, "-e", "-"}
	if len(opts.files) > 0 {
		cmdArgs = append(append(cmdArgs, "--"), opts.files...)
	}
	s.stderr = &runeGUILog{}
	inst, err := startRuneGUIInstance(exec, workspaceapi.Cmd{
		Path: bin,
		Args: cmdArgs,
		Dir:  ws,
		// Appended to the host environment: nothing in the instance may
		// reach the user's Rune.
		Env: []string{
			"RUNE_SOCKET=", "RUNE_TOKEN=", "RUNE_CERT=", "RUNE_PID=", "RUNE_INSTALLDIR=",
			"RUNE_DATADIR=" + s.dir,
		},
		Stderr:      s.stderr,
		Description: "Rune without a window, for the rune_gui tool",
	})
	if err != nil {
		return webBrowserError(fmt.Sprintf("start %s: %v", bin, err))
	}
	s.inst = inst
	msg := fmt.Sprintf("Started Rune (pid %d) on workspace %s, with data directory %s.\n"+
		`Run ["exec", "workspaceready"] before other commands.`, inst.pid, ws, s.dir)
	if opts.workspace != "" {
		msg += " If the workspace has a .rune config, Rune first asks whether to trust it: " +
			"take a screenshot and answer with keys before that."
	}
	return agent.ToolResult{Content: msg}
}

func (t *RuneGUI) resolve(p string) (string, error) {
	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	if t.workspace == "" {
		return "", fmt.Errorf("%q is relative, but your workspace is not on this machine: "+
			"give an absolute path", p)
	}
	return filepath.Join(t.workspace, p), nil
}

// prepare sets up the data directory dir, and ws when it is the scratch
// workspace. The agent's config, once written, is kept; the log of an
// earlier instance is not.
func (t *RuneGUI) prepare(dir, ws string, scratch bool) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if scratch {
		if err := os.MkdirAll(ws, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", ws, err)
		}
	}
	logPath := filepath.Join(dir, runeGUILogFile)
	if err := os.Remove(logPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for name, body := range map[string]string{
		runeGUIConfigFile: runeGUIConfig, runeGUIDefaultsFile: runeGUIDefaults(logPath),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	agentConfig := filepath.Join(dir, runeGUIAgentFile)
	if _, err := os.Stat(agentConfig); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(agentConfig, []byte("{}\n"), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", runeGUIAgentFile, err)
		}
	} else if err != nil {
		return err
	}
	if t.userDataDir == "" {
		return nil
	}
	for _, name := range runeGUISharedDirs {
		link := filepath.Join(dir, name)
		if _, err := os.Lstat(link); err == nil {
			continue
		}
		// Linked even when missing, so a directory created later shows.
		if err := os.Symlink(filepath.Join(t.userDataDir, name), link); err != nil {
			return fmt.Errorf("share %s: %w", name, err)
		}
	}
	return nil
}

func (t *RuneGUI) config(s *runeGUISession, config string) agent.ToolResult {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(config), &doc); err != nil {
		return webBrowserError(fmt.Sprintf("the config is not a YAML map: %v", err))
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return webBrowserError(fmt.Sprintf("create %s: %v", s.dir, err))
	}
	if err := os.WriteFile(filepath.Join(s.dir, runeGUIAgentFile), []byte(config), 0o600); err != nil {
		return webBrowserError(fmt.Sprintf("write the config: %v", err))
	}
	if s.inst == nil || s.inst.hasExited() {
		return agent.ToolResult{Content: "Wrote the config; it applies when Rune starts."}
	}
	return agent.ToolResult{Content: "Wrote the config. The next screenshot reloads it; " +
		"keys that Rune does not reload live take effect after stop and start."}
}

func (t *RuneGUI) command(ctx context.Context, s *runeGUISession, line string) agent.ToolResult {
	reply, failed := t.send(ctx, s, line)
	if failed != nil {
		return *failed
	}
	reply = utf8validate.Sanitize(reply)
	return agent.ToolResult{Content: reply, IsError: strings.HasPrefix(reply, "error: ")}
}

func (t *RuneGUI) screenshot(ctx context.Context, s *runeGUISession) agent.ToolResult {
	reply, failed := t.send(ctx, s, "screenshot --reload")
	if failed != nil {
		return *failed
	}
	p, ok := strings.CutPrefix(reply, "ok ")
	if !ok {
		return agent.ToolResult{Content: utf8validate.Sanitize(reply), IsError: true}
	}
	dir := filepath.Join(s.dir, "screenshots")
	if filepath.Dir(p) != dir {
		return webBrowserError(fmt.Sprintf("Rune saved the screenshot outside %s: %s", dir, p))
	}
	data, err := os.ReadFile(p)
	_ = os.Remove(p)
	if err != nil {
		return webBrowserError(fmt.Sprintf("read screenshot: %v", err))
	}
	summary := fmt.Sprintf("Screenshot (%d bytes, image/png)", len(data))
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		summary = fmt.Sprintf("Screenshot (%dx%d, %d bytes, image/png)", cfg.Width, cfg.Height, len(data))
	}
	dataURI, err := EncodeImageDataURI(p, data, "image/png")
	if err != nil {
		return webBrowserError(err.Error())
	}
	return agent.ToolResult{
		Content: summary,
		MultiContent: []llmapi.ContentPart{
			{Type: llmapi.ContentPartTypeText, Text: summary},
			{Type: llmapi.ContentPartTypeImageURL, ImageURL: dataURI},
		},
	}
}

// send writes line to the instance and returns its reply, or the result
// that reports why there is none. An instance that does not reply is
// stopped, as its reply would come as the answer to the next command.
func (t *RuneGUI) send(ctx context.Context, s *runeGUISession, line string) (string, *agent.ToolResult) {
	inst := s.inst
	if _, err := io.WriteString(inst.stdin, line+"\n"); err != nil {
		return "", t.lost(s, fmt.Sprintf("could not send %q: %v", line, err))
	}
	timer := time.NewTimer(t.replyTimeout)
	defer timer.Stop()
	select {
	case reply, ok := <-inst.replies:
		if ok {
			return reply, nil
		}
		if inst.hasExited() {
			res := t.exited(s)
			return "", &res
		}
		return "", t.lost(s, fmt.Sprintf("could not read the reply to %q: %v", line, inst.readErr))
	case <-timer.C:
		return "", t.lost(s, fmt.Sprintf("no reply to %q within %s", line, t.replyTimeout))
	case <-ctx.Done():
		return "", t.lost(s, fmt.Sprintf("%q was canceled: %v", line, ctx.Err()))
	}
}

// lost stops the instance, whose replies can no longer be matched to the
// commands, and returns the result that reports why.
func (t *RuneGUI) lost(s *runeGUISession, why string) *agent.ToolResult {
	s.inst.stop(t.stopTimeout)
	s.inst = nil
	res := webBrowserError(why + `; stopped Rune, run ["start"] to start it again. Its last log lines:` +
		"\n" + s.logTail(runeGUIExitLogLines))
	return &res
}

// exited reports the exit of the instance, which has exited, and lets the
// session start another.
func (t *RuneGUI) exited(s *runeGUISession) agent.ToolResult {
	inst := s.inst
	inst.stop(t.stopTimeout)
	s.inst = nil
	status := "exit status 0"
	if inst.exitErr != nil {
		status = inst.exitErr.Error()
	}
	return webBrowserError(fmt.Sprintf(`Rune exited (%s); run ["start"] to start it again. Its last log lines:`+
		"\n%s", status, s.logTail(runeGUIExitLogLines)))
}

// runeGUIInstance is a running Rune and the pipes to its stdin and stdout.
type runeGUIInstance struct {
	exec   workspaceapi.Executor
	pid    workspaceapi.Pid
	cancel context.CancelFunc
	stdin  *os.File
	// replies carries the lines Rune writes to stdout, and closes when
	// stdout ends or cannot be read, after setting readErr.
	replies <-chan string
	readErr error
	// exited closes when the process exits, after setting exitErr.
	exited  chan struct{}
	exitErr error
}

// startRuneGUIInstance starts cmd with pipes for its stdin and stdout.
func startRuneGUIInstance(exec workspaceapi.Executor, cmd workspaceapi.Cmd) (*runeGUIInstance, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create stdin pipe: %w", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_, _ = inR.Close(), inW.Close()
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}
	// The instance outlives the tool call that starts it.
	ctx, cancel := context.WithCancel(context.Background())
	watcher := newProcessWatcher()
	cmd.Stdin, cmd.Stdout, cmd.Watcher = inR, outW, watcher
	pid, err := exec.Start(ctx, cmd)
	if err != nil {
		cancel()
		_, _ = inR.Close(), inW.Close()
		_, _ = outR.Close(), outW.Close()
		return nil, err
	}
	replies := make(chan string)
	inst := &runeGUIInstance{
		exec: exec, pid: pid, cancel: cancel, stdin: inW,
		replies: replies, exited: make(chan struct{}),
	}
	go debug.CapturePanicReport(func() {
		defer close(replies)
		defer func() { _ = outR.Close() }()
		sc := bufio.NewScanner(outR)
		sc.Buffer(nil, runeGUIMaxReply)
		for sc.Scan() {
			select {
			case replies <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
		inst.readErr = sc.Err()
	})
	go debug.CapturePanicReport(func() {
		inst.exitErr = <-watcher.WatchProcess()
		close(inst.exited)
		// The executor may relay the pipes rather than hand them to the
		// process, so they stay open until it exits. Closing the write end
		// of stdout then ends the reply reader.
		_, _ = inR.Close(), outW.Close()
	})
	return inst, nil
}

func (inst *runeGUIInstance) hasExited() bool {
	select {
	case <-inst.exited:
		return true
	default:
		return false
	}
}

// stop closes stdin, on which Rune exits, and kills the instance if it
// does not exit within timeout.
func (inst *runeGUIInstance) stop(timeout time.Duration) {
	_ = inst.stdin.Close()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-inst.exited:
	case <-timer.C:
		_ = inst.exec.Signal(inst.pid, syscall.SIGKILL)
	}
	inst.cancel()
}

// localExecutor starts processes on this machine, with this process's
// environment and then the command's variables, which win.
type localExecutor struct{}

func (localExecutor) Start(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	c := osexec.CommandContext(ctx, cmd.Path, cmd.Args...)
	c.Dir, c.Stdin, c.Stdout, c.Stderr = cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr
	c.Env = append(os.Environ(), cmd.Env...)
	if err := c.Start(); err != nil {
		return 0, err
	}
	go debug.CapturePanicReport(func() {
		err := c.Wait()
		if cmd.Watcher != nil {
			cmd.Watcher.WatchProcess() <- err
		}
	})
	return workspaceapi.Pid(c.Process.Pid), nil
}

func (localExecutor) Signal(pid workspaceapi.Pid, sig syscall.Signal) error {
	p, err := os.FindProcess(int(pid))
	if err != nil {
		return err
	}
	return p.Signal(sig)
}

func (localExecutor) LookPath(_ context.Context, name, _ string) (string, error) {
	return osexec.LookPath(name)
}

func (localExecutor) Close() error { return nil }

// runeGUILog keeps the last runeGUILogCap bytes written to it.
type runeGUILog struct {
	mu  sync.Mutex
	buf []byte
}

func (l *runeGUILog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	if len(l.buf) > runeGUILogCap {
		l.buf = slices.Clone(l.buf[len(l.buf)-runeGUILogCap:])
	}
	return len(p), nil
}

// tail returns the last n lines of the log.
func (l *runeGUILog) tail(n int) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return lastLines(string(l.buf), n)
}

func validateRuneGUICommand(command []string) error {
	if len(command) == 0 {
		return errors.New(`command is required, e.g. ["start"]`)
	}
	sub, args := command[0], command[1:]
	if !slices.Contains(runeGUICommands, sub) {
		return fmt.Errorf("%q is not an available command; the first element must be one of: %s",
			sub, strings.Join(runeGUICommands, ", "))
	}
	if sub != "config" {
		for _, arg := range args {
			if strings.ContainsAny(arg, "\r\n") {
				return fmt.Errorf("the arguments of %s must not contain line breaks", sub)
			}
		}
	}
	switch sub {
	case "config":
		if len(args) != 1 {
			return errors.New("config takes the YAML config as one argument")
		}
	case "exec":
		if len(args) == 0 {
			return errors.New(`exec needs a command line, e.g. ["exec", "edit main.go"]`)
		}
	case "keys":
		if len(args) == 0 {
			return errors.New(`keys needs a key sequence, e.g. ["keys", "ix<esc>"]`)
		}
	case "screenshot", "stop":
		if len(args) > 0 {
			return fmt.Errorf("%s takes no arguments", sub)
		}
	case "log":
		if len(args) > 1 {
			return errors.New("log takes at most the number of lines")
		}
		if len(args) == 1 {
			if n, err := strconv.Atoi(args[0]); err != nil || n < 1 || n > runeGUIMaxLogLines {
				return fmt.Errorf("log takes a number of lines from 1 to %d, not %q", runeGUIMaxLogLines, args[0])
			}
		}
	}
	return nil
}
