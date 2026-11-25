// Package config loads, resolves, and validates global, profile, and per-stack orchestrator configurations.
package config

// Default configuration paths and fallback limits applied when optional fields are omitted.
const (
	DefaultConfigPath               = "/etc/bro/config.json"
	DefaultBackupRoot               = "/backups"
	DefaultLockFile                 = "/run/lock/bro.lock"
	DefaultZstdLevel                = 6
	DefaultMinFreeGB                = 3
	DefaultMaxArchiveGB             = 100
	DefaultLocalRetention           = 3
	DefaultRemoteRetain             = 7
	DefaultWaitTimeoutSec           = 30
	DefaultHomeBaseDir              = "/home"
	DefaultComposeName              = "docker-compose.yaml"
	ReservedSelfBackupName          = "bro-self"
	RestoreActionDeployReverseProxy = "deploy_reverse_proxy"

	DefaultTimezone = TimezoneLocal
	TimezoneLocal   = "local"
	TimezoneUTC     = "utc"

	DestTypeFUSE   = "fuse"
	DestTypeMount  = "mount"
	DestTypeDir    = "dir"
	DestTypeRclone = "rclone"
)

// GlobalConfig stores host-wide orchestrator paths, LVM defaults, retention rules, and offsite destinations.
type GlobalConfig struct {
	BackupRoot        string              `json:"backup_root"`
	RestoreWorkDir    string              `json:"restore_work_dir,omitempty"`
	LockFile          string              `json:"lock_file"`
	ProfilesDir       string              `json:"profiles_dir"`
	ConfDir           string              `json:"conf_dir"`
	Timezone          string              `json:"timezone"`
	ReverseProxyStack string              `json:"reverse_proxy_stack,omitempty"`
	LVM               LVMConfig           `json:"lvm"`
	Retention         RetentionConfig     `json:"retention"`
	Destinations      []DestinationConfig `json:"destinations"`
	MinFreeGB         uint64              `json:"min_free_gb"`
	MaxArchiveGB      uint64              `json:"max_archive_gb"`
	ZstdLevel         int                 `json:"zstd_level"`
}

// LVMConfig identifies the volume group, origin logical volume, and temporary snapshot mount target.
type LVMConfig struct {
	VGName       string `json:"vg_name"`
	LVName       string `json:"lv_name"`
	SnapshotName string `json:"snapshot_name"`
	SnapshotSize string `json:"snapshot_size"`
	MountPoint   string `json:"mount_point"`
}

// LVMOverride permits a single stack to override selected LVM snapshot parameters such as allocation size.
type LVMOverride struct {
	VGName       string `json:"vg_name"`
	LVName       string `json:"lv_name"`
	SnapshotName string `json:"snapshot_name"`
	SnapshotSize string `json:"snapshot_size"`
	MountPoint   string `json:"mount_point"`
}

// ReverseProxyProviderConfig defines the compose service and stack-relative directories of a reverse-proxy stack.
type ReverseProxyProviderConfig struct {
	Type        string `json:"type"`
	ServiceName string `json:"service_name"`
	CertsDir    string `json:"certs_dir"`
	DomainsDir  string `json:"domains_dir"`
	StreamDir   string `json:"stream_dir,omitempty"`
}

// RetentionConfig specifies local retention count and default/per-destination offsite retention counts.
type RetentionConfig struct {
	RemoteOverrides map[string]int `json:"remote_overrides,omitempty"`
	LocalCount      int            `json:"local_count"`
	RemoteCount     int            `json:"remote_count"`
}

// RetentionOverride uses pointers so profiles and stacks selectively override retention rules.
type RetentionOverride struct {
	LocalCount      *int           `json:"local_count"`
	RemoteCount     *int           `json:"remote_count"`
	RemoteOverrides map[string]int `json:"remote_overrides"`
}

