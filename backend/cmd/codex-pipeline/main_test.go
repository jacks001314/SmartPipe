package main

import "testing"

func TestResolveModelSelectionProjectWins(t *testing.T) {
	prj := &project{CodexModelProvider: "mpr_1", CodexModel: "deepseek-v4-flash", CodexReasoningEffort: "high"}
	def := &modelDefault{ProviderID: "mpr_2", Model: "gpt-6.1-sol", ReasoningEffort: "low"}
	model, effort, provider := resolveModelSelection("", "", prj, def)
	if model != "deepseek-v4-flash" || effort != "high" || provider != "mpr_1" {
		t.Fatalf("unexpected selection: %q %q %q", model, effort, provider)
	}
}

func TestResolveModelSelectionFallsBackToDefault(t *testing.T) {
	prj := &project{}
	def := &modelDefault{ProviderID: "mpr_2", Model: "gpt-6.1-sol", ReasoningEffort: "medium"}
	model, effort, provider := resolveModelSelection("", "", prj, def)
	if model != "gpt-6.1-sol" || effort != "medium" || provider != "mpr_2" {
		t.Fatalf("unexpected selection: %q %q %q", model, effort, provider)
	}
}

func TestResolveModelSelectionExplicitFlagWins(t *testing.T) {
	prj := &project{CodexModelProvider: "mpr_1", CodexModel: "deepseek-v4-flash"}
	model, _, provider := resolveModelSelection("custom-model", "low", prj, nil)
	if model != "custom-model" || provider != "mpr_1" {
		t.Fatalf("unexpected selection: %q %q", model, provider)
	}
}