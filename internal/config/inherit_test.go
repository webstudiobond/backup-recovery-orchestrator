package config

import (
	"testing"
)

func TestApplyBasicLayer(t *testing.T) {
	t.Parallel()

	minFree := uint64(25)
	zstd := 7
	enabled := false
	user := UserConfig{Name: "custom_user"}

	out := StackConfig{}
	layer := StackLayer{
		Enabled:     &enabled,
		User:        &user,
		StackDir:    "/opt/stacks/app",
		ComposeFile: "/opt/stacks/app/docker-compose.yml",
		ZstdLevel:   &zstd,
		MinFreeGB:   &minFree,
		Retention: &RetentionOverride{
			LocalCount: &zstd,
		},
		LVM: &LVMOverride{
			VGName: "vg_override",
		},
		Destinations: []string{"dest_a"},
	}

	applyBasicLayer(&out, &layer)

	if out.Enabled != enabled {
		t.Fatalf("expected enabled %v, got %v", enabled, out.Enabled)
	}
	if out.User.Name != "custom_user" {
		t.Fatalf("expected user custom_user, got %q", out.User.Name)
	}
	if out.StackDir != "/opt/stacks/app" {
		t.Fatalf("expected stack dir /opt/stacks/app, got %q", out.StackDir)
	}
	if out.ComposeFile != "/opt/stacks/app/docker-compose.yml" {
		t.Fatalf("expected compose file /opt/stacks/app/docker-compose.yml, got %q", out.ComposeFile)
	}
	if out.ZstdLevel != zstd {
		t.Fatalf("expected zstd level %d, got %d", zstd, out.ZstdLevel)
	}
	if out.MinFreeGB != minFree {
		t.Fatalf("expected min free %d, got %d", minFree, out.MinFreeGB)
	}
	if out.Retention.LocalCount != zstd {
		t.Fatalf("expected retention local count %d, got %d", zstd, out.Retention.LocalCount)
	}
	if out.LVM.VGName != "vg_override" {
		t.Fatalf("expected lvm vg_name vg_override, got %q", out.LVM.VGName)
	}
	if len(out.Destinations) != 1 || out.Destinations[0] != "dest_a" {
		t.Fatalf("unexpected destinations: %v", out.Destinations)
	}
}

func TestApplyRetentionOverride(t *testing.T) {
	t.Parallel()

	localCount := 10
	remoteCount := 20

	cases := []struct {
		dst              *RetentionConfig
		src              *RetentionOverride
		name             string
		wantLocal        int
		wantRemote       int
		wantOverridesLen int
	}{
		{
			dst: &RetentionConfig{
				LocalCount:  1,
				RemoteCount: 2,
			},
			src: &RetentionOverride{
				RemoteOverrides: map[string]int{"dest_primary": 3},
			},
			name:             "initialize nil map and apply remote overrides",
			wantLocal:        1,
			wantRemote:       2,
			wantOverridesLen: 1,
		},
		{
			dst: &RetentionConfig{
				LocalCount:      1,
				RemoteCount:     2,
				RemoteOverrides: map[string]int{"dest_first": 5},
			},
			src: &RetentionOverride{
				LocalCount:      &localCount,
				RemoteCount:     &remoteCount,
				RemoteOverrides: map[string]int{"dest_second": 7},
			},
			name:             "override counts and merge into existing map",
			wantLocal:        10,
			wantRemote:       20,
			wantOverridesLen: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dstCopy := *tc.dst
			applyRetentionOverride(&dstCopy, tc.src)

			if dstCopy.LocalCount != tc.wantLocal {
				t.Fatalf("expected local count %d, got %d", tc.wantLocal, dstCopy.LocalCount)
			}
			if dstCopy.RemoteCount != tc.wantRemote {
				t.Fatalf("expected remote count %d, got %d", tc.wantRemote, dstCopy.RemoteCount)
			}
			if len(dstCopy.RemoteOverrides) != tc.wantOverridesLen {
				t.Fatalf("expected overrides len %d, got %d", tc.wantOverridesLen, len(dstCopy.RemoteOverrides))
			}
		})
	}
}

