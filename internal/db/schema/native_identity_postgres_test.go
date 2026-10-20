package schema

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/helmrdotdev/helmr/internal/db"
	"github.com/helmrdotdev/helmr/internal/db/dbtest"
	"github.com/helmrdotdev/helmr/internal/pgvalue"
	"github.com/helmrdotdev/helmr/internal/vmplatform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestNativeIdentityMigrationAndConflictWithPostgres(t *testing.T) {
	database := dbtest.Open(t)
	ctx := t.Context()
	if err := run(ctx, database.DSN, func(m *migrate.Migrate) error { return m.Migrate(1) }); err != nil {
		t.Fatal(err)
	}
	legacy := dbtest.Digest("legacy-runtime")
	dbtest.MustExec(t, ctx, database.Pool, `INSERT INTO vm_platforms(id,descriptor_digest,firecracker_digest,firecracker_version,snapshot_format_version,host_kernel_release,kernel_digest,initramfs_digest,rootfs_digest) VALUES($1,$1,$1,'1.16.1','6.0.0','6.8.0',$1,$1,$1)`, legacy)
	if err := Up(ctx, database.DSN); err != nil {
		t.Fatal(err)
	}
	var backend, arch, contract, id string
	if err := database.Pool.QueryRow(ctx, `SELECT id,backend,arch,contract FROM vm_platforms WHERE id=$1`, legacy).Scan(&id, &backend, &arch, &contract); err != nil {
		t.Fatal(err)
	}
	if id != legacy || backend != "firecracker" || arch != "x86_64" || contract != vmplatform.Contract {
		t.Fatal("legacy row backfill changed identity")
	}
	raw, err := os.ReadFile("../../vmplatform/testdata/apple-vz-profile.json")
	if err != nil {
		t.Fatal(err)
	}
	var p vmplatform.Profile
	if err = json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	native, err := json.Marshal(p.AppleVZ)
	if err != nil {
		t.Fatal(err)
	}
	arg := db.UpsertVMPlatformParams{ID: p.ID, DescriptorDigest: p.VMRuntimeDescriptorDigest, Backend: p.Backend, Arch: p.Arch, Contract: p.Contract, AppleVz: native, HostKernelRelease: p.HostKernelRelease, KernelDigest: p.KernelDigest, InitramfsDigest: p.InitramfsDigest, RootfsDigest: p.RootfsDigest}
	q := db.New(database.Pool)
	for range 2 {
		row, err := q.UpsertVMPlatform(ctx, arg)
		if err != nil {
			t.Fatal(err)
		}
		if row.FirecrackerDigest.Valid || row.SnapshotFormatVersion.Valid || row.Backend != p.Backend {
			t.Fatal("native identity stored in legacy fields")
		}
	}
	for _, field := range []string{"descriptor", "kernel", "initramfs", "rootfs", "host", "native", "backend"} {
		t.Run(field, func(t *testing.T) {
			changed := arg
			switch field {
			case "backend":
				changed.Backend, changed.Arch, changed.Contract = "firecracker", "x86_64", vmplatform.Contract
				changed.AppleVz = nil
				changed.FirecrackerDigest = pgvalue.Text(legacy)
				changed.FirecrackerVersion = pgvalue.Text("1.16.1")
				changed.SnapshotFormatVersion = pgvalue.Text("6.0.0")
			case "descriptor":
				changed.DescriptorDigest = legacy
			case "kernel":
				changed.KernelDigest = legacy
			case "initramfs":
				changed.InitramfsDigest = legacy
			case "rootfs":
				changed.RootfsDigest = legacy
			case "host":
				changed.HostKernelRelease = "different"
			case "native":
				v := *p.AppleVZ
				v.BootSessionDigest = legacy
				changed.AppleVz, _ = json.Marshal(v)
			}
			if _, err := q.UpsertVMPlatform(ctx, changed); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("identity conflict result = %v", err)
			}
		})
	}
	// A failed downgrade must leave the full identity intact, not delete/coerce it.
	if err := Down(ctx, database.DSN); err == nil {
		t.Fatal("downgrade accepted native rows")
	}
	var retained bool
	if err := database.Pool.QueryRow(ctx, `SELECT apple_vz=$2::jsonb FROM vm_platforms WHERE id=$1`, p.ID, native).Scan(&retained); err != nil || !retained {
		t.Fatalf("downgrade changed native identity: %v", err)
	}
}

func TestNativeIdentitySQLRejectsIncompletePayloads(t *testing.T) {
	database := dbtest.Open(t)
	ctx := t.Context()
	if err := Up(ctx, database.DSN); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../vmplatform/testdata/apple-vz-profile.json")
	if err != nil {
		t.Fatal(err)
	}
	var p vmplatform.Profile
	if err = json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	native, _ := json.Marshal(p.AppleVZ)
	dbtest.MustExec(t, ctx, database.Pool, `INSERT INTO vm_platforms(id,descriptor_digest,backend,arch,contract,apple_vz,host_kernel_release,kernel_digest,initramfs_digest,rootfs_digest) VALUES($1,$1,'apple-vz','aarch64','helmr.vm-runtime.v1',$2,'25.6.0',$1,$1,$1)`, p.ID, native)
	cases := []string{
		"backend='unknown'", "arch='x86_64'", "contract='helmr.vm-runtime.v0'", "apple_vz=NULL", "apple_vz='null'::jsonb", "apple_vz='[]'::jsonb", "apple_vz='{}'::jsonb",
		"firecracker_digest=id", "firecracker_version='1.16.1'", "snapshot_format_version='6.0.0'", "cpu_template_digest=id",
		"apple_vz=apple_vz || '{\"unknown\":true}'::jsonb",
	}
	for _, field := range []string{"helper_digest", "installation_digest", "key_domain_digest", "boot_session_digest", "os_build", "hardware_model", "state_format"} {
		cases = append(cases, "apple_vz=apple_vz-'"+field+"'", "apple_vz=jsonb_set(apple_vz,'{"+field+"}','null')", "apple_vz=jsonb_set(apple_vz,'{"+field+"}','42')")
	}
	for _, set := range cases {
		t.Run(set, func(t *testing.T) {
			_, err := database.Pool.Exec(ctx, "UPDATE vm_platforms SET "+set+" WHERE id=$1", p.ID)
			var pgerr *pgconn.PgError
			if !errors.As(err, &pgerr) || pgerr.Code != "23514" {
				t.Fatalf("invalid native row error=%v", err)
			}
		})
	}
	// Downgrade only migration 2 and prove legacy identity survives unchanged.
	dbtest.MustExec(t, ctx, database.Pool, "DELETE FROM vm_platforms WHERE id=$1", p.ID)
	legacy := dbtest.Digest("retained-legacy-runtime")
	dbtest.MustExec(t, ctx, database.Pool, `INSERT INTO vm_platforms(id,descriptor_digest,firecracker_digest,firecracker_version,snapshot_format_version,host_kernel_release,kernel_digest,initramfs_digest,rootfs_digest) VALUES($1,$1,$1,'1.16.1','6.0.0','6.8.0',$1,$1,$1)`, legacy)
	if err := run(ctx, database.DSN, func(m *migrate.Migrate) error { return m.Migrate(1) }); err != nil {
		t.Fatal(err)
	}
	var retained string
	if err := database.Pool.QueryRow(ctx, "SELECT firecracker_digest FROM vm_platforms WHERE id=$1", legacy).Scan(&retained); err != nil || retained != legacy {
		t.Fatalf("legacy identity changed on downgrade: %v", err)
	}
	if err := Up(ctx, database.DSN); err != nil {
		t.Fatal(err)
	}
}
