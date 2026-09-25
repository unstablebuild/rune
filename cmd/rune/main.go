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
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	multierr "github.com/ernestrc/go-multierror"
	extbrowser "github.com/ernestrc/sensible/browser"
	log "github.com/sirupsen/logrus"
	flag "github.com/spf13/pflag"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/blue/release/cdnrelease"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"unstable.build/rune/auth"
	"unstable.build/rune/cmd/rune/crashreport"
	"unstable.build/rune/cmd/rune/ide/apiclient"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension/extensionv2"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/pkgtrust"
	"unstable.build/rune/internal/llm/llmrpc"
	"unstable.build/rune/internal/rpc"
	"unstable.build/rune/internal/term/gui"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacerpc"
	"unstable.build/rune/internal/workspace/workspacessh"
)

const (
	doubleClickTimeout = 500 * time.Millisecond
	telemetryPeriod    = 1 * time.Hour

	flagNameNoSessionReopen = "no-session-reopen"
)

var (
	apicfg = apiclient.DefaultConfig()
	// Version is a combination of Tag, Commit and BuildDate,
	// representing this executables version.
	version string

	configFilename          = "config.yaml"
	configStarFilename      = "config.star"
	workspaceConfigFilename = ".rune/config.yaml"
	home                    string

	defaultConfigPath string
	flagConfigPath    *string
	defaultDataPath   string
	flagDataPath      *string

	flagWorkspaceServerLogFile *string

	flagVersion   = flag.BoolP("version", "v", false, "Print version information and exit")
	flagWorkspace = flag.StringP("workspace", "w", cwdURI().String(),
		"Set the initial workspace to open in the format [scheme:][//[userinfo@]host][/]path")
	flagFPS      = flag.BoolP("fps", "f", false, "Render FPS on GUI mode")
	flagGUI      = flag.BoolP("gui", "G", false, "Run Rune in manual GUI mode")
	flagTUI      = flag.Bool("tui", false, "Run Rune in TUI mode")
	flagHeadless = flag.Bool("headless", false,
		"Run Rune as a headless network node, with no editor UI")

	// marked hidden
	flagWorkspaceServer = flag.StringP("workspace-server", "x", "",
		"Run a workspace server from standard input and output")
	flagWorkspaceServerInstall = flag.String("install", "",
		"CSV manifest of `id@version` packages the workspace server should "+
			"install into its ~/.rune to mirror the local toolchain")
	flagHTTPAddress = flag.String("rune-http-address", apicfg.HTTPEndpointAddress,
		"Rune HTTP API endpoint host/port pair")
	flagGRPCAddress = flag.String("rune-grpc-address", apicfg.GRPCEndpointAddress,
		"Rune GRPC API endpoint host/port pair")
	flagGRPCInsecure = flag.Bool("rune-grpc-insecure", apicfg.InsecureTransport,
		"If set to false, does not use GRPC over TLS or oauth2 credentials.")
	flagReleaseCollection = flag.String("rune-release-collection",
		apicfg.ReleaseCollection,
		"Collection name for the release manager.")
	flagZdotDir = flag.String("rune-zdotdir", "", "Initial ZDOTDIR directory when using default OS shell via $SHELL.")

	flagWebsiteAddress = flag.String("rune-website-address", apiclient.DefaultWebsiteAddress,
		"Base URL of the Rune website. Used to build the checkout URL "+
			"opened by the upgrade prompt during bootstrap and lockdown.")
	flagNoSessionReopen = flag.Bool(flagNameNoSessionReopen, false,
		"Do not offer to reopen the workspaces from the last session. "+
			"Set on the processes spawned by guiwindownew.")
)

func init() {
	var err error
	home, err = os.UserHomeDir()
	if err != nil {
		log.Error(err)
		home = "."
	}

	defaultConfigPath = resolveDefaultConfigPath(defaultDataPath)
	flagConfigPath = flag.StringP("config", "c", defaultConfigPath,
		"Use this file for configuring rune")

	defaultDataPath = filepath.Join(home, ".rune")
	flagDataPath = flag.StringP("datadir", "d", defaultDataPath,
		"Set temporary data directory")

	flagWorkspaceServerLogFile = flag.StringP("workspace-server-log", "o",
		filepath.Join(defaultDataPath, "server.log"),
		"Log workspace server logs to this file")

	version = versionString(debug.Tag, debug.Commit, debug.BuildDate)
}

