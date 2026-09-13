package proxy

import (
	"context"
	"io"
	"lazywrap/internal/config"
	proc "lazywrap/internal/process"
	"lazywrap/internal/supervisor"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRouter(t *testing.T) {
	r := NewRouter([]Route{
		{ID: "foo", Host: "foo.localhost"},
		{ID: "base", Path: "/service"},
		{ID: "admin", Path: "/service/admin"},
	})
	for _, tc := range []struct {
		host, path, id string
		ok             bool
	}{
		{"foo.localhost", "/", "foo", true},
		{"FOO.LOCALHOST", "/", "foo", true},
		{"foo.localhost:3000", "/", "foo", true},
		{"bar.localhost", "/", "", false},
		{"bar.foo.localhost", "/", "", false},
		{"localhost", "/hello", "", false},
		{"foo.localhost", "/service/admin", "foo", true},
		{"localhost", "/service", "base", true},
		{"localhost", "/service/x", "base", true},
		{"localhost", "/service/admin/x", "admin", true},
		{"localhost", "/serviceable", "", false},
		{"localhost", "/unknown", "", false},
	} {
		got, ok := r.Match(tc.host, tc.path)
		if ok != tc.ok || got.ID != tc.id {
			t.Errorf("Match(%q, %q) = %#v,%v", tc.host, tc.path, got, ok)
		}
	}
}

func TestRouterMatchesDefaultHostFromNormalizedConfig(t *testing.T) {
	cfg, err := (config.Config{Apps: map[string]config.AppConfig{
		"api": {Pwd: t.TempDir(), Launch: "server", Port: 1980},
	}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	app := cfg.Apps["api"]
	router := NewRouter([]Route{{ID: app.ID, Path: app.Path, Host: app.Host}})

	got, ok := router.Match("api.localhost:3000", "/health")
	if !ok || got.ID != "api" {
		t.Fatalf("Match() = %#v, %v; want api route", got, ok)
	}
}

type lifecycleRunner struct {
	mu     sync.Mutex
	starts map[string]int
	stops  map[string]int
	done   map[string]chan struct{}
}

func newLifecycleRunner() *lifecycleRunner {
	return &lifecycleRunner{starts: map[string]int{}, stops: map[string]int{}, done: map[string]chan struct{}{}}
}

func (r *lifecycleRunner) Run(_ context.Context, spec proc.CommandSpec) error {
	if spec.Kind != "stop" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stops[spec.Service]++
	if done := r.done[spec.Service]; done != nil {
		close(done)
		r.done[spec.Service] = nil
	}
	return nil
}

func (r *lifecycleRunner) Start(_ context.Context, spec proc.CommandSpec) (*proc.Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts[spec.Service]++
	done := make(chan struct{})
	r.done[spec.Service] = done
	return &proc.Process{Done: done}, nil
}

func (r *lifecycleRunner) counts(id string) (starts, stops int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts[id], r.stops[id]
}

type backendRequest struct {
	path, query, method, body, host, header     string
	forwardedHost, forwardedFor, forwardedProto string
}

func backendPort(t *testing.T, rawURL string) int {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	result, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestHandlerHostRoutingLifecycleAndPathCoexistence(t *testing.T) {
	var mu sync.Mutex
	requests := map[string][]backendRequest{}
	newBackend := func(id string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			requests[id] = append(requests[id], backendRequest{
				path: r.URL.Path, query: r.URL.RawQuery, method: r.Method, body: string(body), host: r.Host, header: r.Header.Get("X-Test"),
				forwardedHost: r.Header.Get("X-Forwarded-Host"), forwardedFor: r.Header.Get("X-Forwarded-For"), forwardedProto: r.Header.Get("X-Forwarded-Proto"),
			})
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		}))
	}
	fooBackend, barBackend, legacyBackend := newBackend("foo"), newBackend("bar"), newBackend("legacy")
	defer fooBackend.Close()
	defer barBackend.Close()
	defer legacyBackend.Close()

	runner := newLifecycleRunner()
	cfg := config.RuntimeConfig{Apps: map[string]config.RuntimeAppConfig{
		"foo":    {ID: "foo", Pwd: t.TempDir(), Launch: "start", Stop: "stop", Host: "foo.localhost", Port: backendPort(t, fooBackend.URL), Idle: 25 * time.Millisecond, StartTimeout: time.Second, StopTimeout: time.Second},
		"bar":    {ID: "bar", Pwd: t.TempDir(), Launch: "start", Stop: "stop", Host: "bar.localhost", Port: backendPort(t, barBackend.URL), Idle: 25 * time.Millisecond, StartTimeout: time.Second, StopTimeout: time.Second},
		"legacy": {ID: "legacy", Pwd: t.TempDir(), Launch: "start", Stop: "stop", Path: "/service/legacy", Port: backendPort(t, legacyBackend.URL), Idle: 25 * time.Millisecond, StartTimeout: time.Second, StopTimeout: time.Second},
	}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewHandler(cfg, supervisor.New(cfg, runner, logger), logger)

	request := func(host, method, path, body string) error {
		r := httptest.NewRequest(method, "http://wrapper"+path, strings.NewReader(body))
		r.Host = host
		r.Header.Set("X-Test", "preserved")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent {
			return &requestError{status: w.Code}
		}
		return nil
	}

	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- request("FOO.LOCALHOST:3000", http.MethodPost, "/foo?q=1", "payload")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := request("bar.localhost:3000", http.MethodGet, "/bar?q=2", ""); err != nil {
		t.Fatal(err)
	}
	if err := request("localhost:3000", http.MethodGet, "/service/legacy/item?q=3", ""); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	fooRequests, barRequests, legacyRequests := requests["foo"], requests["bar"], requests["legacy"]
	mu.Unlock()
	if len(fooRequests) != 10 {
		t.Fatalf("foo requests = %d, want 10", len(fooRequests))
	}
	for _, got := range fooRequests {
		if got.path != "/foo" || got.query != "q=1" || got.method != http.MethodPost || got.body != "payload" || got.header != "preserved" {
			t.Fatalf("foo request = %#v", got)
		}
		if got.host != "127.0.0.1:"+strconv.Itoa(backendPort(t, fooBackend.URL)) {
			t.Fatalf("backend Host = %q", got.host)
		}
		if got.forwardedHost != "FOO.LOCALHOST:3000" || got.forwardedFor != "192.0.2.1" || got.forwardedProto != "http" {
			t.Fatalf("forwarding headers = %#v", got)
		}
	}
	if len(barRequests) != 1 || barRequests[0].path != "/bar" || barRequests[0].query != "q=2" {
		t.Fatalf("bar requests = %#v", barRequests)
	}
	if len(legacyRequests) != 1 || legacyRequests[0].path != "/item" || legacyRequests[0].query != "q=3" {
		t.Fatalf("legacy requests = %#v", legacyRequests)
	}
	if starts, _ := runner.counts("foo"); starts != 1 {
		t.Fatalf("foo starts = %d, want 1", starts)
	}

	deadline := time.Now().Add(time.Second)
	for {
		_, fooStops := runner.counts("foo")
		_, barStops := runner.counts("bar")
		_, legacyStops := runner.counts("legacy")
		if fooStops == 1 && barStops == 1 && legacyStops == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("idle shutdown counts: foo=%d bar=%d legacy=%d", fooStops, barStops, legacyStops)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHandlerRoutesNamedEndpointsAndPrimaryAliasWithSingleStartup(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("web"))
	}))
	defer web.Close()
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("metrics"))
	}))
	defer metrics.Close()

	runner := newLifecycleRunner()
	cfg := config.RuntimeConfig{Apps: map[string]config.RuntimeAppConfig{
		"foo": {
			ID: "foo", Pwd: t.TempDir(), Launch: "start", Stop: "stop",
			Idle: time.Hour, StartTimeout: time.Second, StopTimeout: time.Second,
			Endpoints: map[string]config.RuntimeEndpointConfig{
				"web":     {Name: "web", Protocol: config.ProtocolHTTP, Host: "foo.localhost", Aliases: []string{"web.foo.localhost"}, Port: backendPort(t, web.URL), Primary: true},
				"metrics": {Name: "metrics", Protocol: config.ProtocolHTTP, Host: "metrics.foo.localhost", Port: backendPort(t, metrics.URL)},
			},
		},
	}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewHandler(cfg, supervisor.New(cfg, runner, logger), logger)

	request := func(host string) string {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "http://wrapper/", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d", host, w.Code)
		}
		return w.Body.String()
	}
	if got := request("foo.localhost"); got != "web" {
		t.Fatalf("primary response = %q", got)
	}
	if got := request("web.foo.localhost"); got != "web" {
		t.Fatalf("primary alias response = %q", got)
	}
	if got := request("metrics.foo.localhost"); got != "metrics" {
		t.Fatalf("metrics response = %q", got)
	}
	if starts, _ := runner.counts("foo"); starts != 1 {
		t.Fatalf("starts = %d, want 1", starts)
	}
}

type requestError struct{ status int }

func (e *requestError) Error() string { return http.StatusText(e.status) }

func TestRewrite(t *testing.T) {
	for _, tc := range []struct {
		path    string
		include bool
		want    string
	}{{"/service/a", false, "/"}, {"/service/a/", false, "/"}, {"/service/a/foo", false, "/foo"}, {"/service/a/foo", true, "/service/a/foo"}} {
		t.Run(tc.want, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://front"+tc.path+"?q=x", nil)
			out := r.Clone(r.Context())
			pr := &httputil.ProxyRequest{In: r, Out: out}
			target, _ := url.Parse("http://127.0.0.1:1980")
			pr.SetURL(target)
			rewrite(pr, "/service/a", tc.include)
			if out.URL.Path != tc.want || out.URL.RawQuery != "q=x" {
				t.Fatalf("got %s?%s", out.URL.Path, out.URL.RawQuery)
			}
		})
	}
}
