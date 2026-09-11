package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDrainHandlerWaitsForAdmittedRequestsAndRejectsNewWork(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	handler := NewDrainHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))

	firstDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://wrapper/", nil))
		close(firstDone)
	}()
	<-started
	drained := handler.BeginDrain()

	select {
	case <-drained:
		t.Fatal("drain completed while an admitted request was active")
	default:
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://wrapper/", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("new request status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}

	grpcResponse := httptest.NewRecorder()
	grpcRequest := httptest.NewRequest(http.MethodPost, "http://wrapper/test.Service/Method", nil)
	grpcRequest.Header.Set("Content-Type", "application/grpc+proto")
	handler.ServeHTTP(grpcResponse, grpcRequest)
	if grpcResponse.Code != http.StatusOK || grpcResponse.Header().Get("Grpc-Status") != "14" {
		t.Fatalf("new gRPC response status=%d headers=%v", grpcResponse.Code, grpcResponse.Header())
	}

	close(release)
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("drain did not complete after the active request exited")
	}
	<-firstDone
}

func TestConnectionTrackerClosesAcceptedConnections(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	tracker := NewConnectionTracker()
	trackedListener := tracker.Track(listener)
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := trackedListener.Accept()
		accepted <- conn
	}()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	serverConn := <-accepted
	if serverConn == nil {
		t.Fatal("connection was not accepted")
	}
	if err := tracker.CloseAll(); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("client read error = %v, want EOF", err)
	}
	secondClient, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer secondClient.Close()
	if _, err := trackedListener.Accept(); err == nil {
		t.Fatal("tracker accepted a connection after CloseAll")
	}
}

func TestDrainServerClosesConnectionsAfterActiveHandlerCompletes(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	drainer := NewDrainHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	server := httptest.NewUnstartedServer(drainer)
	tracker := NewConnectionTracker()
	server.Listener = tracker.Track(server.Listener)
	server.Start()
	defer server.Close()

	requestDone := make(chan error, 1)
	go func() {
		response, err := http.Get(server.URL)
		if err == nil {
			_ = response.Body.Close()
		}
		requestDone <- err
	}()
	<-started

	drainDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		drainDone <- DrainServer(ctx, server.Config, drainer, tracker)
	}()
	select {
	case err := <-drainDone:
		t.Fatalf("DrainServer returned with active handler: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-drainDone; err != nil {
		t.Fatal(err)
	}
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
}