// versionString renders the --version line. buildDate is empty for
// builds that go through plain `go build` rather than the Makefile or
// a distro package, so it is reported only when the ldflag is set.
func versionString(tag, commit, buildDate string) string {
	if buildDate == "" {
		return fmt.Sprintf("%s (HEAD is %s)", tag, commit)
	}
	return fmt.Sprintf("%s (HEAD is %s, built %s)", tag, commit, buildDate)
}

func resolveDefaultConfigPath(dataDir string) string {
	yamlPath := filepath.Join(dataDir, configFilename)
	starPath := filepath.Join(dataDir, configStarFilename)
	if _, err := os.Stat(yamlPath); err == nil {
		return yamlPath
	}
	if _, err := os.Stat(starPath); err == nil {
		return starPath
	}
	return yamlPath
}

func cwdURI() workspaceapi.URI {
	wd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Failed to get working directory: %s", err)
	}
	uri, err := workspaceapi.CurrentUserHostURI(wd)
	if err != nil {
		log.Fatalf("Failed to parse working directory as URI %s: %s", wd, err)
	}
	return uri
}

func startWorkspaceServer() int {
	var logger *slog.Logger

	newScheme := workspace.NewFileScheme
	// The log defaults to <default datadir>/server.log; when the datadir is
	// overridden without an explicit --workspace-server-log, keep the log
	// beside the rest of the server state. debug.StartPProfOnSignal writes the
	// pprof HTTP address here on SIGUSR1.
	serverLogs := *flagWorkspaceServerLogFile
	if !flag.Lookup("workspace-server-log").Changed {
		serverLogs = filepath.Join(*flagDataPath, "server.log")
	}
	f, err := workspace.OpenFile(serverLogs,
		os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatal(err)
	}
	log.SetOutput(f)
	log.SetLevel(log.InfoLevel)
	log.SetFormatter(logging.LogrusLogdFormatter{})
	rpc.EnableGRPCLogging(f, f, f)
	logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	defer f.Close()
	defer func() {
		_ = f.Sync()
	}()
	slog.SetDefault(logger)

	log.Tracef("Initialized debug logger")

	// log unhandled signals for debugging
	ch := make(chan os.Signal, 1)
	quitch := make(chan struct{})
	grpcServer := workspacessh.NewSchemeServer(
		grpc.ChainUnaryInterceptor(
			rpc.UnaryReportRecoveryInterceptor(),
		),
		grpc.ChainStreamInterceptor(
			rpc.StreamReportRecoveryInterceptor(),
		),
		grpc.MaxRecvMsgSize(llmrpc.MaxRecvMsgSize),
		grpc.MaxSendMsgSize(llmrpc.MaxSendMsgSize),
	)
	signal.Notify(ch)

	defer close(quitch)
	defer signal.Reset()

	go debug.CapturePanicReport(func() {
		defer grpcServer.Stop()
		for {
			select {
			case sig := <-ch:
				switch sig {
				case syscall.SIGTERM, syscall.SIGINT:
					log.Infof("Received %v signal: cleaning up...", sig)
					return
				case syscall.SIGKILL:
					log.Info("Received SIGKILL signal: exiting")
					os.Exit(1)
				case syscall.SIGURG:
					/* received when socket urgent data is ready to be read */
				default:
					log.Debugf("Received unhandled signal: %#v", sig)
				}
			case <-quitch:
				return
			}
		}
	})

	uri, err := workspaceapi.CurrentUserHostURI(*flagWorkspaceServer)
	if err != nil {
		log.Error(err)
		return 2
	}

	// Install the local toolchain's packages, load the remote config, and
	// apply gui.env before serving so extension-spawned tools resolve. A
	// provisioning scheme reads/writes the remote filesystem; provisioning
	// failures never abort the connection (they warn and continue).
	provScheme, err := newScheme(context.Background(), config.NopConfig(), uri)
	if err != nil {
		log.Error(err)
		return 3
	}
	rootCfg := provisionRemote(provScheme, uri)
	_ = provScheme.Close()

	scheme, err := newScheme(context.Background(), rootCfg, uri)
	if err != nil {
		log.Error(err)
		return 3
	}
	defer scheme.Close()

	server := workspacerpc.NewServer(scheme,
		workspacerpc.CommandAuthorizerFunc(
			func(context.Context, workspaceapi.Cmd) error { return nil }))
	defer func() {
		_ = server.Stop()
	}()

	err = workspacessh.StartSchemeServer(log.StandardLogger(), server, grpcServer)
	if err != nil {
		log.Error(err)
		return 4
	}
	log.Tracef("StartSchemeServer returned with no error")
	return 0
}

