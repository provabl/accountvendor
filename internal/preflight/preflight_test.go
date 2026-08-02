// SPDX-FileCopyrightText: 2026 Playground Logic LLC
// SPDX-License-Identifier: Apache-2.0

package preflight

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type mockSTS struct {
	arn string
	err error
}

func (m mockSTS) GetCallerIdentity(_ context.Context, _ *sts.GetCallerIdentityInput, _ ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &sts.GetCallerIdentityOutput{Arn: aws.String(m.arn)}, nil
}

type mockIAMSim struct {
	denied  map[string]bool
	empty   bool
	err     error
	gotARN  string
	gotActs []string
}

func (m *mockIAMSim) SimulatePrincipalPolicy(_ context.Context, in *iam.SimulatePrincipalPolicyInput, _ ...func(*iam.Options)) (*iam.SimulatePrincipalPolicyOutput, error) {
	m.gotARN = aws.ToString(in.PolicySourceArn)
	m.gotActs = in.ActionNames
	if m.err != nil {
		return nil, m.err
	}
	if m.empty {
		return &iam.SimulatePrincipalPolicyOutput{}, nil
	}
	var results []iamtypes.EvaluationResult
	for _, a := range in.ActionNames {
		dec := iamtypes.PolicyEvaluationDecisionTypeAllowed
		if m.denied[a] {
			dec = iamtypes.PolicyEvaluationDecisionTypeExplicitDeny
		}
		results = append(results, iamtypes.EvaluationResult{EvalActionName: aws.String(a), EvalDecision: dec})
	}
	return &iam.SimulatePrincipalPolicyOutput{EvaluationResults: results}, nil
}

const testARN = "arn:aws:iam::942542972736:role/vendor-runner"

func allOK(rs []Result) bool {
	for _, r := range rs {
		if !r.Status {
			return false
		}
	}
	return true
}

func TestCheck_AllAllowed(t *testing.T) {
	sim := &mockIAMSim{}
	rs := check(context.Background(), mockSTS{arn: testARN}, sim)
	if len(rs) != len(vendorRequiredActions) {
		t.Fatalf("expected %d results, got %d", len(vendorRequiredActions), len(rs))
	}
	if !allOK(rs) {
		t.Error("expected all actions allowed → all ok")
	}
	if sim.gotARN != testARN {
		t.Errorf("simulated against %q, want the caller ARN %q", sim.gotARN, testARN)
	}
}

// The action list is the contract with docs/required-permissions.md. These are the
// calls a vend actually makes; a missing one turns into a mid-vend failure, which
// for CreateAccount means an account that exists but isn't placed.
func TestRequiredActions_CoverTheVendPath(t *testing.T) {
	have := map[string]bool{}
	for _, a := range vendorRequiredActions {
		have[a] = true
	}
	for _, want := range []string{
		// preflight itself
		"sts:GetCallerIdentity",
		"iam:SimulatePrincipalPolicy",
		// OU name → id resolution (ground names OUs; ground-meta has no ids)
		"organizations:ListRoots",
		"organizations:ListOrganizationalUnitsForParent",
		// placement, both paths
		"organizations:ListParents",
		"organizations:MoveAccount",
		// adopt
		"organizations:DescribeAccount",
		// provision (async: create returns a request id, not an account id)
		"organizations:CreateAccount",
		"organizations:DescribeCreateAccountStatus",
		// tagging
		"organizations:TagResource",
	} {
		if !have[want] {
			t.Errorf("required action %q missing — a vend would fail mid-flight on it", want)
		}
	}
}

// A denied action surfaces as a non-ok result with remediation (fail-closed).
func TestCheck_DeniedActionIsError(t *testing.T) {
	target := "organizations:CreateAccount"
	rs := check(context.Background(), mockSTS{arn: testARN}, &mockIAMSim{denied: map[string]bool{target: true}})
	var found bool
	for _, r := range rs {
		if r.Name == target {
			found = true
			if r.Status || r.Severity != "error" || r.Remediation == "" {
				t.Errorf("denied action = %+v; want non-ok error with remediation", r)
			}
			// The remediation must point at the management account — the most
			// common cause of a denial here is running against a member account.
			if !strings.Contains(r.Remediation, "MANAGEMENT") {
				t.Errorf("remediation should name the management account, got %q", r.Remediation)
			}
		}
	}
	if !found {
		t.Fatalf("no result for the denied action %q", target)
	}
	if allOK(rs) {
		t.Error("a denied action must make the set not-all-ok")
	}
}

func TestCheck_CallerIdentityErrorFailsClosed(t *testing.T) {
	rs := check(context.Background(), mockSTS{err: errors.New("ExpiredToken")}, &mockIAMSim{})
	if len(rs) != 1 || rs[0].Status {
		t.Fatalf("expected one error result on GetCallerIdentity failure, got %+v", rs)
	}
}

func TestCheck_SimulatorErrorFailsClosed(t *testing.T) {
	rs := check(context.Background(), mockSTS{arn: testARN}, &mockIAMSim{err: errors.New("AccessDenied")})
	if len(rs) != 1 || rs[0].Status {
		t.Fatalf("expected one fail-closed error result on simulator failure, got %+v", rs)
	}
	if rs[0].Remediation == "" {
		t.Error("fail-closed result should explain how to enable the self-check")
	}
}

// An empty simulator response must not read as "nothing failed, so all good".
func TestCheck_EmptyResultsFailClosed(t *testing.T) {
	rs := check(context.Background(), mockSTS{arn: testARN}, &mockIAMSim{empty: true})
	if len(rs) != 1 || rs[0].Status {
		t.Fatalf("an empty evaluation set must fail closed, got %+v", rs)
	}
}
