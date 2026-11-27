package reverseproxy

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/identifier"
)

// CleanRelativeDir validates and cleans a stack-relative reverse-proxy subdirectory path.
func CleanRelativeDir(raw, field string, required bool) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		if required {
			return "", fmt.Errorf("%s must not be empty", field)
		}
		return "", nil
	}
	if strings.Contains(trimmed, "\x00") || filepath.IsAbs(trimmed) || !filepath.IsLocal(filepath.FromSlash(trimmed)) {
		return "", fmt.Errorf("%s %q must be a relative path inside stack_dir", field, raw)
	}
	cleaned := path.Clean(filepath.ToSlash(trimmed))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%s %q must stay within stack_dir", field, raw)
	}
	return cleaned, nil
}

// ValidateGlobalStackName validates the global reverse_proxy_stack reference when enabled.
func ValidateGlobalStackName(stackName string) error {
	if stackName == "" {
		return nil
	}
	if err := identifier.Validate(stackName, "reverse_proxy_stack"); err != nil {
		return fmt.Errorf("validate reverse_proxy_stack: %w", err)
	}
	if stackName == config.ReservedSelfBackupName {
		return fmt.Errorf("reverse_proxy_stack %q cannot reference reserved selfbackup stack", stackName)
	}
	return nil
}

// ValidateStackAssets validates per-stack reverse-proxy ACME, domain, and stream entry names.
func ValidateStackAssets(assets *config.StackReverseProxyConfig) error {
	if err := identifier.ValidateList(assets.ACMENames, "reverse_proxy.acme_names entry"); err != nil {
		return fmt.Errorf("validate reverse_proxy.acme_names: %w", err)
	}
	if err := identifier.ValidateList(assets.DomainConfs, "reverse_proxy.domain_confs entry"); err != nil {
		return fmt.Errorf("validate reverse_proxy.domain_confs: %w", err)
	}
	if err := identifier.ValidateList(assets.StreamConfs, "reverse_proxy.stream_confs entry"); err != nil {
		return fmt.Errorf("validate reverse_proxy.stream_confs: %w", err)
	}
	return nil
}

// ValidateProviderConfig validates the provider type, service name, and stack-relative directories.
func ValidateProviderConfig(provider *config.ReverseProxyProviderConfig) error {
	if !IsSupported(provider.Type) {
		return fmt.Errorf("unsupported reverse_proxy_provider.type %q (supported: %s)", provider.Type, strings.Join(SupportedTypes(), ", "))
	}
	if err := identifier.Validate(provider.ServiceName, "reverse_proxy_provider.service_name"); err != nil {
		return fmt.Errorf("validate reverse_proxy_provider.service_name: %w", err)
	}
	if _, err := CleanRelativeDir(provider.DomainsDir, "reverse_proxy_provider.domains_dir", true); err != nil {
		return err
	}
	if _, err := CleanRelativeDir(provider.CertsDir, "reverse_proxy_provider.certs_dir", true); err != nil {
		return err
	}
	if _, err := CleanRelativeDir(provider.StreamDir, "reverse_proxy_provider.stream_dir", false); err != nil {
		return err
	}
	return nil
}

// ValidateBundle enforces cross-stack reverse-proxy ownership and stream_dir prerequisites across all configured stacks.
func ValidateBundle(global *config.GlobalConfig, stacks []config.StackConfig) error {
	if global.ReverseProxyStack == "" {
		for i := range stacks {
			s := &stacks[i]
			if s.ReverseProxyProvider != nil {
				return fmt.Errorf("stack %q defines reverse_proxy_provider while global reverse_proxy_stack is not set", s.Name)
			}
			if s.ReverseProxy.HasAssets() {
				return fmt.Errorf("stack %q defines reverse_proxy assets while global reverse_proxy_stack is not set", s.Name)
			}
		}
		return nil
	}

	var reverseProxyStack *config.StackConfig
	for i := range stacks {
		s := &stacks[i]
		if s.Name == global.ReverseProxyStack {
			reverseProxyStack = s
			if s.ReverseProxyProvider == nil {
				return fmt.Errorf("reverse_proxy_stack %q must define reverse_proxy_provider", s.Name)
			}
			if s.HasReverseProxyBlock() || s.ReverseProxy.HasAssets() {
				return fmt.Errorf("reverse_proxy_stack %q must not define reverse_proxy", s.Name)
			}
			continue
		}
		if s.ReverseProxyProvider != nil {
			return fmt.Errorf("stack %q must not define reverse_proxy_provider (only reverse_proxy_stack %q may)", s.Name, global.ReverseProxyStack)
		}
	}

	if reverseProxyStack == nil {
		return fmt.Errorf("reverse_proxy_stack %q not found in conf_dir", global.ReverseProxyStack)
	}
	if reverseProxyStack.ReverseProxyProvider.StreamDir == "" {
		for i := range stacks {
			if len(stacks[i].ReverseProxy.StreamConfs) > 0 {
				return errors.New("reverse_proxy_provider.stream_dir is required when stacks define stream_confs")
			}
		}
	}
	return nil
}
