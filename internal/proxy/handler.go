package proxy

import (
	"errors"
	"local-apps/internal/config"
	"local-apps/internal/supervisor"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
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
		routes = append(routes, Route{ID: id, Path: a.Path})
		target := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(a.Port)}
		cfg := a
		p := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) { pr.SetURL(target); rewrite(pr, cfg.Path, cfg.IncludePrefix) }, ErrorHandler: func(w http.ResponseWriter, r *http.Request, e error) {
			l.Error("backend proxy failed", "service", cfg.ID, "err", e)
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
		}}
		h.proxies[id] = p
	}
	h.router = NewRouter(routes)
	return h
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route, ok := h.router.Match(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	release, e := h.supervisor.Acquire(r.Context(), route.ID)
	if e != nil {
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
	h.proxies[route.ID].ServeHTTP(w, r)
}
