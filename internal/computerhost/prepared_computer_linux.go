//go:build linux

package computerhost

import (
	"context"
	"github.com/helmrdotdev/helmr/internal/disk"
	"github.com/helmrdotdev/helmr/internal/nbd"
	"github.com/helmrdotdev/helmr/internal/vm"
	"github.com/helmrdotdev/helmr/internal/workerapi"
	"path/filepath"
)

func (p *PreparedMachines) attachAllocationVersion(ctx context.Context, local *disk.LocalVersion, dir string, identity workerapi.AllocationIdentity, size int64) (vm.ComputerDevice, error) {
	return disk.AttachDevice(ctx, local, nbd.Config{Helper: p.ComputerHelper, Devices: p.ComputerDevices, Arena: dir, Socket: filepath.Join(dir, "nbd.sock"), Size: size})
}
