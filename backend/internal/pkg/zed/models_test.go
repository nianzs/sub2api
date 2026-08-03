package zed

import "testing"

func TestParseModelsFromDataEnvelope(t *testing.T) {
	body := `{"object":"list","data":[
		{"id":"claude-opus-4-6","display_name":"Claude Opus 4.6"},
		{"id":"gpt-5.6-sol","display_name":"GPT-5.6 Sol"}
	]}`

	models, err := ParseModels([]byte(body))
	if err != nil {
		t.Fatalf("ParseModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	if models[0].ID != "claude-opus-4-6" || models[0].DisplayName != "Claude Opus 4.6" {
		t.Errorf("models[0] = %+v", models[0])
	}
}

func TestParseModelsAcceptsSlugAndName(t *testing.T) {
	// The catalog has used slug/name as well as id/display_name.
	body := `[{"slug":"gpt-5.6-terra","name":"GPT-5.6-Terra"}]`

	models, err := ParseModels([]byte(body))
	if err != nil {
		t.Fatalf("ParseModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if models[0].ID != "gpt-5.6-terra" {
		t.Errorf("id = %q, want gpt-5.6-terra", models[0].ID)
	}
	if models[0].DisplayName != "GPT-5.6-Terra" {
		t.Errorf("display name = %q, want the name field", models[0].DisplayName)
	}
}

func TestParseModelsDropsUntranslatableProviders(t *testing.T) {
	// Gemini and Grok are routable upstream but this build cannot convert their
	// request/response formats, so offering them would produce broken requests.
	body := `{"data":[
		{"id":"claude-sonnet-5"},
		{"id":"gemini-2.5-pro"},
		{"id":"grok-4"}
	]}`

	models, err := ParseModels([]byte(body))
	if err != nil {
		t.Fatalf("ParseModels: %v", err)
	}
	if len(models) != 1 || models[0].ID != "claude-sonnet-5" {
		t.Fatalf("models = %+v, want only claude-sonnet-5", models)
	}
}

func TestParseModelsDeduplicatesAndFallsBackToID(t *testing.T) {
	body := `{"data":[{"id":"gpt-5.5"},{"id":"gpt-5.5"},{"id":""}]}`

	models, err := ParseModels([]byte(body))
	if err != nil {
		t.Fatalf("ParseModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if models[0].DisplayName != "gpt-5.5" {
		t.Errorf("display name = %q, want the id as fallback", models[0].DisplayName)
	}
}

func TestParseModelsRejectsGarbage(t *testing.T) {
	if _, err := ParseModels([]byte(`not json`)); err == nil {
		t.Error("ParseModels should fail on invalid JSON")
	}
}

func TestSupportedModelIDsCoversBothProviders(t *testing.T) {
	var hasClaude, hasGPT bool
	for _, id := range SupportedModelIDs() {
		switch ProviderForModel(id) {
		case ProviderAnthropic:
			hasClaude = true
		case ProviderOpenAI:
			hasGPT = true
		}
	}
	if !hasClaude || !hasGPT {
		t.Error("fallback catalog must cover both Claude and GPT")
	}
}
