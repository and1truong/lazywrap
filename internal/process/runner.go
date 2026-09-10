package process

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
)

type CommandSpec struct{ Command, Dir, Service, Kind string }
type ProcessResult struct{ Err error }
type Process struct {
	Cmd       *exec.Cmd
	Done      <-chan ProcessResult
	terminate func() error
	kill      func() error
}

func (p *Process) Terminate() error {
	if p == nil || p.terminate == nil {
		return nil
	}
	return p.terminate()
}
func (p *Process) Kill() error {
	if p == nil || p.kill == nil {
		return nil
	}
	return p.kill()
}

type ProcessRunner interface {
	Run(context.Context, CommandSpec) error
	Start(context.Context, CommandSpec) (*Process, error)
}
type Runner struct{ Logger *slog.Logger }

func NewRunner(l *slog.Logger) *Runner { return &Runner{Logger: l} }
func (r *Runner) command(ctx context.Context, s CommandSpec) *exec.Cmd {
	c := exec.CommandContext(ctx, "/bin/sh", "-c", s.Command)
	c.Dir = s.Dir
	configureProcess(c)
	return c
}
func (r *Runner) Run(ctx context.Context, s CommandSpec) error {
	p, e := r.Start(ctx, s)
	if e != nil {
		return e
	}
	return (<-p.Done).Err
}
func (r *Runner) Start(ctx context.Context, s CommandSpec) (*Process, error) {
	c := r.command(ctx, s)
	out, e := c.StdoutPipe()
	if e != nil {
		return nil, e
	}
	errout, e := c.StderrPipe()
	if e != nil {
		return nil, e
	}
	if e = c.Start(); e != nil {
		return nil, fmt.Errorf("start %s: %w", s.Kind, e)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go r.logStream(&wg, out, s, "stdout", slog.LevelInfo)
	go r.logStream(&wg, errout, s, "stderr", slog.LevelWarn)
	d := make(chan ProcessResult, 1)
	go func() { e := c.Wait(); wg.Wait(); d <- ProcessResult{Err: e}; close(d) }()
	return &Process{Cmd: c, Done: d, terminate: func() error { return terminateProcess(c) }, kill: func() error { return killProcess(c) }}, nil
}
func (r *Runner) logStream(wg *sync.WaitGroup, rd io.Reader, s CommandSpec, stream string, level slog.Level) {
	defer wg.Done()
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		r.Logger.Log(context.Background(), level, sc.Text(), "service", s.Service, "stream", stream, "kind", s.Kind)
	}
	if e := sc.Err(); e != nil {
		r.Logger.Warn("reading child output", "service", s.Service, "stream", stream, "err", e)
	}
}
