package definition

import "fmt"

// Architecture names in signed/canonical Helmr documents are not OCI aliases.
// Convert only at the tool boundary; keep existing x86 documents byte-identical.
func (a RuntimeArchitecture) Valid() bool {
	return a == ArchitectureX8664 || a == ArchitectureAArch64
}
func (a RuntimeArchitecture) OCIArchitecture() (string, error) {
	switch a {
	case ArchitectureX8664:
		return "amd64", nil
	case ArchitectureAArch64:
		return "arm64", nil
	}
	return "", fmt.Errorf("unsupported runtime architecture %q", a)
}
func (a RuntimeArchitecture) SeedProfile() (string, error) {
	switch a {
	case ArchitectureX8664:
		return ComputerSeedProfile, nil
	case ArchitectureAArch64:
		return ComputerSeedProfileAArch64, nil
	}
	return "", fmt.Errorf("unsupported runtime architecture %q", a)
}
func ArchitectureForSeedProfile(profile string) (RuntimeArchitecture, error) {
	switch profile {
	case ComputerSeedProfile:
		return ArchitectureX8664, nil
	case ComputerSeedProfileAArch64:
		return ArchitectureAArch64, nil
	}
	return "", fmt.Errorf("unsupported Computer seed profile %q", profile)
}

// ValidateBuildPlanTarget binds every image to an independently selected Runtime.
// BuildPlan's v0 serialized shape remains unchanged.
func ValidateBuildPlanTarget(plan BuildPlan, target RuntimeArchitecture) error {
	if !target.Valid() {
		return fmt.Errorf("unsupported runtime architecture %q", target)
	}
	if err := ValidateBuildPlan(plan); err != nil {
		return err
	}
	for _, input := range plan.Definitions {
		if input.Template != nil {
			if err := ValidateImageBuild(input.Template.ImageBuild, string(target)); err != nil {
				return err
			}
		}
	}
	return nil
}

func ImageBuildArchitecture(build ImageBuild) (RuntimeArchitecture, error) {
	if len(build.Images) == 0 {
		return "", fmt.Errorf("image build images must be non-empty")
	}
	a := RuntimeArchitecture(build.Images[0].Platform.Architecture)
	if err := ValidateImageBuild(build, string(a)); err != nil {
		return "", err
	}
	return a, nil
}