// DestinationConfig describes an explicitly typed offsite destination (fuse, mount, dir, or rclone).
type DestinationConfig struct {
	RemoteCount         *int   `json:"remote_count"`
	Type                string `json:"type"`
	Name                string `json:"name"`
	MountPoint          string `json:"mount_point"`
	BackupDir           string `json:"backup_dir"`
	Remote              string `json:"remote"`
	RcloneConfig        string `json:"rclone_config"`
	SystemdUnit         string `json:"systemd_unit"`
	RcloneCleanupRemote string `json:"rclone_cleanup_remote"`
	WaitTimeoutSec      int    `json:"wait_timeout_sec"`
}

// UserConfig captures the host account identity and numeric ownership enforced during disaster recovery.
type UserConfig struct {
	Name string `json:"name"`
	UID  uint32 `json:"uid"`
	GID  uint32 `json:"gid"`
}

// FilesConfig controls exclusion patterns applied when archiving the stack snapshot directory.
type FilesConfig struct {
	ExcludesFile          string   `json:"excludes_file"`
	Excludes              []string `json:"excludes"`
	NoWildcardsMatchSlash bool     `json:"no_wildcards_match_slash"`
}

// DACRule declares ownership and permissions enforced natively inside stack_dir during restoration.
type DACRule struct {
	Path      string `json:"path"`
	Owner     string `json:"owner"`
	DirMode   string `json:"dir_mode"`
	FileMode  string `json:"file_mode"`
	Recursive bool   `json:"recursive"`
	Optional  bool   `json:"optional"`
}

// HostFileConfig identifies a host-level file or directory outside stack_dir backed up alongside the stack.
type HostFileConfig struct {
	Path     string `json:"path"`
	Owner    string `json:"owner"`
	DirMode  string `json:"dir_mode"`
	FileMode string `json:"file_mode"`
	Optional bool   `json:"optional"`
}

// HookStep models a declarative pre/post-dump or pre/post-snapshot lifecycle action.
type HookStep struct {
	Action     string   `json:"action"`
	Path       string   `json:"path"`
	Service    string   `json:"service"`
	Signal     string   `json:"signal"`
	Command    []string `json:"command"`
	TimeoutSec int      `json:"timeout_sec"`
}

// HooksConfig groups lifecycle hooks around database export and LVM snapshot creation.
type HooksConfig struct {
	PreDump      []HookStep `json:"pre_dump"`
	PreSnapshot  []HookStep `json:"pre_snapshot"`
	PostSnapshot []HookStep `json:"post_snapshot"`
}

// DBSecretsConfig locates credential files without embedding plaintext passwords in backup configuration.
type DBSecretsConfig struct {
	Source       string `json:"source"`
	UserFile     string `json:"user_file"`
	DBFile       string `json:"db_file"`
	PasswordFile string `json:"password_file"`
}

// DatabaseConfig describes an online database export and restore adapter for a stack.
type DatabaseConfig struct {
	Secrets *DBSecretsConfig `json:"secrets"`
	ID      string           `json:"id"`
	Driver  string           `json:"driver"`
	Service string           `json:"service"`
	Host    string           `json:"host"`
	DBUser  string           `json:"db_user"`
	DBName  string           `json:"db_name"`
}

// StackReverseProxyConfig lists reverse-proxy domain configs, stream configs, and certificate directories owned by a stack.
type StackReverseProxyConfig struct {
	ACMENames   []string `json:"acme_names,omitempty"`
	DomainConfs []string `json:"domain_confs,omitempty"`
	StreamConfs []string `json:"stream_confs,omitempty"`
}

// HasAssets reports whether any domain configs, stream configs, or ACME directories are configured.
func (r *StackReverseProxyConfig) HasAssets() bool {
	return r != nil && (len(r.DomainConfs) > 0 || len(r.StreamConfs) > 0 || len(r.ACMENames) > 0)
}

// RestoreStep describes a single deterministic action executed during single-stack disaster recovery.
type RestoreStep struct {
	Action     string   `json:"action"`
	DatabaseID string   `json:"database_id"`
	Service    string   `json:"service"`
	From       string   `json:"from"`
	To         string   `json:"to"`
	Services   []string `json:"services"`
	Args       []string `json:"args"`
	Command    []string `json:"command"`
	TimeoutSec int      `json:"timeout_sec"`
}

