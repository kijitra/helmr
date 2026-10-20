//go:build !linux

package builder

import (
	"context"
	"errors"

	"github.com/helmrdotdev/helmr/internal/bundle"
	"github.com/helmrdotdev/helmr/internal/definition"
)

func buildComputerDisk(context.Context, string, string, string, string, string) (bundle.ComputerSeedArtifact, error) {
	return bundle.ComputerSeedArtifact{}, errors.New("deployment disk generation requires the Linux builder")
}

func buildComputerDiskForArchitecture(context.Context, string, string, string, string, string, definition.RuntimeArchitecture) (bundle.ComputerSeedArtifact, error) {
	return bundle.ComputerSeedArtifact{}, errors.New("deployment disk generation requires the Linux builder")
}
