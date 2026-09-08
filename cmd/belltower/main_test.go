package main

import "testing"

// A server bound to ":8088" or "0.0.0.0:8088" has to be probed over loopback.
func TestHealthTarget(t *testing.T) {
	tests := map[string]string{
		":8088":            "127.0.0.1:8088",
		"0.0.0.0:8088":     "127.0.0.1:8088",
		"127.0.0.1:8088":   "127.0.0.1:8088",
		"192.168.1.5:9000": "192.168.1.5:9000",
		"localhost:8088":   "localhost:8088",
		"localhost":        "localhost:8088",
		":":                "127.0.0.1:8088",
	}
	for in, want := range tests {
		if got := healthTarget(in); got != want {
			t.Errorf("healthTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

// The root --addr is a local flag and is not inherited by subcommands, so
// healthcheck must declare its own or the probe fails with "unknown flag".
func TestHealthcheckAcceptsAddrFlag(t *testing.T) {
	healthCmd.Flags().String("addr", "", "")
	if healthCmd.Flags().Lookup("addr") == nil {
		t.Fatal("healthcheck has no --addr flag")
	}
	if err := healthCmd.Flags().Parse([]string{"--addr", "127.0.0.1:9999"}); err != nil {
		t.Fatalf("parsing --addr failed: %v", err)
	}
	got, err := healthCmd.Flags().GetString("addr")
	if err != nil || got != "127.0.0.1:9999" {
		t.Errorf("got %q, %v", got, err)
	}
}
