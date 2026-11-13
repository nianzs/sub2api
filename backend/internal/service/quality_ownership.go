package service

// Ported from ranxi2001/sub2api@f895ea743. The ownership preview/reconcile API
// only serves model isolation, which is not ported.

import (
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const QualityScopeConflictReason = "QUALITY_ISOLATION_SCOPE_CONFLICT"

func QualityScopeConflict(message string) error {
	return infraerrors.Conflict(QualityScopeConflictReason, message)
}
