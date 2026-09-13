package main

import (
	"net"
	"testing"
)

func TestListenLoopbacksBindsIPv4AndIPv6WhenAvailable(t *testing.T) {
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	listeners, err := listenLoopbacks(port)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()

	var ipv4, ipv6 bool
	for _, listener := range listeners {
		address := listener.Addr().(*net.TCPAddr)
		if address.IP.To4() != nil {
			ipv4 = true
		} else if address.IP.Equal(net.IPv6loopback) {
			ipv6 = true
		}
	}
	if !ipv4 {
		t.Fatal("IPv4 loopback listener is missing")
	}
	probe6, err := net.Listen("tcp6", "[::1]:0")
	if err == nil {
		_ = probe6.Close()
		if !ipv6 {
			t.Fatal("IPv6 is available but the loopback listener is missing")
		}
	}
}
