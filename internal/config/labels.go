package config

import (
	"fmt"
	"strings"
)

// Architectures incuse advertises to GitHub. Jobs that don't specify
// an arch label fall back to the host's runtime arch.
const (
	ArchAMD64 = "amd64"
	ArchARM64 = "arm64"
)

// RunnerSpec is the immutable shape shared by every runner in one scale set.
type RunnerSpec struct {
	VCPUs    int
	MemoryMB int
	DiskGB   int
	Arch     string
}

// ScaleSetName returns the only resource label for a homogeneous class.
func (c RunnerClassConfig) ScaleSetName(prefix string) string {
	return fmt.Sprintf("%s-%dvcpu-%dgb-%dgb-%s",
		prefix, c.VCPUs, c.MemoryGiB, c.DiskGiB, strings.ToLower(c.Arch))
}

// RunnerSpec converts a class from GiB config units to Incus MiB limits.
func (c RunnerClassConfig) RunnerSpec() RunnerSpec {
	return RunnerSpec{
		VCPUs:    c.VCPUs,
		MemoryMB: c.MemoryGiB * 1024,
		DiskGB:   c.DiskGiB,
		Arch:     strings.ToLower(c.Arch),
	}
}

// ScaleSetLabels adds the generated class name to shared routing labels.
func ScaleSetLabels(base []string, className string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(base)+1)
	add := func(l string) {
		key := strings.ToLower(l)
		if l == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, l)
	}
	for _, l := range base {
		add(l)
	}
	add(className)
	return out
}
