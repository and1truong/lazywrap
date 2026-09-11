package proxy

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"lazywrap/internal/config"
	"lazywrap/internal/supervisor"
)

func TestTCPProxyLifecycle(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		for {
			conn, err := backend.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	frontend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	runner := newLifecycleRunner()
	backendPort := backend.Addr().(*net.TCPAddr).Port
	app := config.RuntimeAppConfig{
		ID: "db", Protocol: config.ProtocolTCP, Pwd: t.TempDir(), Launch: "start", Stop: "stop",
		Port: backendPort, ListenPort: frontend.Addr().(*net.TCPAddr).Port,
		Idle: 25 * time.Millisecond, StartTimeout: time.Second, StopTimeout: time.Second,
	}
	cfg := config.RuntimeConfig{Apps: map[string]config.RuntimeAppConfig{"db": app}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup := supervisor.New(cfg, runner, logger)
	server := NewTCPServer(app, sup, logger)
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(frontend) }()

	client, err := net.Dial("tcp", frontend.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(client, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "ping" {
		t.Fatalf("response = %q", response)
	}

	time.Sleep(50 * time.Millisecond)
	if starts, stops := runner.counts("db"); starts != 1 || stops != 0 {
		t.Fatalf("while connected: starts=%d stops=%d", starts, stops)
	}
	_ = client.Close()

	deadline := time.Now().Add(time.Second)
	for {
		_, stops := runner.counts("db")
		if stops == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("service did not stop after TCP connection became idle")
		}
		time.Sleep(5 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serveResult; err != ErrTCPServerClosed {
		t.Fatalf("Serve() error = %v", err)
	}
}
