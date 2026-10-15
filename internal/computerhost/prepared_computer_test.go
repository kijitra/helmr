package computerhost

import (
	"github.com/helmrdotdev/helmr/internal/reservation"
	"os"
	"path/filepath"
	"testing"
)

func TestLostComputerOwnerRetainsAttachmentEvidenceAndCapacity(t *testing.T) {
	for _, marker := range []string{"config.json", "claim.json", "nbd.sock", "nbd-owner.json"} {
		for _, symlink := range []bool{false, true} {
			t.Run(marker+map[bool]string{false: "-file", true: "-symlink"}[symlink], func(t *testing.T) {
				const id = "01950000-0000-7000-8000-000000000001"
				ledger, err := reservation.New(reservation.Vector{CPUMillis: 1, MemoryBytes: 1, HostDiskBytes: 1024})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = ledger.Reserve(instanceReservationKey(id, 1), reservation.Vector{HostDiskBytes: 1024}); err != nil {
					t.Fatal(err)
				}
				machines := &PreparedMachines{TempDir: t.TempDir(), Reservations: ledger}
				dir := machines.computerPreparationDirectory(id, 1)
				if err = os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				evidence := filepath.Join(dir, marker)
				var markerErr error
				if symlink {
					markerErr = os.Symlink("missing", evidence)
				} else {
					markerErr = os.WriteFile(evidence, []byte("retained helper claim"), 0600)
				}
				if err = markerErr; err != nil {
					t.Fatal(err)
				}
				if err = machines.releaseInstanceCapacity(id, 1); err == nil {
					t.Fatal("missing map treated as helper exclusion")
				}
				if _, err = os.Lstat(evidence); err != nil {
					t.Fatal("lost reconciliation evidence", err)
				}
				if len(ledger.Snapshot().Reservations) != 1 {
					t.Fatal("released uncertain reservation")
				}
			})
		}
	}
}

func TestInFlightComputerPreparationRetainsCapacity(t *testing.T) {
	machines := &PreparedMachines{preparingDevices: map[preparedMachineRef]bool{{id: "owner", epoch: 1}: true}}
	if err := machines.releaseComputerDevice("owner", 1); err == nil {
		t.Fatal("in-flight preparation released")
	}
}
