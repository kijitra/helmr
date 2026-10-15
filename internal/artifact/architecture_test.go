package artifact

import (
	"strings"
	"testing"

	"github.com/helmrdotdev/helmr/internal/definition"
)

func TestProgramMetadataBindsTargetPlanAndSeeds(t *testing.T) {
	for _, target := range []definition.RuntimeArchitecture{definition.ArchitectureX8664, definition.ArchitectureAArch64} {
		t.Run(string(target), func(t *testing.T) {
			plan := testBuildPlan()
			for _, d := range plan.Definitions {
				if d.Template != nil {
					for i := range d.Template.ImageBuild.Images {
						d.Template.ImageBuild.Images[i].Platform.Architecture = string(target)
					}
				}
			}
			profile, err := target.SeedProfile()
			if err != nil {
				t.Fatal(err)
			}
			seed := definition.ComputerSeed{Profile: profile, Architecture: target, Digest: "sha256:" + strings.Repeat("d", 64), SizeBytes: 4096, MediaType: definition.ComputerSeedMediaType}
			build := func(expected definition.RuntimeArchitecture, s definition.ComputerSeed) (ProgramMetadata, error) {
				return BuildProgramMetadata(expected, plan, testDefinitionIndex(), map[string]definition.ComputerSeed{"repo": s}, "sha256:"+strings.Repeat("4", 64), "sha256:"+strings.Repeat("f", 64))
			}
			metadata, err := build(target, seed)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := CanonicalProgramMetadata(metadata)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseProgramMetadata(raw); err != nil {
				t.Fatal(err)
			}
			other := definition.ArchitectureX8664
			if target == other {
				other = definition.ArchitectureAArch64
			}
			if _, err := build(other, seed); err == nil {
				t.Fatal("wrong runtime target accepted")
			}
			swapped := seed
			swapped.Architecture = other
			if _, err := build(target, swapped); err == nil {
				t.Fatal("wrong seed architecture accepted")
			}
			swapped = seed
			swapped.Profile, _ = other.SeedProfile()
			if _, err := build(target, swapped); err == nil {
				t.Fatal("wrong seed profile accepted")
			}
			metadata.Architecture = other
			if ValidateProgramMetadata(metadata) == nil {
				t.Fatal("metadata relabeling accepted")
			}
		})
	}
}
