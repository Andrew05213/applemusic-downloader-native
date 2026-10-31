package main

import "testing"

func TestServerListenAddressIsLoopbackUnlessRunningInTheContainer(t *testing.T) {
	t.Setenv("AMDL_CONTAINER_MODE", "")
	t.Setenv("AMDL_RUNTIME", "")
	t.Setenv("AMDL_APPLIANCE_BACKEND_HOST", "")
	address, err := serverListenAddress("8080")
	if err != nil {
		t.Fatalf("default listen address: %v", err)
	}
	if address != "127.0.0.1:8080" {
		t.Fatalf("default listen address = %q, want loopback", address)
	}

	t.Setenv("AMDL_CONTAINER_MODE", "1")
	address, err = serverListenAddress("8080")
	if err != nil {
		t.Fatalf("container listen address: %v", err)
	}
	if address != "0.0.0.0:8080" {
		t.Fatalf("container listen address = %q, want all interfaces for Docker port forwarding", address)
	}
}

func TestServerListenAddressUsesOnlyAllowlistedApplianceLoopback(t *testing.T) {
	t.Setenv("AMDL_CONTAINER_MODE", "")
	t.Setenv("AMDL_RUNTIME", "wsl-appliance")
	t.Setenv("AMDL_APPLIANCE_BACKEND_HOST", "")
	address, err := serverListenAddress("8080")
	if err != nil || address != "127.0.0.1:8080" {
		t.Fatalf("default appliance listen address = %q, %v", address, err)
	}
	for _, host := range []string{"127.0.0.1", "127.0.0.2"} {
		t.Run(host, func(t *testing.T) {
			t.Setenv("AMDL_APPLIANCE_BACKEND_HOST", host)
			address, err := serverListenAddress("8080")
			if err != nil {
				t.Fatalf("appliance listen address: %v", err)
			}
			if address != host+":8080" {
				t.Fatalf("appliance listen address = %q, want %q", address, host+":8080")
			}
		})
	}
	for _, host := range []string{"localhost", "127.0.0.3", "0.0.0.0", "127.0.0.2 ", "http://127.0.0.2"} {
		t.Run("reject_"+host, func(t *testing.T) {
			t.Setenv("AMDL_APPLIANCE_BACKEND_HOST", host)
			if _, err := serverListenAddress("8080"); err == nil || err.Error() != "AMDL_APPLIANCE_BACKEND_HOST must be an approved loopback address" {
				t.Fatalf("invalid appliance host %q error = %v", host, err)
			}
		})
	}
	t.Setenv("AMDL_CONTAINER_MODE", "1")
	t.Setenv("AMDL_APPLIANCE_BACKEND_HOST", "127.0.0.2")
	address, err = serverListenAddress("8080")
	if err != nil || address != "127.0.0.2:8080" {
		t.Fatalf("appliance identity did not override container wildcard mode: %q, %v", address, err)
	}
}

func TestServerListenAddressKeepsLegacy125SupervisorUpdateCompatible(t *testing.T) {
	const legacySupervisorVersion = "1.2.5"
	t.Setenv("AMDL_CONTAINER_MODE", "")
	t.Setenv("AMDL_RUNTIME", "wsl-appliance")
	t.Setenv("AMDL_APPLIANCE_BACKEND_HOST", "")
	address, err := serverListenAddress("18086")
	if err != nil || address != "127.0.0.1:18086" {
		t.Fatalf("backend started by %s supervisor listens at %q, %v", legacySupervisorVersion, address, err)
	}
}

func TestServerListenAddressRejectsHostInjectionAndInvalidPorts(t *testing.T) {
	t.Setenv("AMDL_CONTAINER_MODE", "")
	t.Setenv("AMDL_RUNTIME", "")
	t.Setenv("AMDL_APPLIANCE_BACKEND_HOST", "")
	for _, value := range []string{"", "0", "65536", "0.0.0.0:8080", "abc"} {
		if _, err := serverListenAddress(value); err == nil {
			t.Fatalf("serverListenAddress(%q) unexpectedly succeeded", value)
		}
	}
}

func TestServerListenAddressRejectsApplianceHostOutsideAppliance(t *testing.T) {
	t.Setenv("AMDL_CONTAINER_MODE", "1")
	t.Setenv("AMDL_RUNTIME", "")
	t.Setenv("AMDL_APPLIANCE_BACKEND_HOST", "127.0.0.2")
	if _, err := serverListenAddress("8080"); err == nil || err.Error() != "AMDL_APPLIANCE_BACKEND_HOST is only valid for the WSL appliance" {
		t.Fatalf("non-appliance host override error = %v", err)
	}
}
