package session_test

import (
	"errors"
	"github.com/helmrdotdev/helmr/internal/db/dbtest"
	"github.com/helmrdotdev/helmr/internal/definition"
	"github.com/helmrdotdev/helmr/internal/dispatch"
	"testing"
)

func TestAllocationBindsSeedAndHostArchitecture(t *testing.T) {
	for _, test := range []struct {
		profile, arch string
		valid         bool
	}{
		{definition.ComputerSeedProfile, "x86_64", true},
		{definition.ComputerSeedProfileAArch64, "aarch64", true},
		{definition.ComputerSeedProfileAArch64, "x86_64", false},
		{definition.ComputerSeedProfile, "aarch64", false},
		{"unknown-seed", "aarch64", false},
	} {
		t.Run(test.profile+"/"+test.arch, func(t *testing.T) {
			t.Run("preparation", func(t *testing.T) {
				f, p, a := preparationAllocationFixture(t)
				dbtest.MustExec(t, t.Context(), f.pool, `UPDATE image_specs SET seed=jsonb_set(seed,'{profile}',to_jsonb($2::text)) WHERE environment_id=$1`, f.env, test.profile)
				dbtest.MustExec(t, t.Context(), f.pool, `UPDATE vm_platforms SET arch=$1`, test.arch)
				_, err := a.AllocateImageBuild(t.Context(), f.env, p.ID, f.worker)
				if test.valid {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					if !errors.Is(err, dispatch.ErrNotReady) {
						t.Fatalf("wrong host accepted: %v", err)
					}
					var assigned bool
					if err := f.pool.QueryRow(t.Context(), `SELECT worker_host_id IS NOT NULL FROM image_builds WHERE environment_id=$1 AND id=$2`, f.env, p.ID).Scan(&assigned); err != nil || assigned {
						t.Fatalf("rejected allocation retained: %v", err)
					}
				}
			})
			t.Run("fresh lease", func(t *testing.T) {
				f, a, id := freshComputerAllocationFixture(t)
				dbtest.MustExec(t, t.Context(), f.pool, `UPDATE image_specs SET seed=jsonb_set(seed,'{profile}',to_jsonb($2::text)) WHERE environment_id=$1`, f.env, test.profile)
				dbtest.MustExec(t, t.Context(), f.pool, `UPDATE vm_platforms SET arch=$1`, test.arch)
				_, err := a.AllocateFreshLease(t.Context(), f.env, id, 1, f.worker)
				if test.valid {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					if !errors.Is(err, dispatch.ErrNotReady) {
						t.Fatalf("wrong host accepted: %v", err)
					}
					var count int
					if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM computer_leases WHERE environment_id=$1 AND computer_id=$2`, f.env, id).Scan(&count); err != nil || count != 0 {
						t.Fatalf("rejected lease retained: %d %v", count, err)
					}
				}
			})
		})
	}
}
