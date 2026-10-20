package workerapi

import (
	"github.com/helmrdotdev/helmr/internal/vmplatform"
	"strings"
	"testing"
)

func TestARMCPUEnvironmentRejectsMixedEvidenceAndArchitecture(t *testing.T) {
	arm, err := vmplatform.ParseARMCPUInfo([]byte("processor : 0\nFeatures : fp asimd\nCPU implementer : 0x61\nCPU architecture : 8\nCPU variant : 0x0\nCPU part : 0x000\nCPU revision : 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	e := CPUEnvironment{ARM: arm, FirecrackerVersion: "1.17.0", HostKernelRelease: "7.0.0"}
	e.Digest, err = e.ExpectedDigest()
	if err != nil || e.Validate() != nil {
		t.Fatal(err)
	}
	p := vmplatform.Profile{Arch: "aarch64", Contract: vmplatform.Contract, VMRuntimeDescriptorDigest: "sha256:" + strings.Repeat("a", 64), FirecrackerDigest: "sha256:" + strings.Repeat("b", 64), FirecrackerVersion: "1.17.0", SnapshotFormatVersion: "7.0.0", HostKernelRelease: "7.0.0", CPUTemplate: vmplatform.CPUTemplateSelector{Kind: vmplatform.CPUTemplateNone}, KernelDigest: "sha256:" + strings.Repeat("1", 64), InitramfsDigest: "sha256:" + strings.Repeat("2", 64), RootfsDigest: "sha256:" + strings.Repeat("3", 64)}
	p.ID, err = p.ExpectedID()
	if err != nil || !e.MatchesRuntime(p) {
		t.Fatal("ARM environment cannot match ARM runtime")
	}
	p.Arch = "x86_64"
	p.ID, _ = p.ExpectedID()
	if e.MatchesRuntime(p) {
		t.Fatal("ARM environment matched x86 runtime")
	}
	for _, change := range []func(*CPUEnvironment){func(v *CPUEnvironment) { v.MicrocodeVersion = "0x1" }, func(v *CPUEnvironment) { v.BIOSVersion = "fake" }, func(v *CPUEnvironment) { v.Backend = vmplatform.BackendAppleVZ }, func(v *CPUEnvironment) { v.AppleVZ = &AppleVZCPUEnvironment{} }} {
		v := e
		change(&v)
		if _, err := v.ExpectedDigest(); err == nil {
			t.Fatal("mixed ARM evidence accepted")
		}
	}
	changed := e
	changed.ARM = e.ARM.Clone()
	changed.ARM.Processors[0].Revision = "1"
	if changed.Validate() == nil {
		t.Fatal("changed ARM CPU retained digest")
	}
}
