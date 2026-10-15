package vmplatform

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/helmrdotdev/helmr/internal/sha256sum"
)

const (
	Contract           = "helmr.vm-runtime.v0"
	NativeContract     = "helmr.vm-runtime.v1"
	BackendFirecracker = "firecracker"
	BackendAppleVZ     = "apple-vz"
	AppleVZStateFormat = "helmr.apple-vz-state.v1"
)

type CPUTemplateKind string

const (
	CPUTemplateNone   CPUTemplateKind = "none"
	CPUTemplateCustom CPUTemplateKind = "custom"
)

type CPUTemplateSelector struct {
	Kind   CPUTemplateKind `json:"kind"`
	Digest string          `json:"digest,omitempty"`
}

type CPUShape struct {
	VCPUCount       int32  `json:"vcpu_count"`
	CPUConfigDigest string `json:"cpu_config_digest"`
}

// AppleVZ binds saved-state compatibility to a qualified local installation.
// These digests are routing constraints, not hardware attestation. Until
// across-reboot restore is qualified, a different boot requires a new pool.
type AppleVZ struct {
	HelperDigest       string `json:"helper_digest"`
	OSBuild            string `json:"os_build"`
	HardwareModel      string `json:"hardware_model"`
	InstallationDigest string `json:"installation_digest"`
	KeyDomainDigest    string `json:"key_domain_digest"`
	BootSessionDigest  string `json:"boot_session_digest"`
	StateFormat        string `json:"state_format"`
}

func (v AppleVZ) Validate() error {
	for _, digest := range []string{v.HelperDigest, v.InstallationDigest, v.KeyDomainDigest, v.BootSessionDigest} {
		if !sha256sum.ValidDigest(digest) {
			return errors.New("apple-vz compatibility digests must be canonical SHA-256")
		}
	}
	for _, value := range []string{v.OSBuild, v.HardwareModel} {
		if !canonicalHostString(value) {
			return errors.New("apple-vz OS build and hardware model must be canonical bounded strings")
		}
	}
	if v.StateFormat != AppleVZStateFormat {
		return errors.New("unsupported apple-vz state format")
	}
	return nil
}

