//go:build linux || (darwin && arm64)

package computerhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/helmrdotdev/helmr/internal/disk"
	"github.com/helmrdotdev/helmr/internal/disk/blockformat"
	"github.com/helmrdotdev/helmr/internal/ids"
	"github.com/helmrdotdev/helmr/internal/vm"
	"github.com/helmrdotdev/helmr/internal/workerapi"
)

// Allocation owners serialize the entire prepare/release lifecycle with their
// operation mutex. This in-flight map additionally refuses overlapping preparation
// and release attempts; it does not replace that lifecycle owner.
func (p *PreparedMachines) prepareAllocationDevice(ctx context.Context, identity workerapi.AllocationIdentity, source workerapi.ComputerAllocationSource) (vm.ComputerDevice, error) {
	if p == nil || identity.Epoch <= 0 || ids.Validate(identity.AllocationID) != nil {
		return nil, errors.New("invalid Computer device allocation identity")
	}
	ref := preparedMachineRef{id: identity.AllocationID, epoch: identity.Epoch}
	p.mu.Lock()
	if p.preparingDevices[ref] || p.computerDevices[ref] != nil {
		p.mu.Unlock()
		return nil, errors.New("Computer device allocation already owned")
	}
	if p.preparingDevices == nil {
		p.preparingDevices = make(map[preparedMachineRef]bool)
	}
	p.preparingDevices[ref] = true
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.preparingDevices, ref); p.mu.Unlock() }()

	material := source.Disk
	if err := material.Root.Validate(material.Root.LogicalBytes); err != nil {
		return nil, err
	}
	if len(material.Keys) == 0 {
		return nil, errors.New("computer allocation source has no keys")
	}
	keys := make(map[string][]byte, len(material.Keys))
	scope := material.Keys[0].Scope
	for _, key := range material.Keys {
		if key.Scope != scope || keys[key.ID] != nil {
			return nil, errors.New("computer allocation key scope or identity changed")
		}
		keys[key.ID] = key.Key
	}
	if _, err := disk.OpenVersion(ctx, p.ComputerRanges, scope, keys, material.Root, material.Root.LogicalBytes); err != nil {
		return nil, disk.PublishedSourceFailure(err)
	}
	dir := p.computerPreparationDirectory(identity.AllocationID, identity.Epoch)
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	local, err := disk.CreateLocalVersion(ctx, disk.LocalVersionConfig{Directory: filepath.Join(dir, "version"), Base: material.Root, BaseSource: p.ComputerRanges, Scope: scope, ActiveKey: material.WriteKeyID, Keys: keys, DirtyBlocks: 256, StagedBytes: p.ComputerStagingBytes, PackLimit: blockformat.MinPackLimit})
	if err != nil {
		return nil, err
	}
	device, err := p.attachAllocationVersion(ctx, local, dir, identity, material.Root.LogicalBytes)
	if device != nil {
		p.retainComputerDevice(identity.AllocationID, identity.Epoch, device)
	}
	return device, err
}

// Retain before attachment can fail. Close is safe both before binding and after
// backend cleanup, but refuses release while a bound consumer may still live.
func (p *PreparedMachines) retainComputerDevice(id string, epoch int64, device vm.ComputerDevice) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.computerDevices == nil {
		p.computerDevices = make(map[preparedMachineRef]vm.ComputerDevice)
	}
	p.computerDevices[preparedMachineRef{id: id, epoch: epoch}] = device
}