func TestApplyLVMOverride(t *testing.T) {
	t.Parallel()

	cases := []struct {
		dst      LVMConfig
		src      LVMOverride
		expected LVMConfig
		name     string
	}{
		{
			dst: LVMConfig{
				VGName:       "old_vg",
				LVName:       "old_lv",
				SnapshotName: "old_snap",
				SnapshotSize: "10G",
				MountPoint:   "/mnt/old",
			},
			src: LVMOverride{
				VGName:       "new_vg",
				LVName:       "new_lv",
				SnapshotName: "new_snap",
				SnapshotSize: "20G",
				MountPoint:   "/mnt/new",
			},
			expected: LVMConfig{
				VGName:       "new_vg",
				LVName:       "new_lv",
				SnapshotName: "new_snap",
				SnapshotSize: "20G",
				MountPoint:   "/mnt/new",
			},
			name: "override all lvm parameters",
		},
		{
			dst: LVMConfig{
				VGName:       "base_vg",
				LVName:       "base_lv",
				SnapshotName: "base_snap",
				SnapshotSize: "15G",
				MountPoint:   "/mnt/base",
			},
			src: LVMOverride{},
			expected: LVMConfig{
				VGName:       "base_vg",
				LVName:       "base_lv",
				SnapshotName: "base_snap",
				SnapshotSize: "15G",
				MountPoint:   "/mnt/base",
			},
			name: "empty override preserves destination values",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dstCopy := tc.dst
			applyLVMOverride(&dstCopy, &tc.src)

			if dstCopy != tc.expected {
				t.Fatalf("expected lvm %+v, got %+v", tc.expected, dstCopy)
			}
		})
	}
}

func TestApplyFilesLayer(t *testing.T) {
	t.Parallel()

	cases := []struct {
		dst                  *FilesConfig
		src                  *FilesConfig
		name                 string
		wantExcludesFile     string
		wantExcludesCount    int
		wantNoWildcardsSlash bool
	}{
		{
			dst: &FilesConfig{
				Excludes: []string{"*.tmp"},
			},
			src: &FilesConfig{
				Excludes:              []string{"*.log"},
				ExcludesFile:          ".gitignore_excludes",
				NoWildcardsMatchSlash: true,
			},
			name:                 "append excludes and set excludes file",
			wantExcludesFile:     ".gitignore_excludes",
			wantExcludesCount:    2,
			wantNoWildcardsSlash: true,
		},
		{
			dst: &FilesConfig{
				Excludes:     []string{"*.bak"},
				ExcludesFile: ".default_excludes",
			},
			src:                  &FilesConfig{},
			name:                 "empty source preserves destination unchanged",
			wantExcludesFile:     ".default_excludes",
			wantExcludesCount:    1,
			wantNoWildcardsSlash: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dstCopy := *tc.dst
			applyFilesLayer(&dstCopy, tc.src)

			if len(dstCopy.Excludes) != tc.wantExcludesCount {
				t.Fatalf("expected excludes count %d, got %d", tc.wantExcludesCount, len(dstCopy.Excludes))
			}
			if dstCopy.ExcludesFile != tc.wantExcludesFile {
				t.Fatalf("expected excludes file %q, got %q", tc.wantExcludesFile, dstCopy.ExcludesFile)
			}
			if dstCopy.NoWildcardsMatchSlash != tc.wantNoWildcardsSlash {
				t.Fatalf("expected no_wildcards_match_slash %v, got %v", tc.wantNoWildcardsSlash, dstCopy.NoWildcardsMatchSlash)
			}
		})
	}
}

func TestApplyHooksLayer(t *testing.T) {
	t.Parallel()

	dst := HooksConfig{}
	src := HooksConfig{
		PreDump: []HookStep{
			{Action: "pre_dump_action", Command: []string{"sh", "-c", "dump"}},
		},
		PreSnapshot: []HookStep{
			{Action: "pre_snap_action", Command: []string{"bash", "-c", "snap"}},
		},
		PostSnapshot: []HookStep{
			{Action: "post_snap_action", Command: []string{"zsh", "-c", "post"}},
		},
	}

	applyHooksLayer(&dst, &src)

	if len(dst.PreDump) != 1 || len(dst.PreDump[0].Command) != 3 {
		t.Fatalf("unexpected pre_dump: %+v", dst.PreDump)
	}
	if len(dst.PreSnapshot) != 1 || len(dst.PreSnapshot[0].Command) != 3 {
		t.Fatalf("unexpected pre_snapshot: %+v", dst.PreSnapshot)
	}
	if len(dst.PostSnapshot) != 1 || len(dst.PostSnapshot[0].Command) != 3 {
		t.Fatalf("unexpected post_snapshot: %+v", dst.PostSnapshot)
	}
}
