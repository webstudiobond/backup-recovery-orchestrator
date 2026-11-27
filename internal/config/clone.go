package config

import (
	"maps"
	"slices"
)

// Clone returns a deep copy of Bundle so callers cannot alias or mutate internal state across boundaries.
func (b *Bundle) Clone() Bundle {
	if b == nil {
		return Bundle{}
	}
	return Bundle{
		ConfigDir: b.ConfigDir,
		Global:    cloneGlobalConfig(&b.Global),
		Stacks:    cloneStacks(b.Stacks),
	}
}

func cloneGlobalConfig(src *GlobalConfig) GlobalConfig {
	out := *src
	out.Retention = cloneRetentionConfig(src.Retention)
	out.Destinations = cloneDestinations(src.Destinations)
	return out
}

func cloneRetentionConfig(src RetentionConfig) RetentionConfig {
	out := src
	out.RemoteOverrides = maps.Clone(src.RemoteOverrides)
	return out
}

func cloneDestinations(src []DestinationConfig) []DestinationConfig {
	if src == nil {
		return nil
	}
	out := make([]DestinationConfig, len(src))
	for i := range src {
		out[i] = src[i]
		if src[i].RemoteCount != nil {
			count := *src[i].RemoteCount
			out[i].RemoteCount = &count
		}
	}
	return out
}

func cloneStacks(src []StackConfig) []StackConfig {
	if src == nil {
		return nil
	}
	out := make([]StackConfig, len(src))
	for i := range src {
		out[i] = cloneStackConfig(&src[i])
	}
	return out
}

func cloneStackConfig(src *StackConfig) StackConfig {
	out := *src
	out.Vars = maps.Clone(src.Vars)
	if src.ReverseProxyProvider != nil {
		copied := *src.ReverseProxyProvider
		out.ReverseProxyProvider = &copied
	}
	out.ReverseProxy = StackReverseProxyConfig{
		ACMENames:   slices.Clone(src.ReverseProxy.ACMENames),
		DomainConfs: slices.Clone(src.ReverseProxy.DomainConfs),
		StreamConfs: slices.Clone(src.ReverseProxy.StreamConfs),
	}
	out.Hooks = HooksConfig{
		PreDump:      cloneHookSteps(src.Hooks.PreDump),
		PreSnapshot:  cloneHookSteps(src.Hooks.PreSnapshot),
		PostSnapshot: cloneHookSteps(src.Hooks.PostSnapshot),
	}
	out.Destinations = slices.Clone(src.Destinations)
	out.DAC = slices.Clone(src.DAC)
	out.HostFiles = slices.Clone(src.HostFiles)
	out.Databases = cloneDatabases(src.Databases)
	out.RestoreSteps = cloneRestoreSteps(src.RestoreSteps)
	out.Retention = cloneRetentionConfig(src.Retention)
	out.Files.Excludes = slices.Clone(src.Files.Excludes)
	return out
}

func cloneHookSteps(steps []HookStep) []HookStep {
	if steps == nil {
		return nil
	}
	out := make([]HookStep, len(steps))
	for i := range steps {
		out[i] = steps[i]
		out[i].Command = slices.Clone(steps[i].Command)
	}
	return out
}

func cloneDatabases(dbs []DatabaseConfig) []DatabaseConfig {
	if dbs == nil {
		return nil
	}
	out := make([]DatabaseConfig, len(dbs))
	for i := range dbs {
		out[i] = dbs[i]
		if dbs[i].Secrets != nil {
			copied := *dbs[i].Secrets
			out[i].Secrets = &copied
		}
	}
	return out
}

func cloneRestoreSteps(steps []RestoreStep) []RestoreStep {
	if steps == nil {
		return nil
	}
	out := make([]RestoreStep, len(steps))
	for i := range steps {
		out[i] = steps[i]
		out[i].Services = slices.Clone(steps[i].Services)
		out[i].Args = slices.Clone(steps[i].Args)
		out[i].Command = slices.Clone(steps[i].Command)
	}
	return out
}