func main() {
	if err := flag.CommandLine.MarkHidden("rune-http-address"); err != nil {
		panic(err)
	}
	if err := flag.CommandLine.MarkHidden("rune-grpc-address"); err != nil {
		panic(err)
	}
	if err := flag.CommandLine.MarkHidden("rune-grpc-insecure"); err != nil {
		panic(err)
	}
	if err := flag.CommandLine.MarkHidden("workspace-server"); err != nil {
		panic(err)
	}
	if err := flag.CommandLine.MarkHidden("workspace-server-log"); err != nil {
		panic(err)
	}
	if err := flag.CommandLine.MarkHidden("install"); err != nil {
		panic(err)
	}
	if err := flag.CommandLine.MarkHidden("rune-release-collection"); err != nil {
		panic(err)
	}
	if err := flag.CommandLine.MarkHidden("rune-zdotdir"); err != nil {
		panic(err)
	}
	if err := flag.CommandLine.MarkHidden("rune-website-address"); err != nil {
		panic(err)
	}
	if err := flag.CommandLine.MarkHidden(flagNameNoSessionReopen); err != nil {
		panic(err)
	}

	flag.ErrHelp = errors.New("")
	flag.Usage = func() {
		fmt.Printf("Rune %s\n\n", version)
		flag.PrintDefaults()
	}

	flag.Parse()

	exec, _ := os.Executable()
	// If no manual tui/gui flag was set, assume we were launched as a desktop
	// app and inject the same defaults the platform launcher would normally pass.
	if !*flagGUI && !*flagTUI && !*flagHeadless && *flagWorkspaceServer == "" {
		if err := os.MkdirAll(*flagDataPath, 0777); err != nil {
			fmt.Fprintf(os.Stderr, "mkdir datadir %q: %s",
				*flagDataPath, err)
		}
		// gracefully degrade; launch without zsh customization
		// which looses ensuring modal vte works well with zsh
		zdotDir := ""
		if src, ok := bundleZdotDir(runtime.GOOS, exec); ok {
			dst := filepath.Join(*flagDataPath, "zdot")
			if err := installZdotDir(src, dst); err != nil {
				fmt.Fprintf(os.Stderr,
					"install zdot %q -> %q: %s", src, dst, err)
			} else {
				zdotDir = dst
			}
		}
		defaults, ok := appLaunchArgs(runtime.GOOS, zdotDir)
		if ok {
			os.Args = append(os.Args[:1], append(defaults, os.Args[1:]...)...)

			// best effort redirect stdout/err to <datadir>/launch.log
			// so runtime fatal stderr dumps can later be ingested as
			// crash reports.
			logPath := crashreport.DefaultLaunchLogPath(*flagDataPath)
			f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err == nil {
				// syscall.Dup2 isn't defined on linux/arm64 (the
				// kernel only exposes Dup3 there); golang.org/x/sys/unix
				// papers over the difference.
				fd := int(f.Fd())
				_ = unix.Dup2(fd, int(os.Stderr.Fd()))
			}
			flag.Parse()
		}
	}

	// ensure that data path exists
	if _, err := os.Stat(*flagDataPath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			err = fmt.Errorf("stat %q: %s", *flagDataPath, err)
			fmt.Printf("%s", err)
			os.Exit(1)
		}
		if err := os.MkdirAll(*flagDataPath, 0777); err != nil {
			err = fmt.Errorf("mkdir %q: %s", *flagDataPath, err)
			fmt.Printf("%s", err)
			os.Exit(1)
		}
	}
	defaultConfigPath = resolveDefaultConfigPath(*flagDataPath)
	if !flag.Lookup("config").Changed {
		*flagConfigPath = defaultConfigPath
	}

	// Set up the crash report directory under the data path so reports
	// are stored durably rather than in the OS temp dir.
	reportsDir := filepath.Join(*flagDataPath, "reports")
	if err := os.MkdirAll(reportsDir, 0777); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir reports %q: %s", reportsDir, err)
	} else {
		debug.ReportsDir = reportsDir
	}

	var code int
	_, err, ok := debug.CapturePanicReportWith(
		reportsDir, debug.Package, debug.Tag, func() {
			code = run()
		})
	if ok {
		os.Exit(code)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal error: %v", err)
		log.Errorf("fatal error: %v", err)
	}
	os.Exit(4)
}

