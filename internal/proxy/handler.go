package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"github.com/and1truong/heron/internal/config"
	"github.com/and1truong/heron/internal/supervisor"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/http2"
	"google.golang.org/grpc/codes"
)

type Handler struct {
	router     *Router
	supervisor *supervisor.Supervisor
	proxies    map[string]*httputil.ReverseProxy
	logger     *slog.Logger
}

func NewHandler(c config.RuntimeConfig, s *supervisor.Supervisor, l *slog.Logger) *Handler {
	h := &Handler{supervisor: s, proxies: map[string]*httputil.ReverseProxy{}, logger: l}
	routes := make([]Route, 0, len(c.Apps))
	for id, a := range c.Apps {
		for _, endpoint := range a.EndpointList() {
			if endpoint.Protocol == config.ProtocolTCP {
				continue
			}
			route := Route{ID: id, Endpoint: endpoint.Name, Path: endpoint.Path, Host: endpoint.Host, Protocol: endpoint.Protocol}
			routes = append(routes, route)
			for _, alias := range endpoint.Aliases {
				routes = append(routes, Route{ID: id, Endpoint: endpoint.Name, Host: alias, Protocol: endpoint.Protocol})
			}
			target := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(endpoint.Port)}
			cfg := endpoint
			p := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(target)
				pr.SetXForwarded()
				// Local backends receive their own address as Host, rather than the
				// wrapper's routing host. The original host remains in X-Forwarded-Host.
				pr.Out.Host = target.Host
				if cfg.Host == "" {
					rewrite(pr, cfg.Path, cfg.IncludePrefix)
				}
			}, ErrorHandler: func(w http.ResponseWriter, r *http.Request, e error) {
				l.Error("backend proxy failed", "service", id, "endpoint", cfg.Name, "err", e)
				if cfg.Protocol == config.ProtocolGRPC {
					writeGRPCError(w, grpcCode(r.Context(), e), "backend unavailable")
					return
				}
				http.Error(w, "Bad Gateway", http.StatusBadGateway)
			}}
			if endpoint.Protocol == config.ProtocolGRPC {
				dialer := &net.Dialer{}
				p.Transport = &http2.Transport{
					AllowHTTP: true,
					DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
						return dialer.DialContext(ctx, network, addr)
					},
				}
			}
			h.proxies[routeKey(id, endpoint.Name)] = p
		}
	}
	h.router = NewRouter(routes)
	return h
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route, ok := h.router.Match(r.Host, r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	release, e := h.supervisor.Acquire(r.Context(), route.ID)
	if e != nil {
		if route.Protocol == config.ProtocolGRPC {
			if r.Context().Err() == nil && !errors.Is(e, r.Context().Err()) {
				h.logger.Error("service unavailable", "service", route.ID, "err", e)
			}
			writeGRPCError(w, grpcCode(r.Context(), e), grpcMessage(r.Context(), e))
			return
		}
		status := http.StatusServiceUnavailable
		if strings.Contains(e.Error(), "readiness timeout") {
			status = http.StatusGatewayTimeout
		}
		if r.Context().Err() == nil && !errors.Is(e, r.Context().Err()) {
			h.logger.Error("service unavailable", "service", route.ID, "err", e)
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	defer release()
	h.logger.Debug("proxy request", "service", route.ID, "method", r.Method, "path", r.URL.Path)
	h.proxies[routeKey(route.ID, route.Endpoint)].ServeHTTP(w, r)
}

func routeKey(id, endpoint string) string { return id + "\x00" + endpoint }

func grpcCode(ctx context.Context, err error) codes.Code {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return codes.Canceled
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "readiness timeout") {
		return codes.DeadlineExceeded
	}
	return codes.Unavailable
}

func grpcMessage(ctx context.Context, err error) string {
	switch grpcCode(ctx, err) {
	case codes.Canceled:
		return "request canceled"
	case codes.DeadlineExceeded:
		return "service startup timed out"
	default:
		return "service unavailable"
	}
}

func writeGRPCError(w http.ResponseWriter, code codes.Code, message string) {
	w.Header().Set("Content-Type", "application/grpc")
	w.Header().Set("Grpc-Status", strconv.Itoa(int(code)))
	w.Header().Set("Grpc-Message", url.PathEscape(message))
	w.WriteHeader(http.StatusOK)
}
