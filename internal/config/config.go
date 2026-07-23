// Package config carries the on-disk YAML schema, Load+validate plumbing,
// and expansion of configured runner classes into scale-set specifications.
//
// Schema lives in /etc/incuse/config.yaml on a deployed host. Defaults
// match the plan's MVP target — Unix-socket Incus access, ubuntu/24.04
// VMs and the runner-group named "Default". A minimal config supplies
// GitHub auth, runner classes, and the explicit Incus storage pool.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

// Config is the top-level schema. Loaded with Load.
type Config struct {
	GitHub        GitHubConfig        `yaml:"github"`
	ScaleSets     ScaleSetsConfig     `yaml:"scale_sets"`
	Incus         IncusConfig         `yaml:"incus"`
	Runner        RunnerConfig        `yaml:"runner"`
	Observability ObservabilityConfig `yaml:"observability"`
}

// GitHubConfig points incuse at one GitHub org/repo/enterprise scope and
// names the auth strategy.
type GitHubConfig struct {
	// ConfigURL is the per-org / per-repo / per-enterprise URL the
	// scaleset client expects, e.g. https://github.com/netwerk-io.
	ConfigURL string     `yaml:"config_url"`
	Auth      AuthConfig `yaml:"auth"`
}

// AuthConfig selects between PAT and GitHub App authentication. Both
// code paths exist; the operator picks one in YAML.
type AuthConfig struct {
	// Mode is "pat" or "app".
	Mode    string         `yaml:"mode"`
	PATFile string         `yaml:"pat_file"`
	App     AppCredentials `yaml:"app"`
}

// AppCredentials is the GitHub App input. PrivateKeyFile is read from
// disk at startup and the contents fed to the upstream client.
type AppCredentials struct {
	ClientID       string `yaml:"client_id"`
	PrivateKeyFile string `yaml:"private_key_file"`
	InstallationID int64  `yaml:"installation_id"`
}

// ScaleSetsConfig describes the homogeneous runner classes incuse owns.
// Each class becomes a distinct GitHub Runner Scale Set.
type ScaleSetsConfig struct {
	Prefix      string              `yaml:"prefix"`
	RunnerGroup string              `yaml:"runner_group"`
	BaseLabels  []string            `yaml:"base_labels"`
	Classes     []RunnerClassConfig `yaml:"classes"`
}

// RunnerClassConfig is one fixed runner shape and its independent capacity.
type RunnerClassConfig struct {
	VCPUs      int    `yaml:"vcpus"`
	MemoryGiB  int    `yaml:"memory_gib"`
	DiskGiB    int    `yaml:"disk_gib"`
	Arch       string `yaml:"arch"`
	MaxRunners int    `yaml:"max_runners"`
}

// ScaleSetConfig is the derived runtime specification for one class.
type ScaleSetConfig struct {
	Name        string   `yaml:"name"`
	RunnerGroup string   `yaml:"runner_group"`
	BaseLabels  []string `yaml:"base_labels"`
	MaxRunners  int      `yaml:"max_runners"`
	Runner      RunnerSpec
}

// IncusConfig is the input to internal/incus.Connect. URL and
// SocketPath are mutually exclusive — empty URL selects Unix socket.
type IncusConfig struct {
	URL                string        `yaml:"url"`
	SocketPath         string        `yaml:"socket_path"`
	CertFile           string        `yaml:"cert_file"`
	KeyFile            string        `yaml:"key_file"`
	ServerCertFile     string        `yaml:"server_cert_file"`
	InsecureSkipVerify bool          `yaml:"insecure_skip_verify"`
	Project            string        `yaml:"project"`
	DefaultProfile     string        `yaml:"default_profile"`
	StoragePool        string        `yaml:"storage_pool"`
	RequestTimeout     time.Duration `yaml:"request_timeout"`
}

// RunnerConfig describes the per-instance VM shape and the
// actions/runner tarball the cloud-init template installs.
type RunnerConfig struct {
	ImageServer         string         `yaml:"image_server"`
	ImageProtocol       string         `yaml:"image_protocol"`
	ImageAlias          string         `yaml:"image_alias"`
	WorkFolder          string         `yaml:"work_folder"`
	RegistrationTimeout time.Duration  `yaml:"registration_timeout"`
	MaxJobDuration      time.Duration  `yaml:"max_job_duration"`
	ReleaseCacheTTL     *time.Duration `yaml:"release_cache_ttl"`
	MaxParallelMints    int            `yaml:"max_parallel_mints"`
	MaxParallelLaunches int            `yaml:"max_parallel_launches"`

	// InstanceType selects between Incus virtual-machine (default,
	// hypervisor-isolated, ~30s cold-boot) and system container
	// (shared kernel, ~2s cold-boot, weaker isolation). Valid values:
	// "vm" (default) or "container".
	InstanceType string `yaml:"instance_type"`

	// Privileged elevates a container runner to security.privileged=true.
	// Ignored when InstanceType is vm. This is the "insecure" docker
	// option — a compromised job can affect the host kernel. Only
	// appropriate for trusted internal workloads where the speed of
	// container start outweighs the loss of hypervisor isolation.
	Privileged bool `yaml:"privileged"`

	// UseBakedImage tells the orchestrator to use the minimal
	// cloud-init template that assumes actions/runner, the runner
	// user, packages, and the systemd unit are pre-installed on the
	// image. Build the image with scripts/build-runner-image.sh.
	UseBakedImage bool `yaml:"use_baked_image"`
}

