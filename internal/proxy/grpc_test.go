package proxy

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lazywrap/internal/config"
	"lazywrap/internal/supervisor"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	reflectionpb "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const grpcTestServiceName = "lazywrap.test.Echo"

type grpcTestService struct {
	unaryCalls atomic.Int32
}

type grpcTestServiceServer interface{}

func (s *grpcTestService) unary(ctx context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	s.unaryCalls.Add(1)
	if in.Value == "error" {
		withDetails, err := status.New(codes.InvalidArgument, "invalid input").WithDetails(wrapperspb.String("validation detail"))
		if err != nil {
			return nil, err
		}
		return nil, withDetails.Err()
	}
	md, _ := metadata.FromIncomingContext(ctx)
	if err := grpc.SendHeader(ctx, metadata.Pairs("x-upstream-header", "received")); err != nil {
		return nil, err
	}
	grpc.SetTrailer(ctx, metadata.Pairs("x-upstream-trailer", "complete"))
	return wrapperspb.String(in.Value + ":" + strings.Join(md.Get("x-test"), ",")), nil
}

func grpcUnaryHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(wrapperspb.StringValue)
	if err := dec(in); err != nil {
		return nil, err
	}
	invoke := func(ctx context.Context, req any) (any, error) {
		return srv.(*grpcTestService).unary(ctx, req.(*wrapperspb.StringValue))
	}
	if interceptor == nil {
		return invoke(ctx, in)
	}
	return interceptor(ctx, in, &grpc.UnaryServerInfo{Server: srv, FullMethod: "/" + grpcTestServiceName + "/Unary"}, invoke)
}

func grpcServerStreamHandler(_ any, stream grpc.ServerStream) error {
	in := new(wrapperspb.StringValue)
	if err := stream.RecvMsg(in); err != nil {
		return err
	}
	for _, suffix := range []string{"-1", "-2"} {
		if err := stream.SendMsg(wrapperspb.String(in.Value + suffix)); err != nil {
			return err
		}
	}
	return nil
}

func grpcClientStreamHandler(_ any, stream grpc.ServerStream) error {
	values := make([]string, 0)
	for {
		in := new(wrapperspb.StringValue)
		err := stream.RecvMsg(in)
		if err == io.EOF {
			return stream.SendMsg(wrapperspb.String(strings.Join(values, "+")))
		}
		if err != nil {
			return err
		}
		values = append(values, in.Value)
	}
}

func grpcBidiHandler(_ any, stream grpc.ServerStream) error {
	for {
		in := new(wrapperspb.StringValue)
		err := stream.RecvMsg(in)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := stream.SendMsg(wrapperspb.String("echo:" + in.Value)); err != nil {
			return err
		}
	}
}

var grpcTestServiceDesc = grpc.ServiceDesc{
	ServiceName: grpcTestServiceName,
	HandlerType: (*grpcTestServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{MethodName: "Unary", Handler: grpcUnaryHandler},
	},
	Streams: []grpc.StreamDesc{
		{StreamName: "ServerStream", Handler: grpcServerStreamHandler, ServerStreams: true},
		{StreamName: "ClientStream", Handler: grpcClientStreamHandler, ClientStreams: true},
		{StreamName: "Bidi", Handler: grpcBidiHandler, ServerStreams: true, ClientStreams: true},
	},
}

func newGRPCTestServer(listener net.Listener) (*grpc.Server, *grpcTestService) {
	service := &grpcTestService{}
	server := grpc.NewServer()
	server.RegisterService(&grpcTestServiceDesc, service)
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(server, healthServer)
	reflection.Register(server)
	return server, service
}

func startGRPCTestBackend(t *testing.T) (int, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server, _ := newGRPCTestServer(listener)
	go func() {
		_ = server.Serve(listener)
	}()
	return listener.Addr().(*net.TCPAddr).Port, func() {
		server.Stop()
		_ = listener.Close()
	}
}

func startGRPCProxy(t *testing.T, backendPort int, idle, startTimeout time.Duration) (*grpc.ClientConn, *lifecycleRunner, func()) {
	t.Helper()
	runner := newLifecycleRunner()
	cfg := config.RuntimeConfig{Apps: map[string]config.RuntimeAppConfig{
		"echo": {
			ID: "echo", Pwd: t.TempDir(), Launch: "start", Stop: "stop",
			Protocol: config.ProtocolGRPC, Host: "echo.localhost", Port: backendPort,
			Idle: idle, StartTimeout: startTimeout, StopTimeout: 20 * time.Millisecond,
			GRPCHealth: true,
		},
	}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewHandler(cfg, supervisor.New(cfg, runner, logger), logger)
	server := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
	target := strings.TrimPrefix(server.URL, "http://")
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithAuthority("echo.localhost"),
	)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return conn, runner, func() {
		_ = conn.Close()
		server.Close()
	}
}

