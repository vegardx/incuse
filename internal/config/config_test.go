package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validYAML() string {
	return `
github:
  config_url: https://github.com/netwerk-io
  auth:
    mode: pat
    pat_file: /etc/incuse/github.pat
scale_sets:
  prefix: incuse
  runner_group: Default
  base_labels: ["self-hosted", "linux"]
  classes:
    - vcpus: 4
      memory_gib: 8
      disk_gib: 20
      arch: amd64
      max_runners: 4
incus:
  socket_path: /var/lib/incus/unix.socket
  project: incuse
  default_profile: incuse-runner
  storage_pool: runners
runner:
  release_cache_ttl: 1h
`
}

func TestParse_AppliesDefaults(t *testing.T) {
	cfg, err := Parse([]byte(validYAML()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if cfg.ScaleSets.RunnerGroup != "Default" {
		t.Errorf("runner group default: got %q", cfg.ScaleSets.RunnerGroup)
	}
	if cfg.Incus.Project != "incuse" {
		t.Errorf("incus project default: got %q", cfg.Incus.Project)
	}
	if cfg.Runner.ImageAlias != "ubuntu/24.04/cloud" {
		t.Errorf("image alias default: got %q", cfg.Runner.ImageAlias)
	}
	if cfg.Incus.StoragePool != "runners" {
		t.Errorf("storage pool: got %q", cfg.Incus.StoragePool)
	}
	if cfg.Runner.MaxParallelMints != 4 {
		t.Errorf("mint concurrency default: got %d", cfg.Runner.MaxParallelMints)
	}
	if cfg.Runner.RegistrationTimeout != 10*time.Minute {
		t.Errorf("registration timeout default: got %v", cfg.Runner.RegistrationTimeout)
	}
	if cfg.Runner.MaxJobDuration != 6*time.Hour {
		t.Errorf("max job duration default: got %v", cfg.Runner.MaxJobDuration)
	}
	if cfg.Runner.ReleaseCacheTTL == nil ||
		*cfg.Runner.ReleaseCacheTTL != time.Hour {
		t.Errorf("release cache ttl: got %v", cfg.Runner.ReleaseCacheTTL)
	}
	if cfg.Runner.InstanceType != InstanceTypeVM {
		t.Errorf("instance_type default: got %q, want %q", cfg.Runner.InstanceType, InstanceTypeVM)
	}
}

func TestParse_PreservesExplicitZeroReleaseCacheTTL(t *testing.T) {
	raw := strings.Replace(validYAML(),
		"release_cache_ttl: 1h", "release_cache_ttl: 0s", 1)
	cfg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Runner.ReleaseCacheTTL == nil ||
		*cfg.Runner.ReleaseCacheTTL != 0 {
		t.Fatalf("release cache ttl: got %v, want explicit zero",
			cfg.Runner.ReleaseCacheTTL)
	}
}

func TestValidate_InstanceType(t *testing.T) {
	cases := []struct {
		name    string
		mutator func(*Config)
		wantErr string
	}{
		{
			name:    "unknown instance type",
			mutator: func(c *Config) { c.Runner.InstanceType = "jail" },
			wantErr: "runner.instance_type",
		},
		{
			name: "privileged on vm",
			mutator: func(c *Config) {
				c.Runner.InstanceType = InstanceTypeVM
				c.Runner.Privileged = true
			},
			wantErr: "runner.privileged is only valid",
		},
		{
			name: "privileged on container is allowed",
			mutator: func(c *Config) {
				c.Runner.InstanceType = InstanceTypeContainer
				c.Runner.Privileged = true
			},
			wantErr: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Parse([]byte(validYAML()))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			tc.mutator(cfg)
			err = cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestParse_RejectsUnknownKeys(t *testing.T) {
	bad := strings.Replace(validYAML(), "max_runners: 4", "max_runers: 4", 1) // typo
	if _, err := Parse([]byte(bad)); err == nil {
		t.Fatal("want strict-mode error on unknown key, got nil")
	}
}

func TestValidate_RequiresAuthConfig(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			"missing config_url",
			strings.Replace(validYAML(), "config_url: https://github.com/netwerk-io", "config_url: \"\"", 1),
			"github.config_url is required",
		},
		{
			"unknown auth mode",
			strings.Replace(validYAML(), "mode: pat", "mode: oauth", 1),
			"github.auth.mode \"oauth\" must be pat or app",
		},
		{
			"missing pat file",
			strings.Replace(validYAML(), "pat_file: /etc/incuse/github.pat", "pat_file: \"\"", 1),
			"github.auth.pat_file is required when mode=pat",
		},
		{
			"app mode missing all fields",
			strings.NewReplacer(
				"mode: pat", "mode: app",
				"pat_file: /etc/incuse/github.pat", "",
			).Replace(validYAML()),
			"github.auth.app.client_id is required",
		},
		{
			"max_runners zero",
			strings.Replace(validYAML(), "max_runners: 4", "max_runners: 0", 1),
			"scale_sets.classes[0].max_runners must be > 0",
		},
		{
			"https without cert",
			strings.Replace(validYAML(),
				"socket_path: /var/lib/incus/unix.socket",
				"url: https://incus.example.com:8443", 1),
			"incus.cert_file is required when incus.url is set",
		},
		{
			"missing storage pool",
			strings.Replace(validYAML(), "storage_pool: runners", "storage_pool: \"\"", 1),
			"incus.storage_pool is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("want validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestLoad_FromDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(validYAML()), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	specs := cfg.ScaleSetSpecs()
	if len(specs) != 1 || specs[0].Name != "incuse-4vcpu-8gb-20gb-amd64" {
		t.Fatalf("specs: got %#v", specs)
	}
}

func TestExampleConfigParses(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/systemd/incuse.example.yaml")
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	if _, err := Parse(raw); err != nil {
		t.Fatalf("parse example: %v", err)
	}
}

func TestScaleSetSpecs_OneHomogeneousSetPerClass(t *testing.T) {
	cfg, err := Parse([]byte(strings.Replace(validYAML(),
		"      max_runners: 4",
		`      max_runners: 4
    - vcpus: 8
      memory_gib: 16
      disk_gib: 80
      arch: arm64
      max_runners: 2`, 1)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	specs := cfg.ScaleSetSpecs()
	if len(specs) != 2 {
		t.Fatalf("spec count: got %d, want 2", len(specs))
	}
	if got, want := specs[1].Name,
		"incuse-8vcpu-16gb-80gb-arm64"; got != want {
		t.Fatalf("second name: got %q, want %q", got, want)
	}
	if got := specs[1].Runner; got != (RunnerSpec{
		VCPUs: 8, MemoryMB: 16 * 1024, DiskGB: 80, Arch: ArchARM64,
	}) {
		t.Fatalf("second shape: got %#v", got)
	}
}
