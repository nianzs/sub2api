package service

import "time"

// QualityRestoreDiagnostic is a result-only observation of a rejected restore.
// It never transfers ownership or asserts that the account is schedulable.
// Keep evidence explicitly allowlisted: no credentials, raw Extra or responses.
type QualityRestoreDiagnostic struct {
	Reason              string                        `json:"reason"`
	Disposition         string                        `json:"disposition"`
	Scope               string                        `json:"scope"`
	OwnershipRetained   bool                          `json:"ownership_retained"`
	Groups              []QualityRestoreGroupEvidence `json:"groups,omitempty"`
	Models              []string                      `json:"models,omitempty"`
	ChangedFields       []string                      `json:"changed_fields,omitempty"`
	AccountStatus       string                        `json:"account_status,omitempty"`
	Schedulable         *bool                         `json:"schedulable,omitempty"`
	AccountVersion      *time.Time                    `json:"account_version,omitempty"`
	OwnedAccountVersion *time.Time                    `json:"owned_account_version,omitempty"`
	ExpectedConcurrency *int                          `json:"expected_concurrency,omitempty"`
	CurrentConcurrency  *int                          `json:"current_concurrency,omitempty"`
}

type QualityRestoreGroupEvidence struct {
	GroupID           int64 `json:"group_id"`
	GroupExists       bool  `json:"group_exists"`
	MembershipPresent bool  `json:"membership_present"`
}
