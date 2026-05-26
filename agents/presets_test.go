package agents_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nareg/goagent/agents"
)

func TestAll_returnsAllSixPresets(t *testing.T) {
	all := agents.All()
	assert.Len(t, all, 6)
}

func TestAll_presetsAreValid(t *testing.T) {
	for _, p := range agents.All() {
		t.Run(p.ID, func(t *testing.T) {
			require.NoError(t, p.Validate())
			assert.NotEmpty(t, p.Name)
			assert.NotEmpty(t, p.Description)
			assert.Greater(t, p.MaxTokens, 0)
		})
	}
}

func TestAll_idsAreUnique(t *testing.T) {
	seen := make(map[string]bool)
	for _, p := range agents.All() {
		assert.False(t, seen[p.ID], "duplicate preset ID: %s", p.ID)
		seen[p.ID] = true
	}
}

func TestByID_found(t *testing.T) {
	ids := []string{
		"senior-engineer",
		"security-reviewer",
		"code-reviewer",
		"software-architect",
		"db-specialist",
		"test-engineer",
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			p, ok := agents.ByID(id)
			require.True(t, ok)
			assert.Equal(t, id, p.ID)
		})
	}
}

func TestByID_notFound(t *testing.T) {
	_, ok := agents.ByID("nonexistent")
	assert.False(t, ok)
}

func TestModelForTier_anthropic(t *testing.T) {
	cases := []struct {
		tier  agents.Tier
		model string
	}{
		{agents.TierFast, "claude-haiku-4-5"},
		{agents.TierBalanced, "claude-sonnet-4-5"},
		{agents.TierPowerful, "claude-opus-4-5"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.model, agents.ModelForTier("anthropic", tc.tier))
	}
}

func TestModelForTier_openai(t *testing.T) {
	cases := []struct {
		tier  agents.Tier
		model string
	}{
		{agents.TierFast, "gpt-4o-mini"},
		{agents.TierBalanced, "gpt-4o"},
		{agents.TierPowerful, "gpt-4o"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.model, agents.ModelForTier("openai", tc.tier))
	}
}

func TestSystemPrompts_containSOC2References(t *testing.T) {
	// Every system prompt must mention SOC2 so auditors can verify agent
	// behaviour is aligned with compliance requirements.
	for _, p := range agents.All() {
		t.Run(p.ID, func(t *testing.T) {
			assert.Contains(t, p.System, "SOC2",
				"system prompt for %q must reference SOC2 compliance", p.ID)
		})
	}
}

func TestSystemPrompts_neverOutputSecrets(t *testing.T) {
	// Every prompt must explicitly instruct the model not to emit credentials.
	keywords := []string{"credential", "secret", "PII", "never"}
	for _, p := range agents.All() {
		t.Run(p.ID, func(t *testing.T) {
			found := false
			for _, kw := range keywords {
				if contains(p.System, kw) {
					found = true
					break
				}
			}
			assert.True(t, found,
				"system prompt for %q should contain at least one of %v", p.ID, keywords)
		})
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && searchSubstring(s, sub))
}

func searchSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
