//go:build darwin && arm64

package computerhost

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/helmrdotdev/helmr/internal/cas"
	"github.com/helmrdotdev/helmr/internal/disk"
	"github.com/helmrdotdev/helmr/internal/disk/blockformat"
	"github.com/helmrdotdev/helmr/internal/workerapi"
)

func nativeAllocationStorage(t *testing.T, longPath bool) (*PreparedMachines, workerapi.AllocationIdentity, workerapi.ComputerAllocationSource) {
	t.Helper()
	dir, err := os.MkdirTemp("/private/tmp", "hmd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	store, err := cas.NewFile(filepath.Join(dir, "cas"))
	if err != nil {
		t.Fatal(err)
	}
	keyID := "01912345-6789-7abc-8def-0123456789ab"
	key := bytes.Repeat([]byte{7}, 32)
	writer := blockformat.Writer{Source: store, Sink: store, Scope: "local", ActiveKey: keyID, Keys: map[string][]byte{keyID: key}, PackLimit: blockformat.MinPackLimit}
	locator, err := writer.Empty(t.Context(), 1<<20, 64)
	if err != nil {
		t.Fatal(err)
	}
	root, err := disk.NewVersionRoot(locator, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(dir, "work")
	if longPath {
		temp = filepath.Join(dir, strings.Repeat("x", 90))
	}
	machines := &PreparedMachines{TempDir: temp, ComputerRanges: store, ComputerStagingBytes: 32 << 20}
	id := workerapi.AllocationIdentity{Kind: "computer", AllocationID: "01950000-0000-7000-8000-000000000001", Epoch: 1}
	source := workerapi.ComputerAllocationSource{Disk: workerapi.ComputerSourceMaterial{Root: root, WriteKeyID: keyID, Keys: []workerapi.ComputerKeyMaterial{{Scope: "local", ID: keyID, Key: key}}}}
	return machines, id, source
}

func TestNativeAllocationStorageRetainsPartialAttachment(t *testing.T) {
	machines, id, source := nativeAllocationStorage(t, true)
	device, err := machines.prepareAllocationDevice(t.Context(), id, source)
	if err == nil || device == nil {
		t.Fatalf("expected retained partial attachment: %v %v", device, err)
	}
	ref := preparedMachineRef{id: id.AllocationID, epoch: id.Epoch}
	if machines.computerDevices[ref] != device {
		t.Fatal("failed attachment custody lost")
	}
	if err := machines.releaseComputerDevice(id.AllocationID, id.Epoch); err != nil {
		t.Fatal(err)
	}
}

func TestNativeAllocationStorageRejectsDuplicateAndStaleOwner(t *testing.T) {
	machines, id, source := nativeAllocationStorage(t, false)
	device, err := machines.prepareAllocationDevice(t.Context(), id, source)
	if err != nil {
		t.Fatal(err)
	}
	defer device.Close(context.Background())
	arena := machines.computerPreparationDirectory(id.AllocationID, id.Epoch)
	claim := filepath.Join(arena, "nbd-owner.json")
	original, err := os.ReadFile(claim)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := machines.prepareAllocationDevice(t.Context(), id, source); err == nil || second != nil {
		t.Fatal("duplicate allocation adopted")
	}
	if machines.computerDevices[preparedMachineRef{id: id.AllocationID, epoch: id.Epoch}] != device {
		t.Fatal("live owner replaced")
	}
	restarted := &PreparedMachines{TempDir: machines.TempDir, ComputerRanges: machines.ComputerRanges, ComputerStagingBytes: machines.ComputerStagingBytes}
	if second, err := restarted.prepareAllocationDevice(t.Context(), id, source); err == nil || second != nil {
		t.Fatal("stale arena adopted")
	}
	if err := restarted.releaseComputerDevice(id.AllocationID, id.Epoch); err == nil {
		t.Fatal("lost native ownership released")
	}
	if err := CheckComputerAttachmentsReleased(machines.TempDir, nil); err == nil {
		t.Fatal("unreconciled native arena qualified")
	}
	current, err := os.ReadFile(claim)
	if err != nil || !bytes.Equal(current, original) {
		t.Fatal("ownership evidence altered", err)
	}
	if err := device.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.releaseComputerDevice(id.AllocationID, id.Epoch); err == nil {
		t.Fatal("closed endpoint inferred physical exclusion")
	}
}

func TestNativeRecoveryStillRequiresQualifiedReconciler(t *testing.T) {
	if err := CheckComputerAttachmentsReleased(t.TempDir(), nil); err == nil {
		t.Fatal("unimplemented native recovery qualified")
	}
}
