package config

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

func resolveStack(global *GlobalConfig, profiles map[string]StackLayer, stack *StackLayer) (StackConfig, error) {
	chain, err := buildInheritanceChain(stack, profiles)
	if err != nil {
		return StackConfig{}, err
	}

	out := StackConfig{
		Name:      stack.Name,
		Extends:   stack.Extends,
		Enabled:   true,
		ZstdLevel: global.ZstdLevel,
		MinFreeGB: global.MinFreeGB,
		Retention: RetentionConfig{
			LocalCount:      global.Retention.LocalCount,
			RemoteCount:     global.Retention.RemoteCount,
			RemoteOverrides: maps.Clone(global.Retention.RemoteOverrides),
		},
		LVM: global.LVM,
	}

	for i := range chain {
		applyLayer(&out, &chain[i])
	}

	if out.StackDir == "" {
		out.StackDir = filepath.Join(DefaultHomeBaseDir, out.Name)
	}
	if out.ComposeFile == "" {
		out.ComposeFile = filepath.Join(out.StackDir, DefaultComposeName)
	}
	if out.User.Name == "" {
		out.User.Name = out.Name
	}
	for i := range out.Databases {
		if out.Databases[i].ID == "" && out.Databases[i].Service != "" {
			out.Databases[i].ID = out.Databases[i].Service
		}
	}
	if err := expandStackVars(&out); err != nil {
		return StackConfig{}, err
	}

	return out, nil
}

func buildInheritanceChain(stack *StackLayer, profiles map[string]StackLayer) ([]StackLayer, error) {
	visited := make(map[string]struct{})
	chain := make([]StackLayer, 0, 4)

	current := *stack
	for current.Extends != "" {
		if _, seen := visited[current.Extends]; seen {
			return nil, fmt.Errorf("cyclic profile inheritance detected at %q", current.Extends)
		}
		visited[current.Extends] = struct{}{}

		parent, exists := profiles[current.Extends]
		if !exists {
			return nil, fmt.Errorf("referenced profile %q not found", current.Extends)
		}
		chain = append(chain, parent)
		current = parent
	}

	slices.Reverse(chain)
	chain = append(chain, *stack)
	return chain, nil
}

func applyLayer(out *StackConfig, layer *StackLayer) {
	applyBasicLayer(out, layer)
	applyResourceLayer(out, layer)
}

func applyBasicLayer(out *StackConfig, layer *StackLayer) {
	if layer.Enabled != nil {
		out.Enabled = *layer.Enabled
	}
	if layer.User != nil {
		out.User = *layer.User
	}
	if layer.StackDir != "" {
		out.StackDir = layer.StackDir
	}
	if layer.ComposeFile != "" {
		out.ComposeFile = layer.ComposeFile
	}
	if layer.ZstdLevel != nil {
		out.ZstdLevel = *layer.ZstdLevel
	}
	if layer.MinFreeGB != nil {
		out.MinFreeGB = *layer.MinFreeGB
	}
	if layer.Retention != nil {
		applyRetentionOverride(&out.Retention, layer.Retention)
	}
	if layer.LVM != nil {
		applyLVMOverride(&out.LVM, layer.LVM)
	}
	if layer.Destinations != nil {
		out.Destinations = slices.Clone(layer.Destinations)
	}
}

func applyResourceLayer(out *StackConfig, layer *StackLayer) {
	if len(layer.Vars) > 0 {
		if out.Vars == nil {
			out.Vars = make(map[string]string, len(layer.Vars))
		}
		maps.Copy(out.Vars, layer.Vars)
	}
	if layer.Files != nil {
		applyFilesLayer(&out.Files, layer.Files)
	}
	if layer.Hooks != nil {
		applyHooksLayer(&out.Hooks, layer.Hooks)
	}
	if len(layer.DAC) > 0 {
		out.DAC = append(out.DAC, layer.DAC...)
	}
	if len(layer.HostFiles) > 0 {
		out.HostFiles = append(out.HostFiles, layer.HostFiles...)
	}
	if layer.Databases != nil {
		out.Databases = cloneDatabases(layer.Databases)
	}
	if layer.ReverseProxyProvider != nil {
		out.ReverseProxyProvider = &ReverseProxyProviderConfig{
			Type:        strings.TrimSpace(layer.ReverseProxyProvider.Type),
			ServiceName: strings.TrimSpace(layer.ReverseProxyProvider.ServiceName),
			CertsDir:    strings.TrimSpace(layer.ReverseProxyProvider.CertsDir),
			DomainsDir:  strings.TrimSpace(layer.ReverseProxyProvider.DomainsDir),
			StreamDir:   strings.TrimSpace(layer.ReverseProxyProvider.StreamDir),
		}
	}
	if layer.ReverseProxy != nil {
		out.hasReverseProxyBlock = true
		out.ReverseProxy = StackReverseProxyConfig{
			ACMENames:   slices.Clone(layer.ReverseProxy.ACMENames),
			DomainConfs: slices.Clone(layer.ReverseProxy.DomainConfs),
			StreamConfs: slices.Clone(layer.ReverseProxy.StreamConfs),
		}
	}
	if layer.RestoreSteps != nil {
		out.RestoreSteps = cloneRestoreSteps(layer.RestoreSteps)
	}
}

func applyRetentionOverride(dst *RetentionConfig, src *RetentionOverride) {
	if src.LocalCount != nil {
		dst.LocalCount = *src.LocalCount
	}
	if src.RemoteCount != nil {
		dst.RemoteCount = *src.RemoteCount
	}
	if len(src.RemoteOverrides) > 0 {
		if dst.RemoteOverrides == nil {
			dst.RemoteOverrides = make(map[string]int, len(src.RemoteOverrides))
		}
		maps.Copy(dst.RemoteOverrides, src.RemoteOverrides)
	}
}

func applyLVMOverride(dst *LVMConfig, src *LVMOverride) {
	if src.VGName != "" {
		dst.VGName = src.VGName
	}
	if src.LVName != "" {
		dst.LVName = src.LVName
	}
	if src.SnapshotName != "" {
		dst.SnapshotName = src.SnapshotName
	}
	if src.SnapshotSize != "" {
		dst.SnapshotSize = src.SnapshotSize
	}
	if src.MountPoint != "" {
		dst.MountPoint = src.MountPoint
	}
}

func applyFilesLayer(dst, src *FilesConfig) {
	if len(src.Excludes) > 0 {
		dst.Excludes = append(dst.Excludes, src.Excludes...)
	}
	if src.ExcludesFile != "" {
		dst.ExcludesFile = src.ExcludesFile
	}
	if src.NoWildcardsMatchSlash {
		dst.NoWildcardsMatchSlash = true
	}
}

func applyHooksLayer(dst, src *HooksConfig) {
	if src.PreDump != nil {
		dst.PreDump = cloneHookSteps(src.PreDump)
	}
	if src.PreSnapshot != nil {
		dst.PreSnapshot = cloneHookSteps(src.PreSnapshot)
	}
	if src.PostSnapshot != nil {
		dst.PostSnapshot = cloneHookSteps(src.PostSnapshot)
	}
}
