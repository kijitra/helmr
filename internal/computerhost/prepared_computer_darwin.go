//go:build darwin && arm64

package computerhost

import (
	"context"
	"github.com/helmrdotdev/helmr/internal/disk"
	"github.com/helmrdotdev/helmr/internal/vm"
	"github.com/helmrdotdev/helmr/internal/workerapi"
)

// This only prepares storage. Worker admission still requires a qualified
// native backend and recovery; a Unix endpoint does not authorize execution.
func (p *PreparedMachines) attachAllocationVersion(ctx context.Context, local *disk.LocalVersion, dir string, identity workerapi.AllocationIdentity, size int64) (vm.ComputerDevice, error) {
	return disk.AttachUnixDevice(ctx, local, disk.UnixDeviceConfig{Arena: dir, Owner: identity.AllocationID, Generation: identity.Epoch})
}
