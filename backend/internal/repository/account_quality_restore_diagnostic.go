package repository

import "github.com/Wei-Shaw/sub2api/internal/service"

func qualityRestoreConflict(reason string) *service.QualityRestoreDiagnostic {
	return &service.QualityRestoreDiagnostic{Reason: reason, Disposition: "manual_required", OwnershipRetained: true}
}
