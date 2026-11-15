package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDropBPSOAuthMigration(t *testing.T) {
	content, err := FS.ReadFile("251_drop_bps_oauth.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")

	// 与 accountRepository.Delete 等效：解除分组、删除定时测试、软删除并通知调度快照。
	require.Contains(t, sql, "type = 'bps-oauth'")
	require.Contains(t, sql, "parent_account_id IN")
	require.Contains(t, sql, "DELETE FROM account_groups")
	require.Contains(t, sql, "DELETE FROM scheduled_test_plans")
	require.Contains(t, sql, "SET deleted_at = NOW()")
	require.Contains(t, sql, "INSERT INTO scheduler_outbox (event_type, account_id) SELECT 'account_changed', id")
	// 只处理尚未删除的账号，重复执行不会再改动已删除的行。
	require.Contains(t, sql, "WHERE deleted_at IS NULL")

	// 下划线在 LIKE 中是单字符通配，必须转义。
	require.Contains(t, sql, `LIKE 'openai\_excel\_bps%'`)
	// extra 可能是 NULL 或非对象 JSON，jsonb_object_keys 对它们会报错。
	require.Contains(t, sql, "jsonb_typeof(extra) = 'object'")
	require.Contains(t, sql, "DELETE FROM settings WHERE key = 'excel_bps_enabled'")
}
