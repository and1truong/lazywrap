package lifecycle

import (
	"context"
	"errors"
	"fmt"
	proc "github.com/and1truong/heron/internal/process"
	"log/slog"
	"sync"
)

// Hooks runs process-wide lifecycle commands at most once per phase.
type Hooks struct {
	startUp  []string
	tearDown []string
	runner   proc.ProcessRunner
	logger   *slog.Logger

	startOnce    sync.Once
	tearDownOnce sync.Once
	startErr     error
	tearDownErr  error
}

func New(startUp, tearDown []string, runner proc.ProcessRunner, logger *slog.Logger) *Hooks {
	return &Hooks{
		startUp:  append([]string(nil), startUp...),
		tearDown: append([]string(nil), tearDown...),
		runner:   runner,
		logger:   logger,
	}
}

func (h *Hooks) StartUp(ctx context.Context) error {
	h.startOnce.Do(func() {
		for i, command := range h.startUp {
			if err := h.runner.Run(ctx, commandSpec(command, "startUp")); err != nil {
				h.startErr = commandError("startUp", i, command, err)
				return
			}
		}
	})
	return h.startErr
}

// TearDown attempts every command and returns their joined errors. Failures are
// logged here so callers can continue shutdown without promoting them to a
// process-level failure.
func (h *Hooks) TearDown(ctx context.Context) error {
	h.tearDownOnce.Do(func() {
		var errs []error
		for i, command := range h.tearDown {
			if err := h.runner.Run(ctx, commandSpec(command, "tearDown")); err != nil {
				wrapped := commandError("tearDown", i, command, err)
				errs = append(errs, wrapped)
				if h.logger != nil {
					h.logger.Warn("tearDown command failed", "command_index", i+1, "command", command, "err", err)
				}
			}
		}
		h.tearDownErr = errors.Join(errs...)
	})
	return h.tearDownErr
}

func commandSpec(command, kind string) proc.CommandSpec {
	return proc.CommandSpec{Command: command, Service: "heron", Kind: kind}
}

func commandError(kind string, index int, command string, err error) error {
	return fmt.Errorf("%s command %d %q failed: %w", kind, index+1, command, err)
}
