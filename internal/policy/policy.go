// Package policy evaluates whether a requested action may execute.
package policy

import (
	"context"

	"github.com/diakovliev/doit/internal/tools"
)

// Decision is the policy outcome for an action.
type Decision string

const (
	DecisionAllow   Decision = "allow"
	DecisionConfirm Decision = "confirm"
	DecisionDeny    Decision = "deny"
	DecisionCancel  Decision = "cancel"
)

// Action contains the side-effect metadata needed for a policy decision.
type Action struct {
	Name                string
	Risk                tools.Risk
	OutsideWorkspace    bool
	UsesNetwork         bool
	NonInteractive      bool
	WorkspaceAutomation bool
}

// ApprovalPolicy decides without executing the action.
type ApprovalPolicy interface {
	Decide(context.Context, Action) Decision
}

// DefaultPolicy implements the Phase 1 conservative policy.
type DefaultPolicy struct{}

// Decide allows scoped read-only actions, confirms local side effects, and
// rejects unsafe or out-of-scope operations.
func (DefaultPolicy) Decide(ctx context.Context, action Action) Decision {
	if ctx.Err() != nil {
		return DecisionCancel
	}
	if action.OutsideWorkspace || action.UsesNetwork || action.Risk == tools.RiskRejected {
		return DecisionDeny
	}
	if action.Risk == tools.RiskReadOnly {
		return DecisionAllow
	}
	if action.WorkspaceAutomation {
		return DecisionAllow
	}
	if action.NonInteractive {
		return DecisionDeny
	}
	return DecisionConfirm
}
