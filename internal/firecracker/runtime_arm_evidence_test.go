package firecracker

import (
	"github.com/helmrdotdev/helmr/internal/vmplatform"
	"testing"
)

func TestARMRuntimeEnvironmentRequiresCompleteMeasuredCPUInfo(t *testing.T) {
	raw := []byte("processor : 0\nFeatures : fp asimd\nCPU implementer : 0x61\nCPU architecture : 8\nCPU variant : 0x0\nCPU part : 0x000\nCPU revision : 0\n")
	called := 0
	e, err := inspectRuntimeEnvironmentForArch("arm64", func(path string) ([]byte, error) {
		called++
		if path != "/proc/cpuinfo" {
			t.Fatal("ARM attempted x86 evidence")
		}
		return raw, nil
	}, "1.17.0", "7.0.0")
	if err != nil || called != 1 || e.ARM == nil || e.MicrocodeVersion != "" || e.BIOSVersion != "" {
		t.Fatalf("ARM evidence: %+v %v", e, err)
	}
	before, err := digestRuntimeEnvironment(e)
	if err != nil {
		t.Fatal(err)
	}
	e.ARM.Processors[0].Revision = "1"
	after, _ := digestRuntimeEnvironment(e)
	if before == after {
		t.Fatal("ARM CPU mutation retained environment digest")
	}
	if _, err := inspectRuntimeEnvironmentForArch("arm64", func(string) ([]byte, error) { return []byte("processor : 0\nmicrocode : 0x1\n"), nil }, "1.17.0", "7.0.0"); err == nil {
		t.Fatal("x86 cpuinfo accepted for ARM")
	}
	input := HostRuntimeEvidence{CPUShapes: []CPUShapeEvidence{{VCPUCount: 1}}, Environment: RuntimeEnvironmentEvidence{ARM: &vmplatform.ARMCPUEnvironment{Architecture: "aarch64", Processors: []vmplatform.ARMProcessor{{Revision: "0"}}}}}
	retained := input.clone()
	input.CPUShapes[0].VCPUCount = 2
	input.Environment.ARM.Processors[0].Revision = "1"
	if retained.CPUShapes[0].VCPUCount != 1 || retained.Environment.ARM.Processors[0].Revision != "0" {
		t.Fatal("runtime evidence input alias")
	}
	copy := retained.clone()
	copy.CPUShapes[0].VCPUCount = 3
	copy.Environment.ARM.Processors[0].Revision = "2"
	if retained.CPUShapes[0].VCPUCount != 1 || retained.Environment.ARM.Processors[0].Revision != "0" {
		t.Fatal("runtime evidence getter alias")
	}
}
