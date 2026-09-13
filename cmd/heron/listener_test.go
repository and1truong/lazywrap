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

func TestListenLoopbacksFailsWhenIPv6PortIsOccupied(t *testing.T) {
	occupied, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback is unavailable: %v", err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	listeners, err := listenLoopbacks(port)
	if err == nil {
		for _, listener := range listeners {
			_ = listener.Close()
		}
		t.Fatal("listenLoopbacks succeeded with an occupied IPv6 port")
	}

	probe4, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatalf("IPv4 listener was not closed after IPv6 bind failure: %v", err)
	}
	_ = probe4.Close()
}
