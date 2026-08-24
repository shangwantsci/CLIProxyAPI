package config

import (
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ContentGuardModeObserve = "observe"
	ContentGuardModeEnforce = "enforce"

	ContentGuardStrengthOff      = "off"
	ContentGuardStrengthLoose    = "loose"
	ContentGuardStrengthStandard = "standard"
	ContentGuardStrengthStrict   = "strict"

	ContentGuardCategoryChildSafety          = "child_safety"
	ContentGuardCategorySelfHarmInstructions = "self_harm_instructions"
	ContentGuardCategoryWeaponsCBRNE         = "weapons_cbrne"
	ContentGuardCategoryViolentExtremism     = "violent_extremism"
	ContentGuardCategoryCyberOffense         = "cyber_offense"
	ContentGuardCategoryFraud                = "fraud"
	ContentGuardCategorySexualViolence       = "sexual_violence"
	ContentGuardCategoryJailbreak            = "jailbreak"
	ContentGuardCategoryAdultSexual          = "adult_sexual"
)

// ContentGuardConfig is the optional content-guard section.
// A nil pointer means the section is absent and must not be persisted.
type ContentGuardConfig struct {
	Enabled        *bool                           `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Mode           string                          `yaml:"mode,omitempty" json:"mode,omitempty"`
	Categories     map[string]ContentGuardCategory `yaml:"categories,omitempty" json:"categories,omitempty"`
	AdultSexualFor []string                        `yaml:"adult-sexual-for,omitempty" json:"adult-sexual-for,omitempty"`
}

// ContentGuardCategory is one category entry. YAML may be a bool or {strength: ...}.
type ContentGuardCategory struct {
	Strength string `yaml:"strength,omitempty" json:"strength,omitempty"`
}

func (c *ContentGuardCategory) UnmarshalYAML(value *yaml.Node) error {
	if c == nil || value == nil {
		return nil
	}
	switch value.Kind {
	case yaml.ScalarNode:
		text := strings.TrimSpace(value.Value)
		switch {
		case value.Tag == "!!bool" && text == "true":
			c.Strength = ContentGuardStrengthStandard
		case value.Tag == "!!bool" && text == "false":
			c.Strength = ContentGuardStrengthOff
		default:
			c.Strength = normalizeContentGuardStrength(text)
		}
		return nil
	case yaml.MappingNode:
		var raw struct {
			Strength string `yaml:"strength"`
		}
		if err := value.Decode(&raw); err != nil {
			return err
		}
		c.Strength = normalizeContentGuardStrength(raw.Strength)
		return nil
	default:
		return nil
	}
}

func normalizeContentGuardStrength(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ContentGuardStrengthLoose:
		return ContentGuardStrengthLoose
	case ContentGuardStrengthStandard:
		return ContentGuardStrengthStandard
	case ContentGuardStrengthStrict:
		return ContentGuardStrengthStrict
	default:
		return ContentGuardStrengthOff
	}
}

func normalizeContentGuardMode(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), ContentGuardModeEnforce) {
		return ContentGuardModeEnforce
	}
	return ContentGuardModeObserve
}

// ContentGuardCategoryIDs is the stable category order used by config UI and tests.
func ContentGuardCategoryIDs() []string {
	return []string{
		ContentGuardCategoryChildSafety,
		ContentGuardCategorySelfHarmInstructions,
		ContentGuardCategoryWeaponsCBRNE,
		ContentGuardCategoryViolentExtremism,
		ContentGuardCategoryCyberOffense,
		ContentGuardCategoryFraud,
		ContentGuardCategorySexualViolence,
		ContentGuardCategoryJailbreak,
		ContentGuardCategoryAdultSexual,
	}
}
