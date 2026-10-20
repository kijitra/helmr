package session_test

import (
	"errors"
	"testing"

	"github.com/helmrdotdev/helmr/internal/computer"
	"github.com/helmrdotdev/helmr/internal/db/dbtest"
	"github.com/helmrdotdev/helmr/internal/definition"
)

func TestImageBuildStartBindsSeedPlatformArchitecture(t *testing.T) {
	for _, arch := range []definition.RuntimeArchitecture{definition.ArchitectureX8664, definition.ArchitectureAArch64} {
		t.Run(string(arch), func(t *testing.T) {
			f, a, _, build := startBudgetFixture(t)
			profile, _ := arch.SeedProfile()
			dbtest.MustExec(t, t.Context(), f.pool, `UPDATE image_specs SET seed=jsonb_set(seed,'{profile}',to_jsonb($2::text)) WHERE environment_id=$1`, f.env, profile)
			dbtest.MustExec(t, t.Context(), f.pool, `UPDATE vm_platforms SET arch=$1`, string(arch))
			allocation, err := a.AllocateImageBuild(t.Context(), f.env, build, f.worker)
			if err != nil {
				t.Fatal(err)
			}
			identity := computer.ImageBuildIdentity{EnvironmentID: f.env, ImageBuildID: build, InstanceID: allocation.InstanceID, Epoch: allocation.Epoch}
			delivery, err := a.DeliverImageBuild(t.Context(), *f.host(), identity)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := computer.ReadImageBuildStart(t.Context(), f.pool, *f.host(), delivery.Executor); err != nil {
				t.Fatal(err)
			}
			other := definition.ComputerSeedProfile
			if profile == other {
				other = definition.ComputerSeedProfileAArch64
			}
			for _, wrong := range []string{other, "unknown-seed"} {
				dbtest.MustExec(t, t.Context(), f.pool, `UPDATE image_specs SET seed=jsonb_set(seed,'{profile}',to_jsonb($2::text)) WHERE environment_id=$1`, f.env, wrong)
				if _, err := computer.ReadImageBuildStart(t.Context(), f.pool, *f.host(), delivery.Executor); !errors.Is(err, computer.ErrDenied) {
					t.Fatalf("mismatched executable admitted: %v", err)
				}
			}
		})
	}
}