// StackLayer represents the raw JSON schema shared between reusable profiles and per-stack config files.
type StackLayer struct {
	Enabled              *bool                       `json:"enabled"`
	User                 *UserConfig                 `json:"user"`
	ZstdLevel            *int                        `json:"zstd_level"`
	MinFreeGB            *uint64                     `json:"min_free_gb"`
	Retention            *RetentionOverride          `json:"retention"`
	LVM                  *LVMOverride                `json:"lvm"`
	Files                *FilesConfig                `json:"files"`
	Hooks                *HooksConfig                `json:"hooks"`
	ReverseProxyProvider *ReverseProxyProviderConfig `json:"reverse_proxy_provider"`
	ReverseProxy         *StackReverseProxyConfig    `json:"reverse_proxy"`
	Vars                 map[string]string           `json:"vars,omitempty"`
	Name                 string                      `json:"name"`
	Extends              string                      `json:"extends"`
	StackDir             string                      `json:"stack_dir"`
	ComposeFile          string                      `json:"compose_file"`
	Destinations         []string                    `json:"destinations"`
	DAC                  []DACRule                   `json:"dac"`
	HostFiles            []HostFileConfig            `json:"host_files"`
	Databases            []DatabaseConfig            `json:"databases"`
	RestoreSteps         []RestoreStep               `json:"restore_steps"`
}

// StackConfig is the fully resolved, validated effective configuration for a single stack.
type StackConfig struct {
	Vars                 map[string]string           `json:"vars,omitempty"`
	ReverseProxyProvider *ReverseProxyProviderConfig `json:"reverse_proxy_provider,omitempty"`
	Name                 string                      `json:"name"`
	Extends              string                      `json:"extends"`
	StackDir             string                      `json:"stack_dir"`
	ComposeFile          string                      `json:"compose_file"`
	LVM                  LVMConfig                   `json:"lvm"`
	User                 UserConfig                  `json:"user"`
	ReverseProxy         StackReverseProxyConfig     `json:"reverse_proxy"`
	Hooks                HooksConfig                 `json:"hooks"`
	Destinations         []string                    `json:"destinations"`
	DAC                  []DACRule                   `json:"dac"`
	HostFiles            []HostFileConfig            `json:"host_files"`
	Databases            []DatabaseConfig            `json:"databases"`
	RestoreSteps         []RestoreStep               `json:"restore_steps"`
	Retention            RetentionConfig             `json:"retention"`
	Files                FilesConfig                 `json:"files"`
	MinFreeGB            uint64                      `json:"min_free_gb"`
	ZstdLevel            int                         `json:"zstd_level"`
	Enabled              bool                        `json:"enabled"`
	hasReverseProxyBlock bool
}

// Bundle holds the resolved global configuration and all enabled or configured stack definitions.
type Bundle struct {
	ConfigDir string
	Stacks    []StackConfig
	Global    GlobalConfig
}

// StackByName finds a resolved stack definition by its configured name.
func (b *Bundle) StackByName(name string) (StackConfig, bool) {
	for i := range b.Stacks {
		if b.Stacks[i].Name == name {
			return b.Stacks[i], true
		}
	}
	return StackConfig{}, false
}

// ReverseProxyStack returns the resolved stack configuration named by Global.ReverseProxyStack, or nil when disabled or absent.
func (b *Bundle) ReverseProxyStack() *StackConfig {
	if b.Global.ReverseProxyStack == "" {
		return nil
	}
	if st, ok := b.StackByName(b.Global.ReverseProxyStack); ok {
		return &st
	}
	return nil
}

// RemoteCountFor resolves the effective offsite retention count for a given stack and destination pair.
func (s *StackConfig) RemoteCountFor(dest *DestinationConfig) int {
	if dest != nil {
		if override, ok := s.Retention.RemoteOverrides[dest.Name]; ok {
			return override
		}
		if dest.RemoteCount != nil {
			return *dest.RemoteCount
		}
	}
	return s.Retention.RemoteCount
}
