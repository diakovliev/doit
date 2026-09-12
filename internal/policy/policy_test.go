package policy

import (
	"context"
	"testing"

	"github.com/diakovliev/doit/internal/tools"
)

func TestDefaultPolicyAllowsReadOnlyActions(t *testing.T) {
	decision := (DefaultPolicy{}).Decide(context.Background(), Action{Risk: tools.RiskReadOnly})
	if decision != DecisionAllow {
		t.Fatalf("expected allow, got %q", decision)
	}
}

func TestDefaultPolicyConfirmsWrites(t *testing.T) {
	decision := (DefaultPolicy{}).Decide(context.Background(), Action{Risk: tools.RiskWrite})
	if decision != DecisionConfirm {
		t.Fatalf("expected confirmation, got %q", decision)
	}
}

func TestDefaultPolicyRejectsUnsafeNonInteractiveActions(t *testing.T) {
	decision := (DefaultPolicy{}).Decide(context.Background(), Action{
		Risk:           tools.RiskProcess,
		NonInteractive: true,
	})
	if decision != DecisionDeny {
		t.Fatalf("expected deny, got %q", decision)
	}
}

func TestDefaultPolicyRejectsOutsideWorkspaceAndNetwork(t *testing.T) {
	policy := DefaultPolicy{}
	if decision := policy.Decide(context.Background(), Action{Risk: tools.RiskReadOnly, OutsideWorkspace: true}); decision != DecisionDeny {
		t.Fatalf("expected outside-workspace action to be denied, got %q", decision)
	}
	if decision := policy.Decide(context.Background(), Action{Risk: tools.RiskReadOnly, UsesNetwork: true}); decision != DecisionDeny {
		t.Fatalf("expected network action to be denied, got %q", decision)
	}
}

func TestDefaultPolicyCancelsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if decision := (DefaultPolicy{}).Decide(ctx, Action{Risk: tools.RiskReadOnly}); decision != DecisionCancel {
		t.Fatalf("expected cancelled action, got %q", decision)
	}
}

func TestDefaultPolicyRejectsExplicitlyRejectedRisk(t *testing.T) {
	if decision := (DefaultPolicy{}).Decide(context.Background(), Action{Risk: tools.RiskRejected}); decision != DecisionDeny {
		t.Fatalf("expected rejected risk to be denied, got %q", decision)
	}
}
