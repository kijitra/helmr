package workerapi

import "testing"

func TestCPUEnvironmentDigestAndValidation(t *testing.T) {
	environment := CPUEnvironment{
		FirecrackerVersion: "1.16.1",
		HostKernelRelease:  "6.8.0-1024-aws",
		MicrocodeVersion:   "0x2b000643",
		BIOSVersion:        "1.0",
		BIOSRevision:       "1.0",
	}
	var err error
	environment.Digest, err = environment.ExpectedDigest()
	if err != nil {
		t.Fatal(err)
	}
	if environment.Digest != "sha256:b3d42ad024bff274db82faf5a002f0066ddbde5d8cc2ee5fe550aef0f12f8ee1" {
		t.Fatal("legacy CPU digest changed")
	}
	if err := environment.Validate(); err != nil {
		t.Fatalf("validate canonical CPU environment: %v", err)
	}

	environment.BIOSRevision = "changed"
	if err := environment.Validate(); err == nil {
		t.Fatal("CPU environment with stale digest was accepted")
	}
}

func TestCPUEnvironmentRejectsNoncanonicalField(t *testing.T) {
	environment := CPUEnvironment{
		FirecrackerVersion: "1.16.1",
		HostKernelRelease:  "6.8.0-1024-aws",
		MicrocodeVersion:   " 0x2b000643",
		BIOSVersion:        "1.0",
		BIOSRevision:       "1.0",
	}
	if _, err := environment.ExpectedDigest(); err == nil {
		t.Fatal("noncanonical CPU environment was accepted")
	}
}
