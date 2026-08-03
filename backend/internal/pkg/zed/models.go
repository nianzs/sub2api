package zed

import "encoding/json"

// Model is one entry of the upstream model catalog.
type Model struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
}

// modelsResponse is the shape of GET /models. Unknown fields are ignored so a
// catalog change upstream cannot break parsing.
type modelsResponse struct {
	Data []struct {
		ID          string `json:"id"`
		Slug        string `json:"slug"`
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
}

// DefaultModels is the fallback catalog used when the upstream /models call is
// unavailable. The live endpoint is authoritative — prefer ParseModels.
var DefaultModels = []Model{
	{ID: "claude-opus-4-6", Type: "model", DisplayName: "Claude Opus 4.6"},
	{ID: "claude-opus-4-5", Type: "model", DisplayName: "Claude Opus 4.5"},
	{ID: "claude-sonnet-5", Type: "model", DisplayName: "Claude Sonnet 5"},
	{ID: "claude-sonnet-4-5", Type: "model", DisplayName: "Claude Sonnet 4.5"},
	{ID: "claude-haiku-4-5", Type: "model", DisplayName: "Claude Haiku 4.5"},
	{ID: "gpt-5.6-sol", Type: "model", DisplayName: "GPT-5.6 Sol"},
	{ID: "gpt-5.6-terra", Type: "model", DisplayName: "GPT-5.6 Terra"},
	{ID: "gpt-5.6-luna", Type: "model", DisplayName: "GPT-5.6 Luna"},
	{ID: "gpt-5.5", Type: "model", DisplayName: "GPT-5.5"},
}

// SupportedModelIDs returns the fallback catalog's IDs.
func SupportedModelIDs() []string {
	ids := make([]string, 0, len(DefaultModels))
	for _, m := range DefaultModels {
		ids = append(ids, m.ID)
	}
	return ids
}

// ParseModels reads a GET /models response body.
//
// The upstream has used both "id" and "slug" as the model identifier and both
// "name" and "display_name" for the label, so all are accepted. Entries whose
// provider this build cannot translate (Google, xAI) are dropped, since offering
// them would produce requests we cannot convert.
func ParseModels(body []byte) ([]Model, error) {
	var resp modelsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		// Some deployments return a bare array rather than {"data": [...]}.
		var bare []struct {
			ID          string `json:"id"`
			Slug        string `json:"slug"`
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
		}
		if err2 := json.Unmarshal(body, &bare); err2 != nil {
			return nil, err
		}
		resp.Data = bare
	}

	models := make([]Model, 0, len(resp.Data))
	seen := make(map[string]bool, len(resp.Data))

	for _, entry := range resp.Data {
		id := entry.ID
		if id == "" {
			id = entry.Slug
		}
		if id == "" || seen[id] {
			continue
		}
		if !IsSupportedProvider(ProviderForModel(id)) {
			continue
		}
		seen[id] = true

		label := entry.DisplayName
		if label == "" {
			label = entry.Name
		}
		if label == "" {
			label = id
		}

		models = append(models, Model{ID: id, Type: "model", DisplayName: label})
	}

	return models, nil
}