func canonicalHostString(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 255 {
		return false
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

type Profile struct {
	Backend                   string              `json:"backend,omitempty"`
	AppleVZ                   *AppleVZ            `json:"apple_vz,omitempty"`
	ID                        string              `json:"id"`
	Arch                      string              `json:"arch"`
	Contract                  string              `json:"contract"`
	VMRuntimeDescriptorDigest string              `json:"vm_runtime_descriptor_digest"`
	FirecrackerDigest         string              `json:"firecracker_digest"`
	FirecrackerVersion        string              `json:"firecracker_version"`
	SnapshotFormatVersion     string              `json:"snapshot_format_version"`
	HostKernelRelease         string              `json:"host_kernel_release"`
	CPUTemplate               CPUTemplateSelector `json:"cpu_template"`
	KernelDigest              string              `json:"kernel_digest"`
	InitramfsDigest           string              `json:"initramfs_digest"`
	RootfsDigest              string              `json:"rootfs_digest"`
}

// EffectiveBackend preserves omitted-backend v0 profiles without rewriting
// their wire bytes or the canonical Firecracker hash payload.
func (p Profile) EffectiveBackend() string {
	if p.Backend == "" {
		return BackendFirecracker
	}
	return p.Backend
}

func (p Profile) ExpectedID() (string, error) {
	if err := p.validateSelector(); err != nil {
		return "", err
	}
	if p.EffectiveBackend() == BackendAppleVZ {
		payload, err := json.Marshal(struct {
			Domain            string  `json:"domain"`
			Backend           string  `json:"backend"`
			Arch              string  `json:"arch"`
			Contract          string  `json:"contract"`
			Descriptor        string  `json:"vm_runtime_descriptor_digest"`
			HostKernelRelease string  `json:"host_kernel_release"`
			KernelDigest      string  `json:"kernel_digest"`
			InitramfsDigest   string  `json:"initramfs_digest"`
			RootfsDigest      string  `json:"rootfs_digest"`
			AppleVZ           AppleVZ `json:"apple_vz"`
		}{"helmr.vm-platform.v1", BackendAppleVZ, p.Arch, p.Contract, p.VMRuntimeDescriptorDigest,
			p.HostKernelRelease, p.KernelDigest, p.InitramfsDigest, p.RootfsDigest, *p.AppleVZ})
		if err != nil {
			return "", err
		}
		return sha256sum.DigestBytes(payload), nil
	}
	payload, err := json.Marshal(struct {
		Domain                    string              `json:"domain"`
		Backend                   string              `json:"backend"`
		Arch                      string              `json:"arch"`
		Contract                  string              `json:"contract"`
		VMRuntimeDescriptorDigest string              `json:"vm_runtime_descriptor_digest"`
		FirecrackerDigest         string              `json:"firecracker_digest"`
		FirecrackerVersion        string              `json:"firecracker_version"`
		SnapshotFormatVersion     string              `json:"snapshot_format_version"`
		HostKernelRelease         string              `json:"host_kernel_release"`
		CPUTemplate               CPUTemplateSelector `json:"cpu_template"`
		KernelDigest              string              `json:"kernel_digest"`
		InitramfsDigest           string              `json:"initramfs_digest"`
		RootfsDigest              string              `json:"rootfs_digest"`
	}{
		Domain: "helmr.vm-platform.v0", Backend: "firecracker",
		Arch: p.Arch, Contract: p.Contract,
		VMRuntimeDescriptorDigest: p.VMRuntimeDescriptorDigest,
		FirecrackerDigest:         p.FirecrackerDigest, FirecrackerVersion: p.FirecrackerVersion,
		SnapshotFormatVersion: p.SnapshotFormatVersion, HostKernelRelease: p.HostKernelRelease,
		CPUTemplate: p.CPUTemplate, KernelDigest: p.KernelDigest,
		InitramfsDigest: p.InitramfsDigest, RootfsDigest: p.RootfsDigest,
	})
	if err != nil {
		return "", err
	}
	return sha256sum.DigestBytes(payload), nil
}

func (p Profile) Validate() error {
	expected, err := p.ExpectedID()
	if err != nil {
		return err
	}
	if p.ID != expected {
		return errors.New("runtime.id does not match the canonical runtime selector")
	}
	return nil
}

func (p Profile) validateSelector() error {
	var problems []error
	if p.EffectiveBackend() == BackendAppleVZ {
		return p.validateAppleVZ()
	}
	if p.EffectiveBackend() != BackendFirecracker || p.AppleVZ != nil {
		return errors.New("unsupported or mixed runtime backend")
	}
	if p.Arch != "x86_64" || p.Contract != Contract {
		problems = append(problems, errors.New("runtime architecture or contract is not supported"))
	}
	for _, field := range []struct{ name, value string }{
		{name: "vm_runtime_descriptor_digest", value: p.VMRuntimeDescriptorDigest},
		{name: "firecracker_digest", value: p.FirecrackerDigest},
		{name: "kernel_digest", value: p.KernelDigest},
		{name: "initramfs_digest", value: p.InitramfsDigest},
		{name: "rootfs_digest", value: p.RootfsDigest},
	} {
		if !sha256sum.ValidDigest(field.value) {
			problems = append(problems, fmt.Errorf("runtime.%s must be a canonical SHA-256 digest", field.name))
		}
	}
	if !validSemanticVersion(p.FirecrackerVersion) {
		problems = append(problems, errors.New("runtime.firecracker_version must be a canonical semantic version"))
	}
	if !validSemanticVersion(p.SnapshotFormatVersion) {
		problems = append(problems, errors.New("runtime.snapshot_format_version must be a canonical semantic version"))
	}
	if p.HostKernelRelease == "" || strings.TrimSpace(p.HostKernelRelease) != p.HostKernelRelease || len(p.HostKernelRelease) > 255 {
		problems = append(problems, errors.New("runtime.host_kernel_release must be a non-empty canonical release string"))
	}
	switch p.CPUTemplate.Kind {
	case CPUTemplateNone:
		if p.CPUTemplate.Digest != "" {
			problems = append(problems, errors.New("runtime.cpu_template.digest must be empty for kind none"))
		}
	case CPUTemplateCustom:
		if !sha256sum.ValidDigest(p.CPUTemplate.Digest) {
			problems = append(problems, errors.New("runtime.cpu_template.digest must be a canonical SHA-256 digest for kind custom"))
		}
	default:
		problems = append(problems, errors.New("runtime.cpu_template.kind must be none or custom"))
	}
	return errors.Join(problems...)
}

func ArchitectureFromGo(value string) (string, error) {
	if value == "amd64" {
		return "x86_64", nil
	}
	return "", fmt.Errorf("unsupported Go architecture %q", value)
}

func validSemanticVersion(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return false
		}
		if _, err := strconv.ParseUint(part, 10, 32); err != nil {
			return false
		}
	}
	return true
}

func (p Profile) validateAppleVZ() error {
	if p.Backend != BackendAppleVZ || p.Arch != "aarch64" || p.Contract != NativeContract || p.AppleVZ == nil {
		return errors.New("apple-vz requires aarch64, native contract and compatibility payload")
	}
	if p.FirecrackerDigest != "" || p.FirecrackerVersion != "" || p.SnapshotFormatVersion != "" || p.CPUTemplate != (CPUTemplateSelector{}) {
		return errors.New("apple-vz profile contains Firecracker-only fields")
	}
	if !canonicalHostString(p.HostKernelRelease) {
		return errors.New("invalid native host kernel release")
	}
	for _, value := range []string{p.VMRuntimeDescriptorDigest, p.KernelDigest, p.InitramfsDigest, p.RootfsDigest} {
		if !sha256sum.ValidDigest(value) {
			return errors.New("native runtime artifact digest is not canonical SHA-256")
		}
	}
	return p.AppleVZ.Validate()
}
