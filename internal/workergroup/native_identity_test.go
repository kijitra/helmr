package workergroup

import (
	"encoding/json"
	"errors"
	"github.com/helmrdotdev/helmr/internal/db/dbtest"
	"github.com/helmrdotdev/helmr/internal/pgvalue"
	"github.com/helmrdotdev/helmr/internal/vmplatform"
	"os"
	"testing"
	"uuid"
)

func nativeTemplate(t *testing.T) Template {
	t.Helper()
	template := validHostTemplate(t)
	raw, err := os.ReadFile("../vmplatform/testdata/apple-vz-profile.json")
	if err != nil {
		t.Fatal(err)
	}
	var p vmplatform.Profile
	if err = json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	template.Runtime = p
	return template
}
func TestNativeVMPlatformProjectionAndSealedPool(t *testing.T) {
	template := nativeTemplate(t)
	params, err := vmPlatformParams(template.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	if params.Backend != "apple-vz" || params.Arch != "aarch64" || params.FirecrackerDigest.Valid || params.FirecrackerVersion.Valid || params.SnapshotFormatVersion.Valid || params.CPUTemplateDigest.Valid {
		t.Fatal("native profile projected into legacy columns")
	}
	var native vmplatform.AppleVZ
	if err = json.Unmarshal(params.AppleVz, &native); err != nil {
		t.Fatal(err)
	}
	if native != *template.Runtime.AppleVZ {
		t.Fatal("native payload lost fields")
	}
	pool, shapes := sealedPool(pgvalue.NewUUIDv7(), template)
	if !poolMatches(pool, shapes, template) {
		t.Fatal("same boot profile rejected")
	}
	for _, field := range []string{"boot", "installation", "key"} {
		t.Run(field, func(t *testing.T) {
			changed := nativeTemplate(t)
			switch field {
			case "boot":
				changed.Runtime.AppleVZ.BootSessionDigest = dbtest.Digest("new-boot")
			case "installation":
				changed.Runtime.AppleVZ.InstallationDigest = dbtest.Digest("new-host")
			case "key":
				changed.Runtime.AppleVZ.KeyDomainDigest = dbtest.Digest("new-key")
			}
			changed.Runtime.ID, err = changed.Runtime.ExpectedID()
			if err != nil {
				t.Fatal(err)
			}
			if poolMatches(pool, shapes, changed) {
				t.Fatal("changed affinity matched sealed pool")
			}
		})
	}
}
func TestNativeHostReactivationWithPostgres(t *testing.T) {
	f := newSupplyFixture(t)
	cfg := testHostAuthConfig(t)
	template := nativeTemplate(t)
	host := f.start(t, cfg, "native", "local-mac", template)
	// Same boot, new Worker process and epoch: recovery then activation succeeds.
	credential := f.exchange(t, cfg, host.enrolled, uuid.NewV7().String())
	if err := RecordStartupRecovery(t.Context(), f.q, f.authenticate(t, AuthenticateRecoveringHost, cfg, credential), startupEvidence(t)); err != nil {
		t.Fatal(err)
	}
	activating := f.authenticate(t, AuthenticateActivatingHost, cfg, credential)
	if err := ActivateHost(t.Context(), f.pool, activating, Activation{Template: template, CPUEnvironment: []byte(`{}`), CPUEnvironmentDigest: f.cpuConfigDigest}); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"boot", "key"} {
		t.Run(field, func(t *testing.T) {
			changed := nativeTemplate(t)
			if field == "boot" {
				changed.Runtime.AppleVZ.BootSessionDigest = dbtest.Digest("another-boot")
			} else {
				changed.Runtime.AppleVZ.KeyDomainDigest = dbtest.Digest("another-key")
			}
			var err error
			changed.Runtime.ID, err = changed.Runtime.ExpectedID()
			if err != nil {
				t.Fatal(err)
			}
			credential := f.exchange(t, cfg, host.enrolled, uuid.NewV7().String())
			if err := RecordStartupRecovery(t.Context(), f.q, f.authenticate(t, AuthenticateRecoveringHost, cfg, credential), startupEvidence(t)); err != nil {
				t.Fatal(err)
			}
			err = ActivateHost(t.Context(), f.pool, f.authenticate(t, AuthenticateActivatingHost, cfg, credential), Activation{Template: changed, CPUEnvironment: []byte(`{}`), CPUEnvironmentDigest: f.cpuConfigDigest})
			var conflict ConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("changed %s activated: %v", field, err)
			}
		})
	}
}
