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
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
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
	server := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", cfg.Port), Handler: appProxy.NewHandler(cfg, sup, logger), ReadHeaderTimeout: 10 * time.Second}
	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { logger.Info("listening", "address", server.Addr); errc <- server.ListenAndServe() }()
	select {
	case e := <-errc:
		if !errors.Is(e, http.ErrServerClosed) {
			return e
		}
	case <-signals.Done():
	}
	timeout := cfg.StopTimeout + 30*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if e := server.Shutdown(ctx); e != nil {
		logger.Error("HTTP shutdown failed", "err", e)
	}
	return sup.StopAll(ctx)
}
