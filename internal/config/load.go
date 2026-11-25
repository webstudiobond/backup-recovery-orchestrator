package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type globalConfigJSON struct {
	ZstdLevel         *int                `json:"zstd_level"`
	MinFreeGB         *uint64             `json:"min_free_gb"`
	MaxArchiveGB      *uint64             `json:"max_archive_gb"`
	Retention         *RetentionOverride  `json:"retention"`
	BackupRoot        string              `json:"backup_root"`
	RestoreWorkDir    string              `json:"restore_work_dir"`
	LockFile          string              `json:"lock_file"`
	ProfilesDir       string              `json:"profiles_dir"`
	ConfDir           string              `json:"conf_dir"`
	Timezone          string              `json:"timezone"`
	ReverseProxyStack string              `json:"reverse_proxy_stack"`
	LVM               LVMConfig           `json:"lvm"`
	Destinations      []DestinationConfig `json:"destinations"`
}

// LoadGlobal reads and validates only the global orchestrator configuration without loading profiles or stacks.
func LoadGlobal(ctx context.Context, configPath string) (GlobalConfig, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return GlobalConfig{}, fmt.Errorf("context canceled before loading config: %w", ctxErr)
	}

	cleanConfigPath := filepath.Clean(configPath)
	global, err := loadGlobalConfig(cleanConfigPath)
	if err != nil {
		return GlobalConfig{}, err
	}

	if valErr := validateGlobalConfig(&global); valErr != nil {
		return GlobalConfig{}, fmt.Errorf("invalid global config %q: %w", cleanConfigPath, valErr)
	}
	return global, nil
}

// LoadBundle reads the global config, resolves profile inheritance for every conf.d file, and validates the result.
func LoadBundle(ctx context.Context, configPath string) (Bundle, error) {
	global, err := LoadGlobal(ctx, configPath)
	if err != nil {
		return Bundle{}, err
	}

	profiles, profErr := loadProfiles(global.ProfilesDir)
	if profErr != nil {
		return Bundle{}, profErr
	}

	stacks, stackErr := loadStacks(&global, profiles)
	if stackErr != nil {
		return Bundle{}, stackErr
	}

	return Bundle{
		ConfigDir: resolveConfigDir(filepath.Clean(configPath)),
		Global:    global,
		Stacks:    stacks,
	}, nil
}

var filepathAbs = filepath.Abs

func resolveConfigDir(cleanConfigPath string) string {
	absPath, err := filepathAbs(cleanConfigPath)
	if err != nil {
		return filepath.Dir(cleanConfigPath)
	}
	return filepath.Dir(absPath)
}

func loadGlobalConfig(path string) (GlobalConfig, error) {
	var raw globalConfigJSON
	if err := decodeStrictJSONFile(path, &raw); err != nil {
		return GlobalConfig{}, fmt.Errorf("load global config %q: %w", path, err)
	}

	configDir := resolveConfigDir(path)
	resolveDir := func(field, fallback string) string {
		trimmed := strings.TrimSpace(field)
		if trimmed == "" {
			trimmed = fallback
		}
		if filepath.IsAbs(trimmed) {
			return filepath.Clean(trimmed)
		}
		return filepath.Clean(filepath.Join(configDir, trimmed))
	}

	profilesDir := resolveDir(raw.ProfilesDir, "profiles")
	confDir := resolveDir(raw.ConfDir, "conf.d")

	backupRoot := strings.TrimSpace(raw.BackupRoot)
	if backupRoot == "" {
		backupRoot = DefaultBackupRoot
	}
	restoreWorkDir := strings.TrimSpace(raw.RestoreWorkDir)
	if restoreWorkDir != "" {
		restoreWorkDir = filepath.Clean(restoreWorkDir)
	}
	lockFile := strings.TrimSpace(raw.LockFile)
	if lockFile == "" {
		lockFile = DefaultLockFile
	}

	tz := strings.TrimSpace(raw.Timezone)
	if tz == "" {
		tz = DefaultTimezone
	}

	minFreeGB := uint64(DefaultMinFreeGB)
	if raw.MinFreeGB != nil {
		minFreeGB = *raw.MinFreeGB
	}
	maxArchiveGB := uint64(DefaultMaxArchiveGB)
	if raw.MaxArchiveGB != nil {
		maxArchiveGB = *raw.MaxArchiveGB
	}
	zstdLevel := DefaultZstdLevel
	if raw.ZstdLevel != nil {
		zstdLevel = *raw.ZstdLevel
	}

	retention := RetentionConfig{
		LocalCount:      DefaultLocalRetention,
		RemoteCount:     DefaultRemoteRetain,
		RemoteOverrides: make(map[string]int),
	}
	if raw.Retention != nil {
		applyRetentionOverride(&retention, raw.Retention)
	}

	return GlobalConfig{
		BackupRoot:        filepath.Clean(backupRoot),
		RestoreWorkDir:    restoreWorkDir,
		LockFile:          filepath.Clean(lockFile),
		ProfilesDir:       profilesDir,
		ConfDir:           confDir,
		Timezone:          tz,
		ReverseProxyStack: strings.TrimSpace(raw.ReverseProxyStack),
		LVM:               raw.LVM,
		Retention:         retention,
		Destinations:      cleanGlobalDestinations(raw.Destinations),
		MinFreeGB:         minFreeGB,
		MaxArchiveGB:      maxArchiveGB,
		ZstdLevel:         zstdLevel,
	}, nil
}

