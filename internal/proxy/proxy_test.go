package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
)

func TestRouter(t *testing.T) {
	r := NewRouter([]Route{{ID: "base", Path: "/service"}, {ID: "admin", Path: "/service/admin"}})
	for _, tc := range []struct {
		path, id string
		ok       bool
	}{{"/service", "base", true}, {"/service/x", "base", true}, {"/service/admin/x", "admin", true}, {"/serviceable", "", false}, {"/unknown", "", false}} {
		got, ok := r.Match(tc.path)
		if ok != tc.ok || got.ID != tc.id {
			t.Errorf("Match(%q) = %#v,%v", tc.path, got, ok)
		}
	}
}

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
