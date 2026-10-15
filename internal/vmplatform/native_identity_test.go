package vmplatform

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func nativeProfile(t *testing.T) Profile {
	t.Helper()
	raw, err := os.ReadFile("testdata/apple-vz-profile.json")
	if err != nil {
		t.Fatal(err)
	}
	var p Profile
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNativeProfileGoldenAndRoundTrip(t *testing.T) {
	p := nativeProfile(t)
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var again Profile
	if err := json.Unmarshal(raw, &again); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, again) || again.Validate() != nil {
		t.Fatal("native identity changed during round trip")
	}
	legacy := testProfile(t)
	raw, _ = json.Marshal(legacy)
	if strings.Contains(string(raw), `"backend"`) || strings.Contains(string(raw), `"apple_vz"`) {
		t.Fatal("new optional fields changed legacy wire profile")
	}
	legacy.Backend = BackendFirecracker
	if err := legacy.Validate(); err != nil {
		t.Fatal("explicit legacy backend changed ID:", err)
	}
}

func TestNativeProfileBindsEveryCompatibilityField(t *testing.T) {
	for _, field := range []string{"VMRuntimeDescriptorDigest", "HostKernelRelease", "KernelDigest", "InitramfsDigest", "RootfsDigest", "HelperDigest", "OSBuild", "HardwareModel", "InstallationDigest", "KeyDomainDigest", "BootSessionDigest"} {
		t.Run(field, func(t *testing.T) {
			p := nativeProfile(t)
			v := reflect.ValueOf(&p).Elem().FieldByName(field)
			if !v.IsValid() {
				v = reflect.ValueOf(p.AppleVZ).Elem().FieldByName(field)
			}
			if strings.HasSuffix(field, "Digest") {
				v.SetString("sha256:" + strings.Repeat("f", 64))
			} else {
				v.SetString(v.String() + "-changed")
			}
			id, err := p.ExpectedID()
			if err != nil {
				t.Fatal(err)
			}
			if id == p.ID || p.Validate() == nil {
				t.Fatal("changed compatibility field kept identity")
			}
		})
	}
}

func TestNativeProfileRejectsMixedOrUnsupportedSelectors(t *testing.T) {
	cases := map[string]func(*Profile){
		"unknown backend":     func(p *Profile) { p.Backend = "future" },
		"missing backend":     func(p *Profile) { p.Backend = "" },
		"wrong arch":          func(p *Profile) { p.Arch = "x86_64" },
		"wrong contract":      func(p *Profile) { p.Contract = Contract },
		"future contract":     func(p *Profile) { p.Contract = "helmr.vm-runtime.v2" },
		"missing native":      func(p *Profile) { p.AppleVZ = nil },
		"firecracker digest":  func(p *Profile) { p.FirecrackerDigest = p.KernelDigest },
		"firecracker version": func(p *Profile) { p.FirecrackerVersion = "1.16.1" },
		"legacy snapshot":     func(p *Profile) { p.SnapshotFormatVersion = "6.0.0" },
		"cpu template":        func(p *Profile) { p.CPUTemplate.Kind = CPUTemplateNone },
		"empty host":          func(p *Profile) { p.HostKernelRelease = "" },
		"kernel digest":       func(p *Profile) { p.KernelDigest = "bad" },
		"missing affinity":    func(p *Profile) { p.AppleVZ.InstallationDigest = "" },
		"missing key domain":  func(p *Profile) { p.AppleVZ.KeyDomainDigest = "" },
		"missing boot":        func(p *Profile) { p.AppleVZ.BootSessionDigest = "" },
		"noncanonical helper": func(p *Profile) { p.AppleVZ.HelperDigest = strings.ToUpper(p.AppleVZ.HelperDigest) },
		"future state":        func(p *Profile) { p.AppleVZ.StateFormat = "future" },
		"empty model":         func(p *Profile) { p.AppleVZ.HardwareModel = "" },
		"oversized build":     func(p *Profile) { p.AppleVZ.OSBuild = strings.Repeat("a", 256) },
		"control character":   func(p *Profile) { p.AppleVZ.HardwareModel = "Mac\n13,2" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := nativeProfile(t)
			mutate(&p)
			if _, err := p.ExpectedID(); err == nil {
				t.Fatal("invalid native selector accepted")
			}
		})
	}
	legacy := testProfile(t)
	legacy.AppleVZ = nativeProfile(t).AppleVZ
	if _, err := legacy.ExpectedID(); err == nil {
		t.Fatal("native payload accepted in legacy profile")
	}
}
