package definition

import (
	"bytes"
	"testing"
)

func TestBuildPlanRequiresIndependentTarget(t *testing.T) {
	_, raw := fixtureBuildPlan(t)
	for _, target := range []RuntimeArchitecture{ArchitectureX8664, ArchitectureAArch64} {
		candidate := bytes.ReplaceAll(raw, []byte(`"architecture":"x86_64"`), []byte(`"architecture":"`+string(target)+`"`))
		plan, err := ParseBuildPlan(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateBuildPlanTarget(plan, target); err != nil {
			t.Fatal(err)
		}
		for _, wrong := range []RuntimeArchitecture{ArchitectureX8664, ArchitectureAArch64, "amd64", "arm64", ""} {
			if wrong != target && ValidateBuildPlanTarget(plan, wrong) == nil {
				t.Fatalf("%s plan accepted for %s", target, wrong)
			}
		}
		encoded, err := CanonicalBuildPlan(plan)
		if err != nil || !bytes.Equal(encoded, candidate) {
			t.Fatalf("canonical bytes changed: %v", err)
		}
	}
}

func TestBuildPlanRejectsMixedTargets(t *testing.T) {
	plan, _ := fixtureBuildPlan(t)
	arm := *plan.Definitions[1].Template
	arm.ImageBuild.Images = append([]ImageSpec(nil), arm.ImageBuild.Images...)
	for i := range arm.ImageBuild.Images {
		arm.ImageBuild.Images[i].Platform.Architecture = "aarch64"
	}
	plan.Definitions = append(plan.Definitions, Input{Kind: KindTemplate, DeclaredID: "zz-arm", Template: &arm})
	if ValidateBuildPlan(plan) == nil {
		t.Fatal("mixed template targets accepted")
	}
}
