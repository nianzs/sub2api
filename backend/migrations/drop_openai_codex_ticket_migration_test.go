package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDropOpenAICodexTicketMigration(t *testing.T) {
	content, err := FS.ReadFile("250_drop_openai_codex_ticket.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")

	for _, key := range []string{
		"openai_codex_ticket_enabled",
		"openai_codex_ticket_fail_closed",
		"openai_codex_ticket_harvest_proxy_url",
		"openai_codex_ticket_default_length",
		"openai_codex_ticket_plan_lengths",
	} {
		require.Contains(t, sql, "'"+key+"'")
	}

	// 下划线在 LIKE 中是单字符通配，必须转义，否则会误删形如 codexXturnXticket: 的键。
	require.Contains(t, sql, `LIKE 'codex\_turn\_ticket:%'`)
	require.Contains(t, sql, "k = 'codex_harvest_proxy_url'")
	// extra 可能是 NULL 或非对象 JSON，jsonb_object_keys 对它们会报错。
	require.Contains(t, sql, "jsonb_typeof(extra) = 'object'")
}