func cleanGlobalDestinations(raw []DestinationConfig) []DestinationConfig {
	dests := make([]DestinationConfig, len(raw))
	for i := range raw {
		dests[i] = raw[i]
		if dests[i].WaitTimeoutSec == 0 {
			dests[i].WaitTimeoutSec = DefaultWaitTimeoutSec
		}
		if dests[i].MountPoint != "" {
			dests[i].MountPoint = filepath.Clean(dests[i].MountPoint)
		}
		if dests[i].BackupDir != "" {
			dests[i].BackupDir = filepath.Clean(dests[i].BackupDir)
		}
	}
	return dests
}

func loadProfiles(dir string) (map[string]StackLayer, error) {
	profiles := make(map[string]StackLayer)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return profiles, nil
		}
		return nil, fmt.Errorf("read profiles directory %q: %w", dir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		profileName := strings.TrimSuffix(entry.Name(), ".json")
		filePath := filepath.Join(dir, entry.Name())

		var layer StackLayer
		if err := decodeStrictJSONFile(filePath, &layer); err != nil {
			return nil, fmt.Errorf("profile %q: %w", entry.Name(), err)
		}
		layer.Name = profileName
		profiles[profileName] = layer
	}

	return profiles, nil
}

func loadStacks(global *GlobalConfig, profiles map[string]StackLayer) ([]StackConfig, error) {
	entries, err := os.ReadDir(global.ConfDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("conf_dir %q does not exist", global.ConfDir)
		}
		return nil, fmt.Errorf("read conf_dir %q: %w", global.ConfDir, err)
	}

	knownDests := make(map[string]struct{}, len(global.Destinations))
	for i := range global.Destinations {
		knownDests[global.Destinations[i].Name] = struct{}{}
	}

	seenNames := make(map[string]struct{})
	var stacks []StackConfig

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		stackName := strings.TrimSuffix(entry.Name(), ".json")
		if err := validateIdentifier(stackName, "stack name from file "+entry.Name()); err != nil {
			return nil, err
		}

		filePath := filepath.Join(global.ConfDir, entry.Name())
		var rawLayer StackLayer
		if err := decodeStrictJSONFile(filePath, &rawLayer); err != nil {
			return nil, fmt.Errorf("stack config %q: %w", entry.Name(), err)
		}
		if rawLayer.Name == "" {
			rawLayer.Name = stackName
		}

		resolved, resolveErr := resolveStack(global, profiles, &rawLayer)
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve stack %q: %w", stackName, resolveErr)
		}

		if _, duplicate := seenNames[resolved.Name]; duplicate {
			return nil, fmt.Errorf("duplicate stack name %q", resolved.Name)
		}
		seenNames[resolved.Name] = struct{}{}

		if valErr := validateStackConfig(&resolved, knownDests); valErr != nil {
			return nil, fmt.Errorf("validate stack %q: %w", stackName, valErr)
		}

		stacks = append(stacks, resolved)
	}

	slices.SortFunc(stacks, func(a, b StackConfig) int {
		return strings.Compare(a.Name, b.Name)
	})

	if err := validateBundleReverseProxy(global, stacks); err != nil {
		return nil, err
	}

	return stacks, nil
}

func decodeStrictJSONFile(path string, target any) error {
	cleanPath := filepath.Clean(path)
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decErr := decoder.Decode(target); decErr != nil {
		return fmt.Errorf("decode json: %w", decErr)
	}

	var trailing any
	if trailErr := decoder.Decode(&trailing); !errors.Is(trailErr, io.EOF) {
		return errors.New("unexpected trailing data after JSON object")
	}

	return nil
}
