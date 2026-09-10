package proxy

import "sort"

type Route struct{ ID, Path string }
type Router struct{ routes []Route }

func NewRouter(routes []Route) *Router {
	r := append([]Route(nil), routes...)
	sort.Slice(r, func(i, j int) bool { return len(r[i].Path) > len(r[j].Path) })
	return &Router{routes: r}
}
func (r *Router) Match(path string) (Route, bool) {
	for _, v := range r.routes {
		if v.Path == "/" || path == v.Path || (len(path) > len(v.Path) && path[:len(v.Path)] == v.Path && path[len(v.Path)] == '/') {
			return v, true
		}
	}
	return Route{}, false
}
