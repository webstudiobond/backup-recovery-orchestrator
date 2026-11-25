package config

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/reverseproxy"
)

func validateGlobalReverseProxyStack(stackName string) error {
	if stackName == "" {
		return nil
	}
	if err := validateIdentifier(stackName, "reverse_proxy_stack"); err != nil {
		return err
	}
	if stackName == ReservedSelfBackupName {
		return fmt.Errorf("reverse_proxy_stack %q cannot reference reserved selfbackup stack", stackName)
	}
	return nil
}

func validateStackReverseProxy(reverseProxy *StackReverseProxyConfig) error {
	if err := validateIdentifierList(reverseProxy.ACMENames, "reverse_proxy.acme_names entry"); err != nil {
		return err
	}
	if err := validateIdentifierList(reverseProxy.DomainConfs, "reverse_proxy.domain_confs entry"); err != nil {
		return err
	}
	return validateIdentifierList(reverseProxy.StreamConfs, "reverse_proxy.stream_confs entry")
}

func validateReverseProxyProvider(p *ReverseProxyProviderConfig) error {
	if err := validateReverseProxyType(p.Type); err != nil {
		return err
	}
	if err := validateIdentifier(p.ServiceName, "reverse_proxy_provider.service_name"); err != nil {
		return err
	}
	if err := validateRelativeReverseProxyDir(p.DomainsDir, "reverse_proxy_provider.domains_dir", true); err != nil {
		return err
	}
	if err := validateRelativeReverseProxyDir(p.CertsDir, "reverse_proxy_provider.certs_dir", true); err != nil {
		return err
	}
	return validateRelativeReverseProxyDir(p.StreamDir, "reverse_proxy_provider.stream_dir", false)
}

func validateReverseProxyType(reverseProxyType string) error {
	if !reverseproxy.IsSupported(reverseProxyType) {
		return fmt.Errorf("unsupported reverse_proxy_provider.type %q (supported: %s)", reverseProxyType, strings.Join(reverseproxy.SupportedTypes(), ", "))
	}
	return nil
}

func validateRelativeReverseProxyDir(raw, field string, required bool) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		if required {
			return fmt.Errorf("%s must not be empty", field)
		}
		return nil
	}
	if strings.Contains(trimmed, "\x00") || filepath.IsAbs(trimmed) || !filepath.IsLocal(filepath.FromSlash(trimmed)) {
		return fmt.Errorf("%s %q must be a relative path inside stack_dir", field, raw)
	}
	cleaned := path.Clean(filepath.ToSlash(trimmed))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("%s %q must stay within stack_dir", field, raw)
	}
	return nil
}

func validateBundleReverseProxy(global *GlobalConfig, stacks []StackConfig) error {
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

	var reverseProxyStack *StackConfig
	for i := range stacks {
		s := &stacks[i]
		if s.Name == global.ReverseProxyStack {
			reverseProxyStack = s
			if s.ReverseProxyProvider == nil {
				return fmt.Errorf("reverse_proxy_stack %q must define reverse_proxy_provider", s.Name)
			}
			if s.hasReverseProxyBlock || s.ReverseProxy.HasAssets() {
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
