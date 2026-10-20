//go:build linux

package firecracker

import (
	"github.com/helmrdotdev/helmr/internal/vmplatform"
	"testing"
)

func TestQualifiedRuntimeEvidenceCannotBeMutatedByCaller(t *testing.T) {
	e := HostRuntimeEvidence{CPUShapes: []CPUShapeEvidence{{VCPUCount: 1}}, Environment: RuntimeEnvironmentEvidence{ARM: &vmplatform.ARMCPUEnvironment{Architecture: "aarch64", Processors: []vmplatform.ARMProcessor{{Revision: "0"}}}}}
	runtime := QualifiedRuntime{evidence: e.clone()}
	e.Environment.ARM.Processors[0].Revision = "1"
	exported := runtime.HostRuntimeEvidence()
	exported.Environment.ARM.Processors[0].Revision = "2"
	exported.CPUShapes[0].VCPUCount = 2
	actual := runtime.HostRuntimeEvidence()
	if actual.Environment.ARM.Processors[0].Revision != "0" || actual.CPUShapes[0].VCPUCount != 1 {
		t.Fatal("qualified evidence escaped by reference")
	}
}
