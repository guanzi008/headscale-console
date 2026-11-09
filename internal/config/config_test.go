package config

import "testing"

func TestDefaultIsValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config is invalid: %v", err)
	}
}

func TestInternalListenersMustBeLoopback(t *testing.T) {
	cfg := Default()
	cfg.Server.Internal.IDP = "0.0.0.0:18070"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected non-loopback internal listener to be rejected")
	}
}

func TestDuplicateInternalListenersAreRejected(t *testing.T) {
	cfg := Default()
	cfg.Server.Internal.IDP = cfg.Server.Internal.Headplane
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected duplicate internal listeners to be rejected")
	}
}

func TestHeadplaneDefaultRole(t *testing.T) {
	cfg := Default()
	cfg.Headplane.DefaultRole = "Viewer"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected viewer role to be accepted: %v", err)
	}
	if cfg.Headplane.DefaultRole != "viewer" {
		t.Fatalf("default role was not normalized: %q", cfg.Headplane.DefaultRole)
	}

	cfg.Headplane.DefaultRole = "owner"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected owner to be rejected as a default role")
	}
}
