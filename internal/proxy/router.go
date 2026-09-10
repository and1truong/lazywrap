package proxy

import (
	"net"
	"sort"
	"strings"
)

type Route struct{ ID, Path, Host string }
type Router struct {
	hosts map[string]Route
	paths []Route
}

func NewRouter(routes []Route) *Router {
	r := &Router{hosts: make(map[string]Route), paths: make([]Route, 0, len(routes))}
	for _, route := range routes {
		if route.Host != "" {
			r.hosts[strings.ToLower(route.Host)] = route
			continue
		}
		r.paths = append(r.paths, route)
	}
	sort.Slice(r.paths, func(i, j int) bool { return len(r.paths[i].Path) > len(r.paths[j].Path) })
	return r
}

// Match resolves exact host routes before falling back to longest-prefix path routes.
func (r *Router) Match(host, path string) (Route, bool) {
	if route, ok := r.hosts[requestHostname(host)]; ok {
		return route, true
	}
	for _, route := range r.paths {
		if route.Path == "/" || path == route.Path || (len(path) > len(route.Path) && path[:len(route.Path)] == route.Path && path[len(route.Path)] == '/') {
			return route, true
		}
	}
	return Route{}, false
}

// requestHostname removes an optional HTTP port before case-insensitive lookup.
func requestHostname(host string) string {
	host = strings.TrimSpace(host)
	if hostname, _, err := net.SplitHostPort(host); err == nil {
		host = hostname
	}
	return strings.ToLower(host)
}
