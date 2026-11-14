package migrations

import (
	"sort"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

// expectedCompositeRouteTargetPlatforms 是 composite_model_routes.target_platform
// 允许的平台集合，需与 domain.IsConcretePlatform（internal/domain/platforms.go）同步。
// 242 号迁移起白名单改由应用层平台清单校验，不再维护数据库 CHECK 约束。
var expectedCompositeRouteTargetPlatforms = []string{
	"anthropic", "antigravity", "cline", "command_code", "deepseek", "fish",
	"gemini", "grok", "kimi", "kiro", "minimax", "openai", "opencode_go", "typesafe", "zhipu",
}

const compositeRouteTargetPlatformConstraint = "composite_model_routes_target_platform_check"

// TestCompositeRoutesKiroMigration 校验 229 号迁移把 227 漏掉的 kiro 加进
// composite_model_routes.target_platform 的 CHECK 约束。
//
// 227 用 DROP + 重建的写法把国产 3 家加入该约束时，重建的列表里没有本 fork
// 独有的 kiro，导致管理端保存 target_platform='kiro' 的 composite 路由违约失败。
func TestCompositeRoutesKiroMigration(t *testing.T) {
	content, err := FS.ReadFile("229_composite_routes_add_kiro.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS "+compositeRouteTargetPlatformConstraint)
	require.Contains(t, sql,
		"CHECK (target_platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'kiro', 'grok', 'kimi', 'zhipu', 'deepseek'))")
}

// TestCompositeRouteTargetPlatformFinalStateCoversAllPlatforms 是防回归护栏：
// 242 号迁移把 composite_model_routes.target_platform 白名单从数据库 CHECK 约束
// 改为应用层平台清单校验（domain.IsConcretePlatform），断言清单覆盖全部允许平台
// （含 fork 的 kiro），漏项会让对应平台的 composite 路由无法保存。
func TestCompositeRouteTargetPlatformFinalStateCoversAllPlatforms(t *testing.T) {
	got := append([]string{}, domain.ConcretePlatformIDs()...)
	sort.Strings(got)

	require.Equal(t, expectedCompositeRouteTargetPlatforms, got,
		"domain.ConcretePlatformIDs 与 composite 路由允许的目标平台不一致；"+
			"新平台必须登记进 domain/platforms.go，漏项会让对应平台的 composite 路由无法保存")
}
