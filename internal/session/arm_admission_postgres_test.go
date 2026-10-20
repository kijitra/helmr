package session_test

import (
	"errors"
	"testing"
	"uuid"

	"github.com/helmrdotdev/helmr/internal/computer"
	"github.com/helmrdotdev/helmr/internal/db/dbtest"
	"github.com/helmrdotdev/helmr/internal/definition"
	"github.com/helmrdotdev/helmr/internal/session"
)

func TestStartAdmitsKnownSeedArchitectures(t *testing.T) {
	for _, profile := range []string{definition.ComputerSeedProfile, definition.ComputerSeedProfileAArch64, "unknown-seed"} {
		for _, existing := range []bool{false, true} {
			t.Run(profile, func(t *testing.T) {
				f := newAdmissionFixture(t)
				dbtest.MustExec(t, t.Context(), f.pool, `UPDATE image_specs SET seed=jsonb_set(seed,'{profile}',to_jsonb($2::text)) WHERE environment_id=$1`, f.env, profile)
				request := f.startRequest("arch-admission")
				if existing {
					request.ComputerID = f.computer
				}
				_, err := session.Start(t.Context(), f.pool, nil, f.caller(), request)
				if profile == "unknown-seed" {
					if !errors.Is(err, computer.ErrInvalidInput) && !errors.Is(err, session.ErrInvalidInput) {
						t.Fatalf("unknown profile not rejected: %v", err)
					}
				} else if err != nil {
					t.Fatalf("known profile rejected existing=%v: %v", existing, err)
				}
			})
		}
	}
}

func TestStartRejectsExistingComputerArchitectureMismatch(t *testing.T) {
	for _, profile := range []string{definition.ComputerSeedProfile, definition.ComputerSeedProfileAArch64} {
		t.Run(profile, func(t *testing.T) {
			f := newAdmissionFixture(t)
			other := definition.ComputerSeedProfileAArch64
			if profile == other {
				other = definition.ComputerSeedProfile
			}
			dbtest.MustExec(t, t.Context(), f.pool, `UPDATE image_specs SET seed=jsonb_set(seed,'{profile}',to_jsonb($2::text)) WHERE environment_id=$1`, f.env, profile)
			spec := uuid.NewV7()
			dbtest.MustExec(t, t.Context(), f.pool, `INSERT INTO image_specs(environment_id,id,spec_digest,spec,seed) SELECT environment_id,$3,'sha256:'||repeat('a',64),spec,jsonb_set(seed,'{profile}',to_jsonb($4::text)) FROM image_specs WHERE environment_id=$1 AND id=$2`, f.env, f.deployment, spec, other)
			dbtest.MustExec(t, t.Context(), f.pool, `UPDATE computers SET image_spec_id=$3 WHERE environment_id=$1 AND id=$2`, f.env, f.computer, spec)
			request := f.startRequest("mismatched-computer")
			request.ComputerID = f.computer
			if _, err := session.Start(t.Context(), f.pool, nil, f.caller(), request); !errors.Is(err, session.ErrInvalidInput) {
				t.Fatalf("mismatched Computer accepted: %v", err)
			}
		})
	}
}
