package contentguard

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// Hit is a matched category at the configured strength.
type Hit struct {
	Category string
	Strength string
}

// Effective is the runtime view of content-guard. It is never written back to YAML.
type Effective struct {
	Enabled        bool
	Mode           string
	Strengths      map[string]string
	AdultSexualFor []string
}

// EffectiveFrom derives runtime settings. A missing section uses observe defaults
// without materializing them onto cfg.
func EffectiveFrom(cfg *config.SDKConfig) Effective {
	eff := defaultEffective()
	if cfg == nil || cfg.ContentGuard == nil {
		return eff
	}
	section := cfg.ContentGuard
	if section.Enabled != nil {
		eff.Enabled = *section.Enabled
	}
	if strings.TrimSpace(section.Mode) != "" {
		if strings.EqualFold(strings.TrimSpace(section.Mode), config.ContentGuardModeEnforce) {
			eff.Mode = config.ContentGuardModeEnforce
		} else {
			eff.Mode = config.ContentGuardModeObserve
		}
	}
	for id, category := range section.Categories {
		eff.Strengths[id] = strengthName(parseStrength(category.Strength))
	}
	if section.AdultSexualFor != nil {
		eff.AdultSexualFor = append([]string(nil), section.AdultSexualFor...)
	}
	return eff
}

func defaultEffective() Effective {
	strengths := make(map[string]string, len(config.ContentGuardCategoryIDs()))
	for _, id := range config.ContentGuardCategoryIDs() {
		strengths[id] = config.ContentGuardStrengthOff
	}
	strengths[config.ContentGuardCategoryChildSafety] = config.ContentGuardStrengthStandard
	strengths[config.ContentGuardCategorySelfHarmInstructions] = config.ContentGuardStrengthStandard
	return Effective{
		Enabled:        true,
		Mode:           config.ContentGuardModeObserve,
		Strengths:      strengths,
		AdultSexualFor: []string{"claude"},
	}
}

// Evaluate scans already-extracted latest-turn text. Empty text fails open.
func Evaluate(eff Effective, text, sourceFormat, model string) *Hit {
	if !eff.Enabled || strings.TrimSpace(text) == "" {
		return nil
	}
	lower := strings.ToLower(text)
	for _, category := range config.ContentGuardCategoryIDs() {
		level := parseStrength(eff.Strengths[category])
		if level == strengthOff {
			continue
		}
		if !categoryApplies(eff, category, sourceFormat, model) {
			continue
		}
		if matchCategory(category, level, lower) {
			return &Hit{Category: category, Strength: strengthName(level)}
		}
	}
	return nil
}

func categoryApplies(eff Effective, category, sourceFormat, model string) bool {
	if category != config.ContentGuardCategoryAdultSexual {
		return true
	}
	if len(eff.AdultSexualFor) == 0 {
		return true
	}
	blob := strings.ToLower(strings.TrimSpace(sourceFormat) + " " + strings.TrimSpace(model))
	for _, raw := range eff.AdultSexualFor {
		needle := strings.ToLower(strings.TrimSpace(raw))
		if needle != "" && strings.Contains(blob, needle) {
			return true
		}
	}
	return false
}

func matchCategory(category string, level strengthLevel, lowerText string) bool {
	for _, rule := range ruleCatalog {
		if rule.category != category || rule.minStrength > level {
			continue
		}
		for _, gate := range rule.gates {
			if gate != "" && strings.Contains(lowerText, gate) {
				return true
			}
		}
	}
	return false
}