// Instance types.
const (
	InstanceTypeVM        = "vm"
	InstanceTypeContainer = "container"
)

// Auth modes.
const (
	AuthModePAT = "pat"
	AuthModeApp = "app"
)

// ObservabilityConfig turns on the HTTP server that exposes /healthz,
// /readyz, and /metrics. ListenAddr empty disables the server
// entirely — incuse runs perfectly fine without metrics, the unit
// just gets harder to monitor.
type ObservabilityConfig struct {
	// ListenAddr is a Go net.Listen address (e.g. ":9090",
	// "127.0.0.1:9090"). Empty disables the server.
	ListenAddr string `yaml:"listen_addr"`
}

// Load reads the YAML file at path, populates defaults, validates, and
// returns the result. ENOENT is reported as a typed error so callers
// can distinguish "no config" from "broken config".
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}
	cfg, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("config: parse %q: %w", path, err)
	}
	return cfg, nil
}

// Parse decodes raw YAML, applies defaults, and validates. Exposed so
// tests don't have to round-trip through the filesystem.
func Parse(raw []byte) (*Config, error) {
	cfg := &Config{}
	if err := yaml.UnmarshalStrict(raw, cfg); err != nil {
		return nil, fmt.Errorf("decode yaml: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.ScaleSets.Prefix == "" {
		c.ScaleSets.Prefix = "incuse"
	}
	if c.ScaleSets.RunnerGroup == "" {
		c.ScaleSets.RunnerGroup = "Default"
	}
	if c.Incus.SocketPath == "" && c.Incus.URL == "" {
		c.Incus.SocketPath = "/var/lib/incus/unix.socket"
	}
	if c.Incus.Project == "" {
		c.Incus.Project = "incuse"
	}
	if c.Incus.DefaultProfile == "" {
		c.Incus.DefaultProfile = "incuse-runner"
	}
	if c.Incus.RequestTimeout == 0 {
		c.Incus.RequestTimeout = 10 * time.Minute
	}
	if c.Runner.UseBakedImage {
		// Baked-image flow: alias resolves locally on the Incus daemon.
		// Leave ImageServer/Protocol empty so the daemon doesn't try a
		// remote simplestreams lookup.
		if c.Runner.ImageAlias == "" {
			c.Runner.ImageAlias = "incuse-runner"
		}
	} else {
		if c.Runner.ImageServer == "" {
			c.Runner.ImageServer = "https://images.linuxcontainers.org"
		}
		if c.Runner.ImageProtocol == "" {
			c.Runner.ImageProtocol = "simplestreams"
		}
		if c.Runner.ImageAlias == "" {
			c.Runner.ImageAlias = "ubuntu/24.04/cloud"
		}
	}
	if c.Runner.WorkFolder == "" {
		c.Runner.WorkFolder = "_work"
	}
	if c.Runner.RegistrationTimeout == 0 {
		c.Runner.RegistrationTimeout = 10 * time.Minute
	}
	if c.Runner.MaxJobDuration == 0 {
		c.Runner.MaxJobDuration = 6 * time.Hour
	}
	if c.Runner.ReleaseCacheTTL == nil {
		ttl := time.Hour
		c.Runner.ReleaseCacheTTL = &ttl
	}
	if c.Runner.MaxParallelMints == 0 {
		c.Runner.MaxParallelMints = 4
	}
	if c.Runner.MaxParallelLaunches == 0 {
		c.Runner.MaxParallelLaunches = 2
	}
	if c.Runner.InstanceType == "" {
		c.Runner.InstanceType = InstanceTypeVM
	}
}

// Validate is exported so callers can re-check after programmatic
// mutation (the systemd `--validate` preflight, primarily).
func (c *Config) Validate() error {
	var errs []string
	check := func(cond bool, msg string) {
		if !cond {
			errs = append(errs, msg)
		}
	}

	check(c.GitHub.ConfigURL != "", "github.config_url is required")
	if c.GitHub.ConfigURL != "" {
		if u, err := url.Parse(c.GitHub.ConfigURL); err != nil || u.Scheme == "" || u.Host == "" {
			errs = append(errs, fmt.Sprintf("github.config_url %q is not a valid URL", c.GitHub.ConfigURL))
		}
	}

	switch c.GitHub.Auth.Mode {
	case AuthModePAT:
		check(c.GitHub.Auth.PATFile != "", "github.auth.pat_file is required when mode=pat")
	case AuthModeApp:
		check(c.GitHub.Auth.App.ClientID != "", "github.auth.app.client_id is required when mode=app")
		check(c.GitHub.Auth.App.PrivateKeyFile != "", "github.auth.app.private_key_file is required when mode=app")
		check(c.GitHub.Auth.App.InstallationID != 0, "github.auth.app.installation_id is required when mode=app")
	case "":
		errs = append(errs, "github.auth.mode is required (pat|app)")
	default:
		errs = append(errs, fmt.Sprintf("github.auth.mode %q must be pat or app", c.GitHub.Auth.Mode))
	}

	check(validNamePrefix(c.ScaleSets.Prefix),
		"scale_sets.prefix must contain lowercase letters, numbers, and dashes, start with a letter, and not end with a dash")
	check(len(c.ScaleSets.Classes) > 0,
		"scale_sets.classes must contain at least one runner class")
	seenClasses := make(map[string]struct{}, len(c.ScaleSets.Classes))
	bakedArch := ""
	for i, class := range c.ScaleSets.Classes {
		check(class.VCPUs > 0,
			fmt.Sprintf("scale_sets.classes[%d].vcpus must be > 0", i))
		check(class.MemoryGiB > 0,
			fmt.Sprintf("scale_sets.classes[%d].memory_gib must be > 0", i))
		check(class.DiskGiB > 0,
			fmt.Sprintf("scale_sets.classes[%d].disk_gib must be > 0", i))
		check(class.MaxRunners > 0,
			fmt.Sprintf("scale_sets.classes[%d].max_runners must be > 0", i))
		check(class.Arch == ArchAMD64 || class.Arch == ArchARM64,
			fmt.Sprintf("scale_sets.classes[%d].arch must be amd64 or arm64", i))
		name := class.ScaleSetName(c.ScaleSets.Prefix)
		check(len(name) <= maxScaleSetNameLen,
			fmt.Sprintf("scale_sets.classes[%d] generates name %q longer than %d characters", i, name, maxScaleSetNameLen))
		if _, exists := seenClasses[name]; exists {
			errs = append(errs, fmt.Sprintf(
				"scale_sets.classes[%d] duplicates runner class %q", i, name))
		}
		seenClasses[name] = struct{}{}
		if c.Runner.UseBakedImage {
			if bakedArch == "" {
				bakedArch = class.Arch
			}
			check(class.Arch == bakedArch,
				"runner.use_baked_image requires every class to use one architecture because image_alias resolves to one local image")
		}
	}

	if c.Incus.URL != "" {
		check(c.Incus.CertFile != "", "incus.cert_file is required when incus.url is set")
		check(c.Incus.KeyFile != "", "incus.key_file is required when incus.url is set")
	}
	check(c.Incus.StoragePool != "", "incus.storage_pool is required")
	check(c.Incus.RequestTimeout > 0, "incus.request_timeout must be > 0")

	check(validWorkFolder(c.Runner.WorkFolder),
		"runner.work_folder must be a relative path containing letters, numbers, dots, dashes, underscores, or slashes")
	check(c.Runner.RegistrationTimeout > 0, "runner.registration_timeout must be > 0")
	check(c.Runner.MaxJobDuration > 0, "runner.max_job_duration must be > 0")
	check(c.Runner.ReleaseCacheTTL != nil && *c.Runner.ReleaseCacheTTL >= 0,
		"runner.release_cache_ttl must be >= 0")
	check(c.Runner.MaxParallelMints > 0, "runner.max_parallel_mints must be > 0")
	check(c.Runner.MaxParallelLaunches > 0, "runner.max_parallel_launches must be > 0")

	switch c.Runner.InstanceType {
	case InstanceTypeVM, InstanceTypeContainer:
	default:
		errs = append(errs, fmt.Sprintf("runner.instance_type %q must be %q or %q", c.Runner.InstanceType, InstanceTypeVM, InstanceTypeContainer))
	}
	if c.Runner.Privileged && c.Runner.InstanceType != InstanceTypeContainer {
		errs = append(errs, "runner.privileged is only valid when runner.instance_type=container")
	}

	if len(errs) > 0 {
		return errors.New("config invalid:\n  - " + strings.Join(errs, "\n  - "))
	}
	return nil
}

const maxScaleSetNameLen = 54

var (
	namePrefixPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*[a-z0-9]$|^[a-z]$`)
	workFolderPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)*$`)
)

func validNamePrefix(s string) bool {
	return namePrefixPattern.MatchString(s)
}

func validWorkFolder(s string) bool {
	return s != "" && !strings.HasPrefix(s, "/") &&
		!strings.Contains(s, "..") && workFolderPattern.MatchString(s)
}

// ScaleSetSpecs expands the declarative runner classes into runtime scale sets.
func (c *Config) ScaleSetSpecs() []ScaleSetConfig {
	out := make([]ScaleSetConfig, 0, len(c.ScaleSets.Classes))
	for _, class := range c.ScaleSets.Classes {
		name := class.ScaleSetName(c.ScaleSets.Prefix)
		out = append(out, ScaleSetConfig{
			Name:        name,
			RunnerGroup: c.ScaleSets.RunnerGroup,
			BaseLabels:  ScaleSetLabels(c.ScaleSets.BaseLabels, name),
			MaxRunners:  class.MaxRunners,
			Runner:      class.RunnerSpec(),
		})
	}
	return out
}
