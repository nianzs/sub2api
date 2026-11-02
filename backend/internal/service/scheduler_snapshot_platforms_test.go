//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSchedulerSnapshotPlatformsPreserveKiroAndGrok(t *testing.T) {
	platforms := schedulerSnapshotPlatforms()

	require.Contains(t, platforms, PlatformKiro)
	require.Contains(t, platforms, PlatformGrok)
	// 只校验集合一致（AllowedQuotaPlatforms 内容相同），不锁顺序：
	// 平台声明顺序调整不应破坏本测试。
	require.ElementsMatch(t, AllowedQuotaPlatforms, platforms)
}
