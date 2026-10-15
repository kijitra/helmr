//go:build linux

package builder

import (
	"context"

	"github.com/helmrdotdev/helmr/internal/bundle"
	"github.com/helmrdotdev/helmr/internal/definition"
	"github.com/helmrdotdev/helmr/internal/disk"
)

func buildComputerDisk(ctx context.Context, source, target, scratch, mkfs, config string) (bundle.ComputerSeedArtifact, error) {
	return buildComputerDiskForArchitecture(ctx, source, target, scratch, mkfs, config, definition.ArchitectureX8664)
}
func buildComputerDiskForArchitecture(ctx context.Context, source, target, scratch, mkfs, config string, architecture definition.RuntimeArchitecture) (bundle.ComputerSeedArtifact, error) {
	profile, err := architecture.SeedProfile()
	if err != nil {
		return bundle.ComputerSeedArtifact{}, err
	}
	seed, err := buildSeedForArchitecture(ctx, source, target, scratch, mkfs, config, disk.SeedCapacity, architecture)
	if err != nil {
		return bundle.ComputerSeedArtifact{}, err
	}
	return bundle.ComputerSeedArtifact{Architecture: architecture, Profile: profile, Config: seed.Config, Digest: seed.Artifact.Object.Digest, SizeBytes: seed.Artifact.Object.SizeBytes, MediaType: seed.Artifact.Object.MediaType}, nil
}
