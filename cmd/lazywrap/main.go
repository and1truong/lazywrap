package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"lazywrap/internal/config"
	proc "lazywrap/internal/process"
	appProxy "lazywrap/internal/proxy"
	"lazywrap/internal/supervisor"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
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
	def, e := config.DefaultPath()
	if e != nil {
		return e
	}
	path := flag.String("c", def, "configuration file")
	flag.Parse()
	cfg, e := config.Load(*path)
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
	sup := supervisor.New(cfg, proc.NewRunner(logger), logger)
	drainer := appProxy.NewDrainHandler(appProxy.NewHandler(cfg, sup, logger))
	http2Server := &http2.Server{}
	handler := h2c.NewHandler(drainer, http2Server)
	server := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", cfg.Port), Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	if e := http2.ConfigureServer(server, http2Server); e != nil {
		return fmt.Errorf("configure HTTP/2 server: %w", e)
	}
	listener, e := net.Listen("tcp", server.Addr)
	if e != nil {
		return e
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
	if e := sup.StopAll(ctx); e != nil {
		return e
	}
	return serveErr
}
