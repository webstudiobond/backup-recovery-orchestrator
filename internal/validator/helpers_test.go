package validator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func writeValidatorTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %q: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func validTestGlobalConfig() config.GlobalConfig {
	return config.GlobalConfig{
		BackupRoot:   "/var/backups",
		LockFile:     "/var/run/bro.lock",
		MaxArchiveGB: config.DefaultMaxArchiveGB,
		ZstdLevel:    3,
		Timezone:     config.TimezoneUTC,
		LVM: config.LVMConfig{
			VGName:       "vg_system",
			LVName:       "lv_system",
			SnapshotName: "snap_system",
			SnapshotSize: "10G",
			MountPoint:   "/mnt/snap_point",
		},
		Retention: config.RetentionConfig{
			LocalCount:  1,
			RemoteCount: 1,
		},
	}
}

func validTestStackConfig() config.StackConfig {
	return config.StackConfig{
		Name:        "service_stack",
		User:        config.UserConfig{Name: "app_deployer"},
		StackDir:    "/opt/service",
		ComposeFile: "/opt/service/docker-compose.yml",
		ZstdLevel:   3,
		LVM: config.LVMConfig{
			VGName:       "vg_srv",
			LVName:       "lv_srv",
			SnapshotName: "snap_srv",
			SnapshotSize: "12G",
			MountPoint:   "/mnt/srv_snap",
		},
		Retention: config.RetentionConfig{
			LocalCount:  2,
			RemoteCount: 2,
		},
		Destinations: []string{"main_target"},
	}
}
