package repository

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIQCheckStateIsSchedulerNeutral(t *testing.T) {
	t.Parallel()

	require.True(t, isSchedulerNeutralExtraKey("iq_check_state"))
	require.False(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{
		"iq_check_state": map[string]any{"disabled_by_check": true},
	}))
	// The settings edited from the account form still go through the outbox.
	require.False(t, isSchedulerNeutralExtraKey("iq_check_enabled"))
}