func TestGRPCProxyPreservesUnaryStreamingMetadataAndStatus(t *testing.T) {
	backendPort, stopBackend := startGRPCTestBackend(t)
	defer stopBackend()
	conn, runner, stopProxy := startGRPCProxy(t, backendPort, time.Second, time.Second)
	defer stopProxy()

	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-test", "metadata")
	var response wrapperspb.StringValue
	var header, trailer metadata.MD
	if err := conn.Invoke(ctx, "/"+grpcTestServiceName+"/Unary", wrapperspb.String("hello"), &response, grpc.Header(&header), grpc.Trailer(&trailer)); err != nil {
		t.Fatal(err)
	}
	if response.Value != "hello:metadata" || header.Get("x-upstream-header")[0] != "received" || trailer.Get("x-upstream-trailer")[0] != "complete" {
		t.Fatalf("unary response=%q header=%v trailer=%v", response.Value, header, trailer)
	}

	serverStream, err := conn.NewStream(context.Background(), &grpcTestServiceDesc.Streams[0], "/"+grpcTestServiceName+"/ServerStream")
	if err != nil {
		t.Fatal(err)
	}
	if err := serverStream.SendMsg(wrapperspb.String("item")); err != nil {
		t.Fatal(err)
	}
	if err := serverStream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"item-1", "item-2"} {
		out := new(wrapperspb.StringValue)
		if err := serverStream.RecvMsg(out); err != nil || out.Value != want {
			t.Fatalf("server stream message %d = %q, %v; want %q", i, out.Value, err, want)
		}
	}
	if err := serverStream.RecvMsg(new(wrapperspb.StringValue)); err != io.EOF {
		t.Fatalf("server stream final error = %v, want EOF", err)
	}

	clientStream, err := conn.NewStream(context.Background(), &grpcTestServiceDesc.Streams[1], "/"+grpcTestServiceName+"/ClientStream")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"a", "b"} {
		if err := clientStream.SendMsg(wrapperspb.String(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := clientStream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	clientResponse := new(wrapperspb.StringValue)
	if err := clientStream.RecvMsg(clientResponse); err != nil || clientResponse.Value != "a+b" {
		t.Fatalf("client stream response = %q, %v", clientResponse.Value, err)
	}

	err = conn.Invoke(context.Background(), "/"+grpcTestServiceName+"/Unary", wrapperspb.String("error"), new(wrapperspb.StringValue))
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status code = %v, want %v", status.Code(err), codes.InvalidArgument)
	}
	details := status.Convert(err).Details()
	if len(details) != 1 || details[0].(*wrapperspb.StringValue).Value != "validation detail" {
		t.Fatalf("status details = %#v", details)
	}
	if starts, _ := runner.counts("echo"); starts != 1 {
		t.Fatalf("starts = %d, want 1", starts)
	}

	reflectionStream, err := reflectionpb.NewServerReflectionClient(conn).ServerReflectionInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := reflectionStream.Send(&reflectionpb.ServerReflectionRequest{
		MessageRequest: &reflectionpb.ServerReflectionRequest_ListServices{ListServices: ""},
	}); err != nil {
		t.Fatal(err)
	}
	reflectionResponse, err := reflectionStream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, service := range reflectionResponse.GetListServicesResponse().GetService() {
		if service.GetName() == grpcTestServiceName {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("reflection services = %#v; missing %s", reflectionResponse.GetListServicesResponse(), grpcTestServiceName)
	}
	if err := reflectionStream.CloseSend(); err != nil {
		t.Fatal(err)
	}
}

func TestGRPCOpenStreamPreventsIdleShutdown(t *testing.T) {
	backendPort, stopBackend := startGRPCTestBackend(t)
	defer stopBackend()
	idle := 40 * time.Millisecond
	conn, runner, stopProxy := startGRPCProxy(t, backendPort, idle, time.Second)
	defer stopProxy()

	stream, err := conn.NewStream(context.Background(), &grpcTestServiceDesc.Streams[2], "/"+grpcTestServiceName+"/Bidi")
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.SendMsg(wrapperspb.String("live")); err != nil {
		t.Fatal(err)
	}
	out := new(wrapperspb.StringValue)
	if err := stream.RecvMsg(out); err != nil || out.Value != "echo:live" {
		t.Fatalf("bidi response = %q, %v", out.Value, err)
	}
	time.Sleep(3 * idle)
	if _, stops := runner.counts("echo"); stops != 0 {
		t.Fatalf("stops with open stream = %d, want 0", stops)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if err := stream.RecvMsg(new(wrapperspb.StringValue)); err != io.EOF {
		t.Fatalf("bidi final error = %v, want EOF", err)
	}

	deadline := time.Now().Add(time.Second)
	for {
		if _, stops := runner.counts("echo"); stops == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("service was not stopped after stream closed")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestGRPCStartupTimeoutReturnsGRPCStatus(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	conn, _, stopProxy := startGRPCProxy(t, port, time.Second, 40*time.Millisecond)
	defer stopProxy()
	err = conn.Invoke(context.Background(), "/"+grpcTestServiceName+"/Unary", wrapperspb.String("hello"), new(wrapperspb.StringValue))
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("status code = %v, want %v: %v", status.Code(err), codes.DeadlineExceeded, err)
	}
}

func TestGRPCCancellationDuringStartupDoesNotForwardAbandonedRPC(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server, service := newGRPCTestServer(listener)
	serveDone := make(chan struct{})
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = server.Serve(listener)
		close(serveDone)
	}()
	defer func() {
		server.Stop()
		_ = listener.Close()
		<-serveDone
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	conn, runner, stopProxy := startGRPCProxy(t, port, time.Second, time.Second)
	defer stopProxy()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	err = conn.Invoke(ctx, "/"+grpcTestServiceName+"/Unary", wrapperspb.String("abandoned"), new(wrapperspb.StringValue))
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("status code = %v, want %v: %v", status.Code(err), codes.DeadlineExceeded, err)
	}

	var response wrapperspb.StringValue
	if err := conn.Invoke(context.Background(), "/"+grpcTestServiceName+"/Unary", wrapperspb.String("live"), &response); err != nil {
		t.Fatal(err)
	}
	if response.Value != "live:" {
		t.Fatalf("response = %q, want live:", response.Value)
	}
	if calls := service.unaryCalls.Load(); calls != 1 {
		t.Fatalf("upstream unary calls = %d, want only the live request", calls)
	}
	if starts, _ := runner.counts("echo"); starts != 1 {
		t.Fatalf("starts = %d, want 1", starts)
	}
}
