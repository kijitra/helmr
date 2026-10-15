package workerapi

import (
	"encoding/json"
	"github.com/helmrdotdev/helmr/internal/vmplatform"
	"os"
	"testing"
)

func TestNativeCPUEnvironmentBindsRuntime(t *testing.T) {
	raw, err := os.ReadFile("../vmplatform/testdata/apple-vz-profile.json")
	if err != nil {
		t.Fatal(err)
	}
	var p vmplatform.Profile
	if err = json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	e := CPUEnvironment{Backend: vmplatform.BackendAppleVZ, HostKernelRelease: p.HostKernelRelease, AppleVZ: &AppleVZCPUEnvironment{HelperDigest: p.AppleVZ.HelperDigest, OSBuild: p.AppleVZ.OSBuild, HardwareModel: p.AppleVZ.HardwareModel}}
	e.Digest, err = e.ExpectedDigest()
	if err != nil {
		t.Fatal(err)
	}
	if !e.MatchesRuntime(p) {
		t.Fatal("native CPU evidence did not match")
	}
	for _, field := range []string{"helper", "build", "model", "kernel"} {
		t.Run(field, func(t *testing.T) {
			changed := e
			native := *e.AppleVZ
			changed.AppleVZ = &native
			switch field {
			case "helper":
				native.HelperDigest = p.KernelDigest
			case "build":
				native.OSBuild += "-changed"
			case "model":
				native.HardwareModel += "-changed"
			case "kernel":
				changed.HostKernelRelease += "-changed"
			}
			changed.Digest, err = changed.ExpectedDigest()
			if err != nil {
				t.Fatal(err)
			}
			if changed.Digest == e.Digest || changed.MatchesRuntime(p) {
				t.Fatal("mismatched native environment accepted")
			}
		})
	}
	for _, field := range []string{"backend", "legacy", "nil", "missing", "control"} {
		t.Run(field, func(t *testing.T) {
			changed := e
			native := *e.AppleVZ
			changed.AppleVZ = &native
			switch field {
			case "backend":
				changed.Backend = "future"
			case "legacy":
				changed.BIOSVersion = "1.0"
			case "nil":
				changed.AppleVZ = nil
			case "missing":
				native.HelperDigest = ""
			case "control":
				native.OSBuild = "bad\nfield"
			}
			if _, err := changed.ExpectedDigest(); err == nil {
				t.Fatal("invalid environment accepted")
			}
		})
	}
}
