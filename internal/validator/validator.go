// Package validator composes domain configuration validators and issues validated bundles for execution.
package validator

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/reverseproxy"
)

// Bundle holds a semantically validated configuration bundle so callers cannot execute unvalidated state.
type Bundle struct {
	cfg config.Bundle
}

// Config returns a deep copy of the validated configuration bundle, or nil if the receiver is nil.
func (b *Bundle) Config() *config.Bundle {
	if b == nil {
		return nil
	}
	cloned := b.cfg.Clone()
	return &cloned
}

// LoadGlobal loads and validates only the global orchestrator configuration without resolving stacks.
func LoadGlobal(ctx context.Context, configPath string) (config.GlobalConfig, error) {
	global, _, err := loadAndValidateGlobal(ctx, configPath)
	return global, err
}

// LoadBundle loads global, profile, and stack configurations and validates every semantic and cross-stack rule.
func LoadBundle(ctx context.Context, configPath string) (*Bundle, error) {
	global, knownDests, err := loadAndValidateGlobal(ctx, configPath)
	if err != nil {
		return nil, err
	}

	rawBundle, loadErr := config.LoadBundle(ctx, configPath, &global)
	if loadErr != nil {
		return nil, fmt.Errorf("load bundle: %w", loadErr)
	}

	if stackErr := validateBundleStacks(&rawBundle, knownDests); stackErr != nil {
		return nil, stackErr
	}

	return &Bundle{cfg: rawBundle}, nil
}

// ValidateBundle validates a deep copy of an in-memory configuration bundle and isolates internal state from caller mutations.
func ValidateBundle(bundle *config.Bundle) (*Bundle, error) {
	if bundle == nil {
		return nil, errors.New("configuration bundle must not be nil")
	}

	cloned := bundle.Clone()
	knownDests, valErr := validateGlobalConfig(&cloned.Global)
	if valErr != nil {
		return nil, fmt.Errorf("invalid global config: %w", valErr)
	}

	if stackErr := validateBundleStacks(&cloned, knownDests); stackErr != nil {
		return nil, stackErr
	}

	return &Bundle{cfg: cloned}, nil
}

func loadAndValidateGlobal(ctx context.Context, configPath string) (config.GlobalConfig, map[string]struct{}, error) {
	global, err := config.LoadGlobal(ctx, configPath)
	if err != nil {
		return config.GlobalConfig{}, nil, fmt.Errorf("load global config: %w", err)
	}

	cleanConfigPath := filepath.Clean(configPath)
	knownDests, valErr := validateGlobalConfig(&global)
	if valErr != nil {
		return config.GlobalConfig{}, nil, fmt.Errorf("invalid global config %q: %w", cleanConfigPath, valErr)
	}

	return global, knownDests, nil
}

func validateBundleStacks(bundle *config.Bundle, knownDests map[string]struct{}) error {
	seenNames := make(map[string]struct{}, len(bundle.Stacks))
	for i := range bundle.Stacks {
		stack := &bundle.Stacks[i]
		if err := validateStackConfig(stack, knownDests); err != nil {
			return fmt.Errorf("validate stack %q: %w", stack.Name, err)
		}
		if _, duplicate := seenNames[stack.Name]; duplicate {
			return fmt.Errorf("duplicate stack name %q", stack.Name)
		}
		seenNames[stack.Name] = struct{}{}
	}

	if err := reverseproxy.ValidateBundle(&bundle.Global, bundle.Stacks); err != nil {
		return fmt.Errorf("validate reverse proxy bundle: %w", err)
	}

	return nil
}