var zdotFiles = []string{".zshenv", ".zprofile", ".zshrc", ".zlogin"}

func appLaunchArgs(goos, zdotDir string) ([]string, bool) {
	var args []string
	if zdotDir != "" {
		args = append(args, "--rune-zdotdir="+zdotDir)
	}
	switch goos {
	case "darwin", "linux", "windows":
		return append(args, "-G", "-w", ""), true
	default:
		return nil, false
	}
}

func bundleZdotDir(goos, execPath string) (string, bool) {
	switch goos {
	case "darwin":
		macosDir := filepath.Dir(execPath)
		contentsDir := filepath.Dir(macosDir)
		resourcesDir := filepath.Join(contentsDir, "Resources")
		return filepath.Join(resourcesDir, "zdot"), true
	case "linux":
		appDir, ok := linuxAppDir(execPath)
		if !ok {
			return "", false
		}
		return filepath.Join(appDir, "share", "zdot"), true
	default:
		return "", false
	}
}

func installZdotDir(srcDir, dstDir string) error {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	for _, name := range zdotFiles {
		src := filepath.Join(srcDir, name)
		data, err := os.ReadFile(src)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		dst := filepath.Join(dstDir, name)
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func linuxAppDir(execPath string) (string, bool) {
	binDir := filepath.Dir(execPath)
	if filepath.Base(binDir) != "bin" {
		return "", false
	}

	appDir := filepath.Dir(binDir)
	if filepath.Base(appDir) != "rune.app" {
		return "", false
	}

	return appDir, true
}

// headlessFlags are the flags a headless node reads. Any other flag
// passed with --headless is rejected rather than ignored, so a script
// finds out instead of getting a node that quietly differs from what it
// asked for.
var headlessFlags = map[string]bool{
	"headless":                true,
	"version":                 true,
	"config":                  true,
	"datadir":                 true,
	"rune-http-address":       true,
	"rune-grpc-address":       true,
	"rune-grpc-insecure":      true,
	"rune-release-collection": true,
	"rune-website-address":    true,
}

// checkModeArgs rejects the argument combinations the mode dispatch in
// run would otherwise resolve silently.
func checkModeArgs(
	fs *flag.FlagSet, gui, tui, headless bool, files []string,
) error {
	var modes []string
	for _, m := range []struct {
		name string
		set  bool
	}{{"gui", gui}, {"tui", tui}, {"headless", headless}} {
		if m.set {
			modes = append(modes, "--"+m.name)
		}
	}
	if len(modes) > 1 {
		return fmt.Errorf(
			"only one of --gui, --tui or --headless can be passed at once, got %s",
			strings.Join(modes, " "))
	}
	if !headless {
		return nil
	}

	var rejected []string
	fs.Visit(func(f *flag.Flag) {
		if !headlessFlags[f.Name] {
			rejected = append(rejected, "--"+f.Name)
		}
	})
	if len(files) > 0 {
		rejected = append(rejected, "file arguments")
	}
	if len(rejected) == 0 {
		return nil
	}
	return fmt.Errorf("--headless does not take %s", strings.Join(rejected, ", "))
}

func run() int {
	var filenames []string

	if *flagVersion {
		fmt.Printf("Rune %s\n", version)
		return 0
	}

	filenames = append(filenames, flag.Args()...)
	if err := checkModeArgs(flag.CommandLine,
		*flagGUI, *flagTUI, *flagHeadless, filenames); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	if err := setupRuneBinPATH(*flagDataPath); err != nil {
		log.Errorf("installed executables will not be available: "+
			"set the PATH env variable: %v", err)
	}

	// TUI and headless inherit the parent-shell PATH the user already
	// exported, so they skip the login SHELL PATH resolve.
	var pathDone <-chan error
	if !*flagTUI && !*flagHeadless {
		pathDone = startLoginShellPATHResolve(*flagDataPath)
	} else {
		ch := make(chan error)
		pathDone = ch
		close(ch)
	}

	rpc.DisableGRPCLogging()

	debug.StartPProfOnSignal()

	if *flagWorkspaceServer != "" {
		if err := <-pathDone; err != nil {
			log.Errorf("could not resolve login shell PATH; tools on "+
				"it (e.g. homebrew, mise) may be unavailable: %v", err)
		}
		code := startWorkspaceServer()
		return code
	}

	ctx := context.Background()

	if *flagHeadless {
		return runHeadless(ctx)
	}

	var mu sync.Mutex
	runner, err := extensionv2.NewRunner(ctx, &mu, *flagDataPath)
	if err != nil {
		err = fmt.Errorf("new extension runner: %v", err)
		fmt.Fprintf(os.Stderr, "%s", err)
		return 1
	}

	if *flagConfigPath != defaultConfigPath {
		if _, err := os.Stat(*flagConfigPath); err != nil {
			err = fmt.Errorf("stat %q: %s", *flagConfigPath, err)
			fmt.Printf("%s", err)
			return 1
		}
	}

	// A single process-wide trust store distributes the package-signing
	// keyring to the package manager (install verification) and the
	// authorizer (verified-publisher check). The store fetches the served
	// keyring once, asynchronously, at construction; the first
	// VerifyBundle briefly waits for it.
	trust := pkgtrust.NewStore(*flagDataPath, trustKeyringFetcher())

	if *flagGUI {
		return runGUI(filenames, runner, trust, &mu, pathDone)
	} else if *flagTUI {
		return runTUI(filenames, runner, trust, &mu)
	} else {
		fmt.Fprintf(os.Stderr,
			"one of --gui, --tui or --headless must be set if running on %s\n",
			runtime.GOOS)
		return 1
	}
}

func runTUI(
	filenames []string, runner ide.ExtensionsRunner, trust *pkgtrust.Store,
	mu *sync.Mutex,
) int {
	opts := []ide.Option{
		ide.WithExtensionsRunner(runner),
		ide.WithInitShader(
			initShader,
			initShaderFPS,
			initShaderDuration),
		ide.WithShutdownShader(
			func(defaultAttr term.Attributes) shader.Shader {
				return shutdownShader(defaultAttr)
			}, 30, shutdownShaderDuration),
		ide.WithLoadingShader(loadingShader, loadingShaderFPS, loadingShaderDuration),
		ide.WithOpenShader(openShader, openShaderFPS, openShaderDuration),
		ide.WithLocker(mu),
		ide.WithConfigFilename(workspaceConfigFilename),
		ide.WithDefaultWallpaper(makeWallpaper()),
		ide.WithDefaultConfigStarlark(defaultStarlarkConfig, true, true),
		ide.WithScheduleNextTick(func(fn func()) bool {
			return tui.PublishEvent(term.Event{Type: term.EventInterrupt, UserFunc: fn})
		}),
		ide.WithZdotDir(*flagZdotDir),
		ide.WithScheme(docsScheme, newDocsSchemeFunc(*flagConfigPath)),
		ide.WithStreamingOpen(true),
	}
	opts = append(opts, embeddedTutorialOptions()...)
	if debug.DebugBuild == "true" {
		opts = append(opts, ide.WithDebugCommands(true))
	}

	scheduleNextTick := func(fn func()) bool {
		return tui.PublishEvent(term.Event{Type: term.EventInterrupt, UserFunc: fn})
	}

	storage := newRuneStorage(*flagDataPath)
	rootCfg, err := ide.Config(*flagConfigPath, runeDefaultConfig())
	if err != nil {
		fmt.Printf("%s", err)
		return 1
	}
	client, releaseManager := newAPIClient(storage, os.TempDir(), rootCfg)
	defer client.Close()

	net := newNetwork(rootCfg, *flagDataPath, newNetworkGate(client))
	net.startAutoJoin()
	defer func() {
		_ = net.Close()
	}()

	opts = append(opts,
		ide.WithReleaseManager(releaseManager),
		nagPromptOption(client),
		ide.WithWatchedFilesChangeHook(client.RecordWatchedFilesChange),
		ide.WithCommandDispatchHook(client.RecordCommand),
		net.completerOption(),
	)

	i, err := ide.New(*flagWorkspace, *flagConfigPath,
		*flagDataPath, trust, storage, opts...)
	if err != nil {
		fmt.Printf("%s", err)
		return 1
	}
	if err := net.register(i, scheduleNextTick); err != nil {
		log.Errorf("register network: %v", err)
	}

	err = subscribeOtherCommands(i, client, *flagConfigPath)
	if err != nil {
		log.Errorf("subscribe to commands: %v", err)
	}

	if client.TelemetryEnabled() {
		err := i.SubscribeEvents(apiclient.TelemetryEvents(), client)
		if err != nil {
			log.Errorf("subscribe telemetry events: %v", err)
		}
	}

	openFiles(i, filenames)

	scheduleCrashReportCheck(i, client, *flagDataPath, scheduleNextTick)

	upgradeCtx, upgradeCancel := context.WithCancel(context.Background())
	defer upgradeCancel()
	upgradeMgr := scheduleUpgradeCheck(upgradeCtx, i, apiclient.DefaultDownloadsHost,
		scheduleNextTick)
	if err := registerUpgradeCommand(i, upgradeMgr); err != nil {
		log.Errorf("register upgrade command: %v", err)
	}
	defer func() {
		_ = upgradeMgr.Close()
	}()

	var ret error
	if err := doRunTUI(mu, i); err != nil {
		ret = multierr.Append(ret, err)
	}
	if err := i.Close(); err != nil {
		ret = multierr.Append(ret, err)
	}
	if ret != nil {
		fmt.Printf("%s", ret)
		log.Error(ret)
		return 1
	}

	return 0
}

func runGUI(
	filenames []string, runner ide.ExtensionsRunner, trust *pkgtrust.Store,
	mu *sync.Mutex, pathDone <-chan error,
) int {
	setEnvForGUI(*flagDataPath)

	// Capture the launch command for guiwindownew. Visit iterates
	// only over flags that were explicitly set (including
	// macOS-injected defaults after the second flag.Parse), so
	// positional filename args are naturally excluded. The reopen
	// flag is re-added unconditionally rather than inherited, so a
	// spawned window never restores the parent instance's session.
	execPath, _ := os.Executable()
	var launchArgs []string
	flag.CommandLine.Visit(func(f *flag.Flag) {
		if f.Name == flagNameNoSessionReopen {
			return
		}
		launchArgs = append(launchArgs, fmt.Sprintf("--%s=%s", f.Name, f.Value.String()))
	})
	launchArgs = append(launchArgs, "--"+flagNameNoSessionReopen)
	launchCmd := append([]string{execPath}, launchArgs...)

	chdirerr := os.Chdir(home)
	if chdirerr != nil {
		chdirerr = fmt.Errorf("cd %s: %w", home, chdirerr)
	}

	// The GUI is created after the bootstrap handler, but the IDE may
	// publish redraw interrupts during its async init before the GUI
	// exists. guiRef is loaded atomically so those early interrupts are
	// dropped (superseded by the GUI's initial draw) rather than racing.
	var guiRef atomic.Pointer[gui.GUI]
	publishEvent := func(ev term.Event) bool {
		g := guiRef.Load()
		if g == nil {
			return false
		}
		return g.PublishEvent(ev)
	}
	cellPixelSize := func() (int, int) {
		g := guiRef.Load()
		if g == nil {
			return 0, 0
		}
		return g.CellPixelSize()
	}

	// We load config twice, but it's better than the race conditions caused
	// by env var resolution order.
	rootCfg, envErr := ide.Config(*flagConfigPath, runeDefaultConfig())
	if envErr != nil {
		envErr = fmt.Errorf("load config for gui.env: %w", envErr)
		rootCfg = config.NopConfig()
	}
	guiCfg, _, err := getGUIConfig(rootCfg)
	if err != nil {
		envErr = multierr.Append(envErr, err)
	}
	if guiCfg == nil {
		guiCfg = config.NopConfig()
	}

	// gui.env must be applied before the workspace is created, otherwise we
	// risk running LSP servers or other workspace-wide processes without the
	// right env vars. When gui.env sets its own PATH the value may expand
	// $PATH, so we must wait for the login-shell PATH resolve first; when it
	// does not, we still wait, because we run LSP servers or other tools
	// which might need PATH to be set
	if err := applyShellPATHAndGUIEnv(guiCfg, pathDone); err != nil {
		envErr = multierr.Append(envErr, err)
	}

	root, err := newBootstrapHandler(
		*flagDataPath, *flagConfigPath,
		*flagWorkspace, *flagZdotDir, filenames,
		launchCmd, runner, mu, publishEvent, cellPixelSize,
		func(u *url.URL) error { return extbrowser.Browse(u) },
		text.NewSystemClipboard(), os.TempDir(), rootCfg, trust,
	)
	if err != nil {
		fmt.Printf("ide: %s", err)
		log.Errorf("ide: %v", err)
		return 1
	}

	browser := root.browser()
	if chdirerr != nil {
		_, _ = browser.Notify(browserapi.LevelError, "%v", chdirerr)
	}
	cfg, ok, err := getGUIConfig(root.config())
	if err != nil {
		_, _ = browser.Notify(browserapi.LevelError, "%v", err)
	}
	if !ok {
		cfg = config.NopConfig()
	}

	transparentWindow := getGUITransparentWindow(browser, cfg)

	if envErr != nil {
		_, _ = browser.Notify(browserapi.LevelError, "%s", envErr)
	}

	options := buildGUIOptions(browser, cfg, transparentWindow, mu, *flagFPS)
	options = append(options, gui.WithDragObserver(root.dragObserver))
	options = append(options, gui.WithLinkObserver(root.linkObserver))

	storage := root.storage
	width, height, ok := getLastSize(storage)
	if ok {
		options = append(options, gui.WithSize(width, height))
	}
	x, y, ok := getLastPosition(storage)
	if ok {
		options = append(options, gui.WithPosition(x, y))
	}

	g, err := gui.New(root, options...)
	if err != nil {
		fmt.Printf("gui: %s", err)
		log.Errorf("gui: %v", err)
		return 1
	}
	defer func() { _ = g.Close() }()
	defer func() { _ = root.Close() }()
	guiRef.Store(g)
	root.attachGUI(g, transparentWindow)
	defer watchGUISignals(publishEvent,
		quitEvent(appMenuKeyBindings(cfg)))()

	if fg, bg := getGUIWindowOpacity(browser, cfg); transparentWindow && (fg != 1 || bg != 1) {
		g.SetOpacity(bg, fg)
	}
	if root.alreadyBootstrapped() {
		if err := root.setupConfiguredIDE(root.realIDE, root.client); err != nil {
			fmt.Printf("setup ide: %s", err)
			log.Errorf("setup ide: %v", err)
			return 1
		}
	} else {
		if err := root.setupPreIDE(); err != nil {
			fmt.Printf("setup pre-config ide: %s", err)
			log.Errorf("setup pre-config ide: %v", err)
			return 1
		}
	}
	root.publishAppMenuInstall()
	err = g.Run("Rune")
	if err != nil && !errors.Is(err, gui.ErrHandlerExited) {
		fmt.Printf("%s", err)
		log.Errorf("run: %v", err)
		saveLastSize(storage, g)
		saveLastPosition(storage, g)
		return 1
	}

	saveLastSize(storage, g)
	saveLastPosition(storage, g)
	return 0
}

// buildGUIOptions builds the gui.Option list for the production GUI
// runtime from the gui config section. It is shared between runGUI and
// the GUI benchmark harness so benchmarks measure the exact production
// GUI wiring.
func buildGUIOptions(
	b browser.Browser, cfg config.Config, transparentWindow bool,
	mu sync.Locker, printFPS bool,
) []gui.Option {
	return []gui.Option{
		gui.WithColorThemes(getGUIDefaultColorTheme(b, cfg), getGUIColorThemes(b, cfg)),
		gui.WithFontDPI(getGUIFontDPI(b, cfg)),
		gui.WithFontSize(getGUIFontSize(b, cfg)),
		gui.WithFontFamily(getGUIFontFamily(b, cfg)),
		gui.WithColumnWidthOffset(getGUIColumnWidthOffset(b, cfg)),
		gui.WithLineHeightOffset(getGUILineHeightOffset(b, cfg)),
		gui.WithScrollMultiplier(getGUIScrollMultiplier(b, cfg)),
		gui.WithRenderOffset(0, 10),
		gui.WithLigatures(getGUILigatures(b, cfg)),
		gui.WithTransparentWindow(transparentWindow),
		gui.WithBackgroundBlur(getGUIBackgroundBlur(b, cfg)),
		gui.WithLocker(mu),
		gui.WithPrintFPS(printFPS),
		gui.WithKeyMapping(getGUIKeyMapping(b, cfg)),
		gui.WithAltModifier(getGUIAltModifier(b, cfg)),
		gui.WithCloseRequestEvent(quitEvent(appMenuKeyBindings(cfg))),
	}
}

// applyShellPATHAndGUIEnv applies the login-shell PATH resolution and gui.env.
//
// When gui.env defines its own PATH, the value may expand $PATH and therefore
// depends on the resolved login PATH; in that case we block on pathDone so the
// gui.env baseline is captured after the resolved PATH lands, keeping expansion
// race-free. On resolution timeout or error Rune continues with the inherited
// PATH and notifies the user.
//
// When gui.env does not define PATH, blocking would needlessly delay startup,
// so we apply gui.env immediately and let the background resolve apply the
// resolved login PATH via os.Setenv whenever it completes.
func applyShellPATHAndGUIEnv(
	cfg config.Config, pathDone <-chan error,
) (ret error) {
	env, err := getGUIEnvVars(cfg)
	if err != nil {
		return fmt.Errorf("load 'gui.env' from config: %v", err)
	}

	// we must always wait for PATH, otherwise we might end up with
	// tools like gopls running without PATH set, and having issues
	// when trying to find "go" in their PATH.
	ret = waitLoginShellPATH(pathDone)

	if err := applyGUIEnvVars(env); err != nil {
		ret = multierr.Append(ret, fmt.Errorf("apply gui.env: %v", err))
	}
	return ret
}

// waitLoginShellPATH blocks until the background login-shell PATH resolution
// completes, applying the resolved PATH via os.Setenv on success and notifying
// the user on error or timeout.
func waitLoginShellPATH(pathDone <-chan error) error {
	select {
	case err := <-pathDone:
		if err != nil {
			return fmt.Errorf("could not resolve your login shell PATH; tools on it "+
				"(e.g. homebrew, mise) may be unavailable: %v", err)
		}
		return nil
	case <-time.After(loginPathTimeout + time.Second):
		return fmt.Errorf("resolving your login shell PATH timed out; tools on it " +
			"(e.g. homebrew, mise) may be unavailable")
	}
}

// newAPIClient constructs the production apiclient.Client and its
// release.Manager from a shared storage. Both can be passed to
// ide.New via WithReleaseManager without creating an
// IDE → apiclient → IDE cycle. installBackupDir hosts the install-ID
// tamper-detection backup (the OS temp dir in production).
func newAPIClient(
	storage storageapi.Service, installBackupDir string, cfg config.Config,
) (*apiclient.Client, release.Manager) {
	apicfg := apiclient.DefaultConfig()
	apicfg.HTTPEndpointAddress = *flagHTTPAddress
	apicfg.GRPCEndpointAddress = *flagGRPCAddress
	apicfg.InsecureTransport = *flagGRPCInsecure
	apicfg.ReleaseCollection = *flagReleaseCollection
	apicfg.WebsiteAddress = *flagWebsiteAddress
	apicfg.EnableTelemetry = ide.TelemetryEnabled(cfg)
	apicfg.TelemetryPeriod = telemetryPeriod
	apicfg.InstallBackupDir = installBackupDir
	apicfg.EditorMode = ide.EditorMode(cfg)
	client := apiclient.New(storage, apicfg, *flagDataPath)
	// Release downloads are unauthenticated: the oauth transport
	// fails client-side with auth.ErrNotAuthenticated when no token
	// is cached, which would break package installs for logged-out
	// users under the usage-based paywall.
	httpClient := &http.Client{}
	releaseManager := cdnrelease.NewManager(httpClient,
		idepkg.ReleasesURL(*flagHTTPAddress, idepkg.HostArch()))
	return client, releaseManager
}

// trustKeyringFetcher builds the KeyringFetcher the process trust store uses
// to load the package-signing keyring the API advertises. It fetches the
// oauth2 config once and returns the armored keyring it carries.
func trustKeyringFetcher() pkgtrust.KeyringFetcher {
	endpoint, err := url.Parse(*flagHTTPAddress)
	if err != nil {
		log.Warnf("parse http endpoint for trust keyring fetch: %v", err)
		return nil
	}
	return func() ([]byte, error) {
		conf, err := auth.FetchConfig(endpoint)
		if err != nil {
			return nil, err
		}
		return []byte(conf.PackageKeyringArmored), nil
	}
}

func doRunTUI(mu *sync.Mutex, i *ide.IDE) error {
	err := tui.Run(i.Ready(),
		tui.WithLocker(mu),
		tui.WithDefaultAttributes(i.DefaultAttributes()),
		tui.WithInputMode(i.InputMode()),
	)
	if err != nil {
		return fmt.Errorf("tui run: %w", err)
	}

	return nil
}

func openFiles(i *ide.IDE, filenames []string) {
	for _, file := range filenames {
		uri, err := workspaceapi.CurrentUserHostURI(file)
		if err != nil {
			err = fmt.Errorf("get uri: %w", err)
			_, _ = i.Notifications().Notify(browserapi.LevelError, err.Error())
			continue
		}
		err = i.Open(uri)
		if err != nil {
			_, _ = i.Notifications().Notify(browserapi.LevelError, err.Error())
			continue
		}
	}
}
