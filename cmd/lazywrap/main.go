package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"lazywrap/internal/config"
	"lazywrap/internal/lifecycle"
	proc "lazywrap/internal/process"
	appProxy "lazywrap/internal/proxy"
	"lazywrap/internal/supervisor"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}

func run() error {
	return runArgs(os.Args[1:], os.Stdout)
}

func runArgs(args []string, output io.Writer) error {
	if len(args) > 0 && args[0] == "help" {
		switch {
		case len(args) == 1:
			args = []string{"-h"}
		case len(args) == 2 && args[1] == "doctor":
			args = []string{"doctor", "-h"}
		default:
			return fmt.Errorf("help: unknown topic or unexpected arguments: %v (use lazywrap help)", args[1:])
		}
	}
	def, e := config.DefaultPath()
	if e != nil {
		return e
	}
	if len(args) > 0 && args[0] == "doctor" {
		return runDoctorArgs(def, args[1:], output)
	}

	flags := flag.NewFlagSet("lazywrap", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("c", def, "configuration file")
	flags.Usage = func() {
		fmt.Fprintln(output, "Lazy-start HTTP/gRPC/TCP proxy and local process supervisor.")
		fmt.Fprintln(output, "\nUsage: lazywrap [-c FILE]")
		fmt.Fprintln(output, "       lazywrap doctor [-c FILE]")
		fmt.Fprintln(output, "       lazywrap help [doctor]")
		fmt.Fprintln(output, "\nCommands:")
		fmt.Fprintln(output, "  doctor  Validate configuration without running hooks or app commands")
		fmt.Fprintln(output, "  help    Show general help or help for a command")
		fmt.Fprintln(output, "\nWithout a command, start the proxy and supervise configured services.")
		fmt.Fprintln(output, "\nOptions:")
		flags.PrintDefaults()
		fmt.Fprintln(output, "  -h, --help\n        show help")
		fmt.Fprintln(output, "\nExamples:\n  lazywrap -c ./config.yaml\n  lazywrap doctor -c ./config.yaml\n  lazywrap help doctor")
	}
	if e := flags.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if flags.NArg() == 1 && flags.Arg(0) == "doctor" {
		return runDoctor(*path, output)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	return runServer(*path)
}

func runDoctorArgs(defaultPath string, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("lazywrap doctor", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("c", defaultPath, "configuration file")
	flags.Usage = func() {
		fmt.Fprintln(output, "Validate configuration and list resolved apps without running hooks or app commands.")
		fmt.Fprintln(output, "\nUsage: lazywrap doctor [-c FILE]")
		fmt.Fprintln(output, "\nOptions:")
		flags.PrintDefaults()
		fmt.Fprintln(output, "  -h, --help\n        show help")
		fmt.Fprintln(output, "\nExample:\n  lazywrap doctor -c ./config.yaml")
	}
	if e := flags.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("doctor: unexpected arguments: %v", flags.Args())
	}
	return runDoctor(*path, output)
}

func runDoctor(path string, output io.Writer) error {
	cfg, e := config.LoadStrict(path)
	if e != nil {
		return fmt.Errorf("doctor: configuration is invalid: %w", e)
	}

	ids := make([]string, 0, len(cfg.Apps))
	for id := range cfg.Apps {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	fmt.Fprintf(output, "[ok] configuration: %s\n", path)
	for _, id := range ids {
		app := cfg.Apps[id]
		fmt.Fprintf(output, "[ok] app %s: %s -> 127.0.0.1:%d (pwd: %s)\n", id, doctorEndpoint(cfg, app), app.Port, app.Pwd)
	}
	fmt.Fprintf(output, "[ok] %d app(s) checked\n", len(ids))
	return nil
}

func doctorEndpoint(cfg config.RuntimeConfig, app config.RuntimeAppConfig) string {
	switch app.Protocol {
	case config.ProtocolTCP:
		return fmt.Sprintf("tcp://127.0.0.1:%d", app.ListenPort)
	case config.ProtocolGRPC:
		return fmt.Sprintf("grpc://%s:%d", app.Host, cfg.Port)
	default:
		if app.Host != "" {
			return fmt.Sprintf("http://%s:%d", app.Host, cfg.Port)
		}
		return fmt.Sprintf("http://127.0.0.1:%d%s", cfg.Port, app.Path)
	}
}

func runServer(path string) error {
	cfg, e := config.Load(path)
	if e != nil {
		return fmt.Errorf("load configuration: %w", e)
	}
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warning", "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	runner := proc.NewRunner(logger)
	hooks := lifecycle.New(cfg.StartUp, cfg.TearDown, runner, logger)
	sup := supervisor.New(cfg, runner, logger)
	drainer := appProxy.NewDrainHandler(appProxy.NewHandler(cfg, sup, logger))
	http2Server := &http2.Server{}
	handler := h2c.NewHandler(drainer, http2Server)
	server := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", cfg.Port), Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	if e := http2.ConfigureServer(server, http2Server); e != nil {
		return fmt.Errorf("configure HTTP/2 server: %w", e)
	}
	connections := appProxy.NewConnectionTracker()
	tcpServers := make([]*appProxy.TCPServer, 0)
	for _, app := range cfg.Apps {
		if app.Protocol == config.ProtocolTCP {
			tcpServers = append(tcpServers, appProxy.NewTCPServer(app, sup, logger))
		}
	}
	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cfg.StopTimeout+30*time.Second)
		defer cancel()
		_ = hooks.TearDown(cleanupCtx)
	}()
	if e := hooks.StartUp(signals); e != nil {
		if signals.Err() != nil {
			return nil
		}
		return e
	}
	listener, e := net.Listen("tcp", server.Addr)
	if e != nil {
		return e
	}
	errc := make(chan error, 1+len(tcpServers))
	go func() {
		logger.Info("listening", "address", server.Addr)
		errc <- server.Serve(connections.Track(listener))
	}()
	for _, tcpServer := range tcpServers {
		go func(s *appProxy.TCPServer) { errc <- s.ListenAndServe() }(tcpServer)
	}
	var serveErr error
	select {
	case e := <-errc:
		if !errors.Is(e, http.ErrServerClosed) && !errors.Is(e, appProxy.ErrTCPServerClosed) {
			serveErr = e
		}
	case <-signals.Done():
	}
	timeout := cfg.StopTimeout + 30*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if e := appProxy.DrainServer(ctx, server, drainer, connections); e != nil {
		logger.Warn("HTTP drain failed", "err", e)
	}
	for _, tcpServer := range tcpServers {
		if e := tcpServer.Shutdown(ctx); e != nil {
			logger.Error("TCP shutdown failed", "address", tcpServer.Addr(), "err", e)
		}
	}
	stopErr := sup.StopAll(ctx)
	return errors.Join(serveErr, stopErr)
}
