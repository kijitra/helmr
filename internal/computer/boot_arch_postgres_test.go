package computer

import (
	"strings"
	"testing"
	"uuid"

	"github.com/helmrdotdev/helmr/internal/db/dbtest"
	"github.com/helmrdotdev/helmr/internal/definition"
	"github.com/helmrdotdev/helmr/internal/session/sessiontest"
	"github.com/helmrdotdev/helmr/internal/workergroup"
)

func TestReadBootRequiresSeedPlatformArchitecture(t *testing.T) {
	for _, test := range []struct {
		profile, platform string
		valid             bool
	}{
		{definition.ComputerSeedProfile, "x86_64", true},
		{definition.ComputerSeedProfileAArch64, "aarch64", true},
		{definition.ComputerSeedProfileAArch64, "x86_64", false},
		{definition.ComputerSeedProfile, "aarch64", false},
		{"unknown-seed", "aarch64", false},
	} {
		t.Run(test.profile+"/"+test.platform, func(t *testing.T) {
			f := newFixture(t)
			sessiontest.PrepareAdmission(t, f.base)
			digest := "sha256:" + strings.Repeat("1", 64)
			dbtest.MustExec(t, t.Context(), f.pool, `UPDATE image_specs SET seed=jsonb_build_object('profile',$2::text,'mediaType',$3::text,'artifactDigest',$4::text,'config','{}'::jsonb) WHERE environment_id=$1`, f.env, test.profile, definition.ComputerSeedMediaType, digest)
			dbtest.MustExec(t, t.Context(), f.pool, `UPDATE vm_platforms SET arch=$1`, test.platform)
			var instance uuid.UUID
			if err := f.pool.QueryRow(t.Context(), `SELECT allocation_id FROM computer_leases WHERE environment_id=$1 AND computer_id=$2 AND epoch=1`, f.env, f.computer).Scan(&instance); err != nil {
				t.Fatal(err)
			}
			host := workergroup.HostPrincipal{HostID: f.base.Worker, GroupID: f.base.Group, Epoch: 1, HostClaimVersion: 1, GroupClaimVersion: 1}
			_, err := ReadBoot(t.Context(), f.pool, host, LeaseIdentity{EnvironmentID: f.env, ComputerID: f.computer, InstanceID: instance, Epoch: 1})
			if (err == nil) != test.valid {
				t.Fatalf("boot valid=%v error=%v", test.valid, err)
			}
		})
	}
}
