package repository

// Ported from ranxi2001/sub2api@f895ea743 (backend/internal/repository/account_quality_groups.go).
// account_groups has no allowed_models column here, and the scheduler outbox
// event goes through this fork's enqueueSchedulerOutbox helper.

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Callers hold the plan/account locks and commit ownership plus outbox in one transaction.
func removeQualityGroups(ctx context.Context, tx *sql.Tx, accountID int64, targets []int64, state *qualityState) error {
	raw, err := qualityGroups(ctx, tx, accountID)
	if err != nil {
		return err
	}
	var groups []qualityGroup
	if err = json.Unmarshal(raw, &groups); err != nil {
		return err
	}
	selected := map[int64]bool{}
	for _, id := range targets {
		selected[id] = true
	}
	for _, group := range groups {
		if !selected[group.GroupID] {
			continue
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1 AND group_id=$2`, accountID, group.GroupID); err != nil {
			return err
		}
		state.Removed = append(state.Removed, group)
	}
	if len(state.Removed) > 0 {
		_, err = tx.ExecContext(ctx, `UPDATE accounts SET updated_at=clock_timestamp() WHERE id=$1`, accountID)
	}
	return err
}

func restoreQualityGroups(ctx context.Context, tx *sql.Tx, accountID int64, state *qualityState) (bool, error) {
	// Validate the entire restore before inserting anything. A manual membership
	// replacement must never be silently claimed as a successful rule restore.
	diagnostic := qualityRestoreConflict("membership_already_present")
	conflict, allPresent := false, len(state.Removed) > 0
	for _, group := range state.Removed {
		var exists, replaced bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM groups WHERE id=$1 AND deleted_at IS NULL),EXISTS(SELECT 1 FROM account_groups WHERE account_id=$2 AND group_id=$1)`, group.GroupID, accountID).Scan(&exists, &replaced); err != nil {
			return false, err
		}
		diagnostic.Groups = append(diagnostic.Groups, service.QualityRestoreGroupEvidence{GroupID: group.GroupID, GroupExists: exists, MembershipPresent: replaced})
		allPresent = allPresent && exists && replaced
		if !exists {
			diagnostic.Reason = "group_missing"
		}
		conflict = conflict || !exists || replaced
	}
	if conflict {
		// Presence only: priority and the creator belong to the external writer.
		// Do not claim its membership or clear our snapshot.
		if allPresent {
			diagnostic.Disposition = "external_membership_present"
		}
		state.RestoreDiagnostic = diagnostic
		return true, nil
	}
	for _, group := range state.Removed {
		if _, err := tx.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority,created_at) VALUES($1,$2,$3,$4)`, accountID, group.GroupID, group.Priority, group.CreatedAt); err != nil {
			return false, err
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE accounts SET updated_at=clock_timestamp() WHERE id=$1`, accountID)
	return false, err
}

// qualityGroupsChanged invalidates the scheduler snapshot for the account and
// every group it belonged to before or after the change. The scheduler reloads
// the account, so schedulable flips propagate through the same event.
func qualityGroupsChanged(ctx context.Context, tx *sql.Tx, accountID int64, before []byte, removed []qualityGroup) error {
	var groups []qualityGroup
	if err := json.Unmarshal(before, &groups); err != nil {
		return err
	}
	ids := make([]int64, 0, len(groups)+len(removed))
	for _, group := range groups {
		ids = append(ids, group.GroupID)
	}
	for _, group := range removed {
		ids = append(ids, group.GroupID)
	}
	return enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountGroupsChanged, &accountID, nil, buildSchedulerGroupPayload(ids))
}
