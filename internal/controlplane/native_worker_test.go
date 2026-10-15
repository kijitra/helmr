package controlplane

import (
	"bytes"
	"encoding/json"
	"github.com/helmrdotdev/helmr/internal/vmplatform"
	"github.com/helmrdotdev/helmr/internal/workerapi"
	"net/http/httptest"
	"os"
	"testing"
)

func nativeWorkerCapabilities(t *testing.T) workerapi.Capabilities {
	t.Helper()
	c := validWorkerCapabilities(t)
	raw, err := os.ReadFile("../vmplatform/testdata/apple-vz-profile.json")
	if err != nil {
		t.Fatal(err)
	}
	c.Runtime = vmplatform.Profile{}
	if err = json.Unmarshal(raw, &c.Runtime); err != nil {
		t.Fatal(err)
	}
	v := c.Runtime.AppleVZ
	c.CPUEnvironment = workerapi.CPUEnvironment{Backend: vmplatform.BackendAppleVZ, HostKernelRelease: c.Runtime.HostKernelRelease, AppleVZ: &workerapi.AppleVZCPUEnvironment{HelperDigest: v.HelperDigest, OSBuild: v.OSBuild, HardwareModel: v.HardwareModel}}
	c.CPUEnvironment.Digest, err = c.CPUEnvironment.ExpectedDigest()
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestNativeWorkerActivationContract(t *testing.T) {
	c := nativeWorkerCapabilities(t)
	raw, err := json.Marshal(workerapi.ActivateRequest{Capabilities: c})
	if err != nil {
		t.Fatal(err)
	}
	var req workerapi.ActivateRequest
	if err := decodeRequestJSON(httptest.NewRequest("POST", "/", bytes.NewReader(raw)), &req); err != nil {
		t.Fatal(err)
	}
	normalized, err := normalizeWorkerCapabilities(req.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if err := workerTemplate(normalized).Validate(); err != nil {
		t.Fatal(err)
	}
	// A valid but different native host measurement must fail admission too.
	c.CPUEnvironment.AppleVZ.OSBuild += "-other"
	c.CPUEnvironment.Digest, err = c.CPUEnvironment.ExpectedDigest()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeWorkerCapabilities(c); err == nil {
		t.Fatal("native environment mismatch admitted")
	}
	future := bytes.Replace(raw, []byte(`"apple_vz":{`), []byte(`"apple_vz":{"future_field":true,`), 1)
	if err := decodeRequestJSON(httptest.NewRequest("POST", "/", bytes.NewReader(future)), &req); err == nil {
		t.Fatal("unknown native field accepted")
	}
	legacy := validWorkerCapabilities(t)
	legacy.Runtime.Backend = "future"
	if _, err := normalizeWorkerCapabilities(legacy); err == nil {
		t.Fatal("unknown backend accepted")
	}
}

func TestNativeWorkerTemplateDetachesCompatibilityPayload(t *testing.T) {
	c := nativeWorkerCapabilities(t)
	template := workerTemplate(c)
	template.Runtime.AppleVZ.OSBuild = "changed-result"
	if c.Runtime.AppleVZ.OSBuild == "changed-result" {
		t.Fatal("template aliases activation payload")
	}
	c.Runtime.AppleVZ.HardwareModel = "changed-input"
	if template.Runtime.AppleVZ.HardwareModel == "changed-input" {
		t.Fatal("input aliases sealed template")
	}
}
