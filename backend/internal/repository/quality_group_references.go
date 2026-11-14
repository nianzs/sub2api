package repository

// Ported from ranxi2001/sub2api@f895ea743. This fork has no observer roles.

import (
	"context"
	"fmt"
	"sort"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Group removal affects only the target account's existing memberships. A rule
// may name a group the account has not joined, but not a deleted/nonexistent group.
func validateQualityGroupReferences(ctx context.Context, q planRowQuerier, cfg *service.PelicanTestConfig) error {
	if cfg == nil || cfg.Quality == nil {
		return nil
	}
	policy := cfg.Quality
	ids := map[int64]bool{}
	if policy.Action == "remove_groups" {
		for _, id := range policy.RemoveGroupIDs {
			ids[id] = true
		}
	}
	ordered := []int64{}
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for _, id := range ordered {
		var exists bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM groups WHERE id=$1 AND deleted_at IS NULL)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("quality target group %d does not exist", id)
		}
	}
	return nil
}
