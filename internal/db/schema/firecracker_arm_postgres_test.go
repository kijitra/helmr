package schema

import (
	"github.com/golang-migrate/migrate/v4"
	"github.com/helmrdotdev/helmr/internal/db/dbtest"
	"testing"
)

func TestFirecrackerARMMigrationRejectsDowngradeWithProfiles(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	if err := run(ctx, d.DSN, func(m *migrate.Migrate) error { return m.Migrate(2) }); err != nil {
		t.Fatal(err)
	}
	id := dbtest.Digest("ARM-runtime")
	insert := `INSERT INTO vm_platforms(id,descriptor_digest,backend,arch,contract,firecracker_digest,firecracker_version,snapshot_format_version,host_kernel_release,kernel_digest,initramfs_digest,rootfs_digest) VALUES($1,$1,'firecracker','aarch64','helmr.vm-runtime.v0',$1,'1.17.0','7.0.0','7.0.0',$1,$1,$1)`
	if _, err := d.Pool.Exec(ctx, insert, id); err == nil {
		t.Fatal("old schema accepted ARM")
	}
	if err := Up(ctx, d.DSN); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, d.Pool, insert, id)
	var arch string
	if err := d.Pool.QueryRow(ctx, "SELECT arch FROM vm_platforms WHERE id=$1", id).Scan(&arch); err != nil || arch != "aarch64" {
		t.Fatal("ARM profile did not roundtrip", err)
	}
	for _, assignment := range []string{"arch='arm64'", "arch='riscv64'", "arch=NULL", "firecracker_digest=NULL", "contract='helmr.vm-runtime.v1'", "apple_vz='{}'::jsonb"} {
		if _, err := d.Pool.Exec(ctx, "UPDATE vm_platforms SET "+assignment+" WHERE id=$1", id); err == nil {
			t.Fatalf("invalid ARM profile accepted: %s", assignment)
		}
	}
	if err := run(ctx, d.DSN, func(m *migrate.Migrate) error { return m.Steps(-1) }); err == nil {
		t.Fatal("downgrade accepted ARM rows")
	}
	if err := d.Pool.QueryRow(ctx, "SELECT arch FROM vm_platforms WHERE id=$1", id).Scan(&arch); err != nil || arch != "aarch64" {
		t.Fatal("failed downgrade damaged ARM profile", err)
	}
	// Migrate marks the version dirty after the rejected transaction. The test
	// explicitly restores only migration bookkeeping after proving row integrity.
	if err := run(ctx, d.DSN, func(m *migrate.Migrate) error { return m.Force(3) }); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, d.Pool, "DELETE FROM vm_platforms WHERE id=$1", id)
	if err := run(ctx, d.DSN, func(m *migrate.Migrate) error { return m.Steps(-1) }); err != nil {
		t.Fatal(err)
	}
	if err := Up(ctx, d.DSN); err != nil {
		t.Fatal(err)
	}
}
