package proxy

import (
	"net/http/httputil"
	"strings"
)

func rewrite(pr *httputil.ProxyRequest, prefix string, include bool) {
	pr.SetXForwarded()
	if include {
		return
	}
	p := strings.TrimPrefix(pr.Out.URL.Path, prefix)
	if p == "" || p == "/" {
		p = "/"
	}
	pr.Out.URL.Path = p
	if pr.Out.URL.RawPath != "" {
		rp := strings.TrimPrefix(pr.Out.URL.RawPath, prefix)
		if rp == "" || rp == "/" {
			rp = "/"
		}
		pr.Out.URL.RawPath = rp
	}
}
