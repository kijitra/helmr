package bundle

import (
	"os"
	"testing"

	"github.com/helmrdotdev/helmr/internal/artifact"
	"github.com/helmrdotdev/helmr/internal/definition"
)

// Compiler-produced ARM declarations must survive the real Go publication path.
func TestARMCompilerMetadataBundleContract(t *testing.T) {
	raw, err := os.ReadFile("../definition/testdata/agent-build-plan-arm64.json")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := definition.ParseBuildPlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	indexRaw, err := os.ReadFile("../artifact/testdata/definition-index.json")
	if err != nil {
		t.Fatal(err)
	}
	index, err := artifact.ParseDefinitionIndex(indexRaw)
	if err != nil {
		t.Fatal(err)
	}
	b := testDeploymentBundle(t)
	b.Platform.Architecture = definition.ArchitectureAArch64
	seed := &b.ComputerSeeds[0]
	seed.DeclaredID = "workspace"
	seed.Artifact.Architecture = definition.ArchitectureAArch64
	seed.Artifact.Profile = definition.ComputerSeedProfileAArch64
	metadata, err := artifact.BuildProgramMetadata(definition.ArchitectureAArch64, plan, index, map[string]definition.ComputerSeed{"workspace": {
		Architecture: seed.Artifact.Architecture, Profile: seed.Artifact.Profile, Digest: seed.Artifact.Digest, SizeBytes: seed.Artifact.SizeBytes, MediaType: seed.Artifact.MediaType,
	}}, b.Program.Metadata.ConfigResultDigest, b.Runtime.Artifact.Digest)
	if err != nil {
		t.Fatal(err)
	}
	b.Program.Metadata = metadata
	b.Plan.Definitions = metadata.Definitions
	encoded, err := Canonical(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(encoded); err != nil {
		t.Fatal(err)
	}
	admission := Admission{Runtime: artifact.RuntimeDescriptor{Architecture: definition.ArchitectureAArch64, Digest: b.Runtime.Artifact.Digest, SizeBytes: b.Runtime.Artifact.SizeBytes, MediaType: b.Runtime.Artifact.MediaType, RuntimeContract: b.Runtime.Contract, FormatVersion: artifact.RuntimeDescriptorFormatVersion}}
	if err := admission.Admit(b); err != nil {
		t.Fatal(err)
	}
	admission.Runtime.Architecture = definition.ArchitectureX8664
	if admission.Admit(b) == nil {
		t.Fatal("wrong host Runtime accepted")
	}
	for name, mutate := range map[string]func(*Manifest){
		"platform":          func(b *Manifest) { b.Platform.Architecture = definition.ArchitectureX8664 },
		"program":           func(b *Manifest) { b.Program.Metadata.Architecture = definition.ArchitectureX8664 },
		"seed architecture": func(b *Manifest) { b.ComputerSeeds[0].Artifact.Architecture = definition.ArchitectureX8664 },
		"seed profile":      func(b *Manifest) { b.ComputerSeeds[0].Artifact.Profile = definition.ComputerSeedProfile },
	} {
		t.Run(name, func(t *testing.T) {
			candidate, err := Parse(encoded)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&candidate)
			if validate(candidate) == nil {
				t.Fatal("cross architecture bundle accepted")
			}
		})
	}
}
