package firecracker

import (
	"fmt"
	"github.com/helmrdotdev/helmr/internal/vmplatform"
)

func inspectRuntimeEnvironmentForArch(arch string, readFile func(string) ([]byte, error), version, kernel string) (RuntimeEnvironmentEvidence, error) {
	if arch == "amd64" {
		return inspectRuntimeEnvironment(readFile, version, kernel)
	}
	if arch != "arm64" {
		return RuntimeEnvironmentEvidence{}, fmt.Errorf("unsupported environment architecture %q", arch)
	}
	raw, err := readFile("/proc/cpuinfo")
	if err != nil {
		return RuntimeEnvironmentEvidence{}, err
	}
	arm, err := vmplatform.ParseARMCPUInfo(raw)
	if err != nil {
		return RuntimeEnvironmentEvidence{}, fmt.Errorf("measure ARM CPU environment: %w", err)
	}
	return RuntimeEnvironmentEvidence{ARM: arm, FirecrackerVersion: version, KernelVersion: kernel}, nil
}
