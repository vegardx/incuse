package config

import (
	"reflect"
	"testing"
)

func TestRunnerClassScaleSetName(t *testing.T) {
	class := RunnerClassConfig{
		VCPUs:     4,
		MemoryGiB: 8,
		DiskGiB:   20,
		Arch:      "AMD64",
	}
	if got, want := class.ScaleSetName("incuse"),
		"incuse-4vcpu-8gb-20gb-amd64"; got != want {
		t.Fatalf("name: got %q, want %q", got, want)
	}
}

func TestRunnerClassSpec(t *testing.T) {
	class := RunnerClassConfig{
		VCPUs:     4,
		MemoryGiB: 8,
		DiskGiB:   20,
		Arch:      "ARM64",
	}
	got := class.RunnerSpec()
	want := RunnerSpec{
		VCPUs:    4,
		MemoryMB: 8192,
		DiskGB:   20,
		Arch:     ArchARM64,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spec: got %#v, want %#v", got, want)
	}
}

func TestScaleSetLabelsDedupes(t *testing.T) {
	got := ScaleSetLabels(
		[]string{"self-hosted", "Linux", "linux", ""},
		"incuse-4vcpu-8gb-20gb-amd64",
	)
	want := []string{
		"self-hosted",
		"Linux",
		"incuse-4vcpu-8gb-20gb-amd64",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("labels: got %v, want %v", got, want)
	}
}
