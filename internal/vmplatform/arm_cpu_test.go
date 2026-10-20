package vmplatform

import (
	"encoding/json"
	"strings"
	"testing"
)

const armRecord = "processor : 0\nFeatures : fp asimd aes\nCPU implementer : 0x61\nCPU architecture : 8\nCPU variant : 0x0\nCPU part : 0x000\nCPU revision : 0\n"

func TestARMCPUInfoCanonicalAndComplete(t *testing.T) {
	e, err := ParseARMCPUInfo([]byte(armRecord + "\n" + strings.Replace(armRecord, "processor : 0", "processor : 1", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Processors) != 2 || e.Processors[0].Features != "aes asimd fp" || e.Processors[0].Part != "0x0" {
		t.Fatalf("unexpected ARM evidence: %+v", e)
	}
	cloned := e.Clone()
	cloned.Processors[0].Revision = "1"
	if e.Processors[0].Revision != "0" {
		t.Fatal("CPU evidence alias")
	}
	raw, _ := json.Marshal(e)
	var round ARMCPUEnvironment
	if json.Unmarshal(raw, &round) != nil || round.Validate() != nil {
		t.Fatal("CPU evidence roundtrip")
	}
	p := testProfile(t)
	old := p.ID
	p.Arch = "aarch64"
	p.ID, err = p.ExpectedID()
	if err != nil || p.Validate() != nil || p.ID == old {
		t.Fatalf("ARM identity failed: %v", err)
	}
}
func TestARMCPUInfoRejectsIncompleteAndMixedProcessors(t *testing.T) {
	for name, raw := range map[string]string{
		"missing":             strings.Replace(armRecord, "CPU revision : 0\n", "", 1),
		"duplicate":           armRecord + "CPU revision : 0\n",
		"duplicate_processor": armRecord + "\n" + armRecord,
		"gap":                 strings.Replace(armRecord, "processor : 0", "processor : 2", 1),
		"empty_features":      strings.Replace(armRecord, "fp asimd aes", "", 1),
		"overflow":            strings.Replace(armRecord, "0x61", "0x161", 1),
		"malformed":           armRecord + "broken\n",
		"empty":               "",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseARMCPUInfo([]byte(raw)); err == nil {
				t.Fatal("malformed ARM CPU evidence accepted")
			}
		})
	}
}
