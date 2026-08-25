package main

import "testing"

func TestIsLoopback(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8787": true,
		"[::1]:8787":     true,
		"localhost:8787": true,
		// Regression: net.SplitHostPort(":8787") returns an empty host with no
		// error, and http.Server binds that to every interface. Treating it as
		// loopback silenced the SECURITY.md §4 warning in the single most common
		// way to expose a Go server.
		":8787":          false,
		"0.0.0.0:8787":   false,
		"[::]:8787":      false,
		"192.168.1.5:80": false,
		"not-an-address": false,
	}
	for addr, want := range cases {
		if got := isLoopback(addr); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}
