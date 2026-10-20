package vmplatform

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ARMCPUEnvironment records the CPUs exposed by the Linux host. Like legacy
// microcode evidence, this is measured compatibility data, not attestation.
// Ordered per-CPU records retain heterogeneous hosts without selecting one CPU.
type ARMCPUEnvironment struct {
	Architecture string         `json:"architecture"`
	Processors   []ARMProcessor `json:"processors"`
}
type ARMProcessor struct {
	Implementer  string `json:"implementer"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant"`
	Part         string `json:"part"`
	Revision     string `json:"revision"`
	Features     string `json:"features"`
}

var armFeature = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func (e *ARMCPUEnvironment) Clone() *ARMCPUEnvironment {
	if e == nil {
		return nil
	}
	result := *e
	result.Processors = slices.Clone(e.Processors)
	return &result
}
func (e ARMCPUEnvironment) Validate() error {
	if e.Architecture != "aarch64" || len(e.Processors) == 0 || len(e.Processors) > 4096 {
		return errors.New("invalid ARM CPU architecture or processor count")
	}
	for _, p := range e.Processors {
		for _, f := range []struct {
			value string
			bits  int
			hex   bool
		}{{p.Implementer, 8, true}, {p.Architecture, 8, false}, {p.Variant, 4, true}, {p.Part, 12, true}, {p.Revision, 4, false}} {
			base := 10
			input := f.value
			if f.hex {
				base = 16
				if !strings.HasPrefix(input, "0x") {
					return errors.New("ARM CPU hexadecimal field lacks prefix")
				}
				input = strings.TrimPrefix(input, "0x")
			}
			n, err := strconv.ParseUint(input, base, f.bits)
			canonical := strconv.FormatUint(n, base)
			if f.hex {
				canonical = "0x" + canonical
			}
			if err != nil || canonical != f.value {
				return errors.New("ARM CPU numeric field is not canonical")
			}
		}
		if p.Architecture != "8" && p.Architecture != "9" {
			return errors.New("unsupported ARM CPU architecture")
		}
		fields := strings.Fields(p.Features)
		if len(fields) == 0 || len(p.Features) > 8192 || strings.Join(fields, " ") != p.Features {
			return errors.New("invalid ARM CPU features")
		}
		for i, feature := range fields {
			if !armFeature.MatchString(feature) || (i > 0 && fields[i-1] >= feature) {
				return errors.New("ARM CPU features must be a canonical sorted set")
			}
		}
	}
	return nil
}

// ParseARMCPUInfo requires complete, uniquely numbered processor records.
// Unknown diagnostic keys are ignored, but duplicate keys are never accepted.
func ParseARMCPUInfo(raw []byte) (*ARMCPUEnvironment, error) {
	if len(raw) == 0 || len(raw) > 4<<20 || !utf8.Valid(raw) {
		return nil, errors.New("ARM cpuinfo is empty or unbounded")
	}
	result := &ARMCPUEnvironment{Architecture: "aarch64"}
	for _, block := range strings.Split(strings.TrimSpace(string(raw)), "\n\n") {
		values := map[string]string{}
		for line := range strings.SplitSeq(block, "\n") {
			name, value, ok := strings.Cut(line, ":")
			if !ok {
				return nil, errors.New("malformed ARM cpuinfo line")
			}
			name = strings.TrimSpace(name)
			if _, exists := values[name]; exists {
				return nil, errors.New("duplicate ARM cpuinfo field")
			}
			values[name] = strings.TrimSpace(value)
		}
		if values["processor"] != strconv.Itoa(len(result.Processors)) {
			return nil, errors.New("ARM cpuinfo processor numbers must be complete and ordered")
		}
		numeric := func(key string, base, bits int) (string, error) {
			v, ok := values[key]
			if !ok || v == "" {
				return "", fmt.Errorf("missing ARM cpuinfo %s", key)
			}
			n, err := strconv.ParseUint(v, base, bits)
			if err != nil {
				return "", err
			}
			if base == 0 {
				return "0x" + strconv.FormatUint(n, 16), nil
			}
			return strconv.FormatUint(n, 10), nil
		}
		var p ARMProcessor
		var err error
		for _, field := range []struct {
			key        string
			base, bits int
			dst        *string
		}{{"CPU implementer", 0, 8, &p.Implementer}, {"CPU architecture", 10, 8, &p.Architecture}, {"CPU variant", 0, 4, &p.Variant}, {"CPU part", 0, 12, &p.Part}, {"CPU revision", 10, 4, &p.Revision}} {
			*field.dst, err = numeric(field.key, field.base, field.bits)
			if err != nil {
				return nil, err
			}
		}
		features := strings.Fields(values["Features"])
		slices.Sort(features)
		p.Features = strings.Join(slices.Compact(features), " ")
		result.Processors = append(result.Processors, p)
	}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return result, nil
}
