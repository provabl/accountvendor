// SPDX-FileCopyrightText: 2026 Playground Logic LLC
// SPDX-License-Identifier: Apache-2.0

package provision

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
)

// mockOrg implements orgAPI with scriptable responses. The CreateAccount status
// is a queue so the async poll can be driven through IN_PROGRESS → terminal.
type mockOrg struct {
	roots []string
	// ouPages maps parent id → successive pages of children (to exercise paging).
	ouPages map[string][][]OU

	createErr    error
	createStatus *orgtypes.CreateAccountStatus
	statusQueue  []*orgtypes.CreateAccountStatus
	statusErr    error
	statusCalls  int

	accountName string
	describeErr error

	currentParent string
	parentsErr    error
	moveErr       error
	moveCalls     int
	movedFrom     string
	movedTo       string

	tagErr   error
	tagCalls int
	gotTags  map[string]string
}

func (m *mockOrg) ListRoots(context.Context, *organizations.ListRootsInput, ...func(*organizations.Options)) (*organizations.ListRootsOutput, error) {
	out := &organizations.ListRootsOutput{}
	for _, r := range m.roots {
		out.Roots = append(out.Roots, orgtypes.Root{Id: aws.String(r)})
	}
	return out, nil
}

func (m *mockOrg) ListOrganizationalUnitsForParent(_ context.Context, in *organizations.ListOrganizationalUnitsForParentInput, _ ...func(*organizations.Options)) (*organizations.ListOrganizationalUnitsForParentOutput, error) {
	pages := m.ouPages[aws.ToString(in.ParentId)]
	idx := 0
	if in.NextToken != nil {
		// Token is just the next page index, encoded as a digit.
		idx = int(aws.ToString(in.NextToken)[0] - '0')
	}
	if idx >= len(pages) {
		return &organizations.ListOrganizationalUnitsForParentOutput{}, nil
	}
	out := &organizations.ListOrganizationalUnitsForParentOutput{}
	for _, ou := range pages[idx] {
		out.OrganizationalUnits = append(out.OrganizationalUnits, orgtypes.OrganizationalUnit{
			Id: aws.String(ou.ID), Name: aws.String(ou.Name),
		})
	}
	if idx+1 < len(pages) {
		out.NextToken = aws.String(string(rune('0' + idx + 1)))
	}
	return out, nil
}

func (m *mockOrg) ListParents(context.Context, *organizations.ListParentsInput, ...func(*organizations.Options)) (*organizations.ListParentsOutput, error) {
	if m.parentsErr != nil {
		return nil, m.parentsErr
	}
	if m.currentParent == "" {
		return &organizations.ListParentsOutput{}, nil
	}
	return &organizations.ListParentsOutput{
		Parents: []orgtypes.Parent{{Id: aws.String(m.currentParent)}},
	}, nil
}

func (m *mockOrg) DescribeAccount(context.Context, *organizations.DescribeAccountInput, ...func(*organizations.Options)) (*organizations.DescribeAccountOutput, error) {
	if m.describeErr != nil {
		return nil, m.describeErr
	}
	return &organizations.DescribeAccountOutput{
		Account: &orgtypes.Account{Name: aws.String(m.accountName)},
	}, nil
}

func (m *mockOrg) CreateAccount(context.Context, *organizations.CreateAccountInput, ...func(*organizations.Options)) (*organizations.CreateAccountOutput, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	return &organizations.CreateAccountOutput{CreateAccountStatus: m.createStatus}, nil
}

func (m *mockOrg) DescribeCreateAccountStatus(context.Context, *organizations.DescribeCreateAccountStatusInput, ...func(*organizations.Options)) (*organizations.DescribeCreateAccountStatusOutput, error) {
	m.statusCalls++
	if m.statusErr != nil {
		return nil, m.statusErr
	}
	if len(m.statusQueue) == 0 {
		return &organizations.DescribeCreateAccountStatusOutput{}, nil
	}
	st := m.statusQueue[0]
	if len(m.statusQueue) > 1 {
		m.statusQueue = m.statusQueue[1:]
	}
	return &organizations.DescribeCreateAccountStatusOutput{CreateAccountStatus: st}, nil
}

func (m *mockOrg) MoveAccount(_ context.Context, in *organizations.MoveAccountInput, _ ...func(*organizations.Options)) (*organizations.MoveAccountOutput, error) {
	m.moveCalls++
	if m.moveErr != nil {
		return nil, m.moveErr
	}
	m.movedFrom = aws.ToString(in.SourceParentId)
	m.movedTo = aws.ToString(in.DestinationParentId)
	return &organizations.MoveAccountOutput{}, nil
}

func (m *mockOrg) TagResource(_ context.Context, in *organizations.TagResourceInput, _ ...func(*organizations.Options)) (*organizations.TagResourceOutput, error) {
	m.tagCalls++
	if m.tagErr != nil {
		return nil, m.tagErr
	}
	m.gotTags = map[string]string{}
	for _, t := range in.Tags {
		m.gotTags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return &organizations.TagResourceOutput{}, nil
}

// newTestProv wires the mock with an instant clock so polls don't really sleep.
func newTestProv(m *mockOrg) *AWSProvisioner {
	p := newAWSProvisioner(m)
	p.sleep = func(context.Context, time.Duration) error { return nil }
	p.pollInterval = time.Second
	p.pollTimeout = 5 * time.Second
	return p
}

func succeeded(id string) *orgtypes.CreateAccountStatus {
	return &orgtypes.CreateAccountStatus{State: orgtypes.CreateAccountStateSucceeded, AccountId: aws.String(id)}
}
func inProgress() *orgtypes.CreateAccountStatus {
	return &orgtypes.CreateAccountStatus{State: orgtypes.CreateAccountStateInProgress}
}

// CreateAccount is asynchronous: the account id only exists once the request
// reaches SUCCEEDED, so the poll must run to completion before placing.
func TestCreate_PollsAsyncStatusThenMoves(t *testing.T) {
	m := &mockOrg{
		createStatus:  &orgtypes.CreateAccountStatus{Id: aws.String("car-1")},
		statusQueue:   []*orgtypes.CreateAccountStatus{inProgress(), inProgress(), succeeded("123456789012")},
		currentParent: "r-root",
	}
	acct, err := newTestProv(m).Create(context.Background(), CreateRequest{
		Name: "chen", Email: "chen@x.org", ParentOU: "ou-nih",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if acct.ID != "123456789012" {
		t.Errorf("account id = %q", acct.ID)
	}
	if m.statusCalls != 3 {
		t.Errorf("expected to poll until SUCCEEDED (3 calls), got %d", m.statusCalls)
	}
	if m.movedFrom != "r-root" || m.movedTo != "ou-nih" {
		t.Errorf("move = %s → %s, want r-root → ou-nih", m.movedFrom, m.movedTo)
	}
}

// A FAILED request must surface AWS's FailureReason — it's the actionable part
// (EMAIL_ALREADY_EXISTS, ACCOUNT_LIMIT_EXCEEDED, …).
func TestCreate_FailedStatusSurfacesReason(t *testing.T) {
	m := &mockOrg{
		createStatus: &orgtypes.CreateAccountStatus{Id: aws.String("car-1")},
		statusQueue: []*orgtypes.CreateAccountStatus{{
			State:         orgtypes.CreateAccountStateFailed,
			FailureReason: orgtypes.CreateAccountFailureReasonEmailAlreadyExists,
		}},
	}
	_, err := newTestProv(m).Create(context.Background(), CreateRequest{Name: "c", Email: "e", ParentOU: "ou-1"})
	if err == nil {
		t.Fatal("expected a FAILED error")
	}
	if !strings.Contains(err.Error(), "EMAIL_ALREADY_EXISTS") {
		t.Errorf("error should carry the AWS failure reason, got: %v", err)
	}
	if m.moveCalls != 0 {
		t.Error("must not attempt a move when creation failed")
	}
}

// The dangerous case: the account EXISTS but placement failed. The error must say
// so and must NOT read like "create didn't happen" — re-running create would vend
// a second account that can never be deleted.
func TestCreate_MoveFailureWarnsAccountExists(t *testing.T) {
	m := &mockOrg{
		createStatus:  &orgtypes.CreateAccountStatus{Id: aws.String("car-1")},
		statusQueue:   []*orgtypes.CreateAccountStatus{succeeded("123456789012")},
		currentParent: "r-root",
		moveErr:       errors.New("AccessDenied"),
	}
	_, err := newTestProv(m).Create(context.Background(), CreateRequest{Name: "c", Email: "e", ParentOU: "ou-1"})
	if err == nil {
		t.Fatal("expected a placement error")
	}
	msg := err.Error()
	for _, want := range []string{"123456789012", "CREATED", "do NOT re-run create", "vendor adopt"} {
		if !strings.Contains(msg, want) {
			t.Errorf("placement error must mention %q so the operator doesn't vend a duplicate; got: %v", want, err)
		}
	}
}

// A poll that never terminates must time out with a warning that the request may
// still be in flight — not a bare "failed".
func TestCreate_PollTimeoutWarnsInFlight(t *testing.T) {
	m := &mockOrg{
		createStatus: &orgtypes.CreateAccountStatus{Id: aws.String("car-1")},
		statusQueue:  []*orgtypes.CreateAccountStatus{inProgress()},
	}
	_, err := newTestProv(m).Create(context.Background(), CreateRequest{Name: "c", Email: "e", ParentOU: "ou-1"})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), "IN_PROGRESS") || !strings.Contains(err.Error(), "second account") {
		t.Errorf("timeout must warn the request may still be in flight, got: %v", err)
	}
}

func TestCreate_RequiresNameEmailParent(t *testing.T) {
	m := &mockOrg{}
	for _, req := range []CreateRequest{
		{Email: "e", ParentOU: "ou-1"},
		{Name: "n", ParentOU: "ou-1"},
		{Name: "n", Email: "e"},
	} {
		if _, err := newTestProv(m).Create(context.Background(), req); err == nil {
			t.Errorf("expected an error for %+v", req)
		}
	}
	if m.statusCalls != 0 {
		t.Error("must not call AWS with an incomplete request")
	}
}

func TestAdopt_MovesExistingAccount(t *testing.T) {
	m := &mockOrg{accountName: "existing-acct", currentParent: "r-root"}
	acct, err := newTestProv(m).Adopt(context.Background(), "999988887777", "ou-nih")
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if acct.ID != "999988887777" || acct.Name != "existing-acct" {
		t.Errorf("adopted account = %+v", acct)
	}
	if m.movedTo != "ou-nih" {
		t.Errorf("moved to %q, want ou-nih", m.movedTo)
	}
}

// Adopt must be idempotent: an account already in the target OU is a no-op
// success, so re-running an adopt to re-apply tags/compile doesn't fail.
func TestAdopt_AlreadyInTargetIsNoOp(t *testing.T) {
	m := &mockOrg{accountName: "a", currentParent: "ou-nih"}
	if _, err := newTestProv(m).Adopt(context.Background(), "999988887777", "ou-nih"); err != nil {
		t.Fatalf("re-adopting into the same OU should succeed: %v", err)
	}
	if m.moveCalls != 0 {
		t.Errorf("expected no MoveAccount call when already placed, got %d", m.moveCalls)
	}
}

// A bad account id should fail on the read, before any write.
func TestAdopt_UnknownAccountFailsOnDescribe(t *testing.T) {
	m := &mockOrg{describeErr: errors.New("AccountNotFoundException")}
	if _, err := newTestProv(m).Adopt(context.Background(), "000000000000", "ou-1"); err == nil {
		t.Fatal("expected a describe error")
	}
	if m.moveCalls != 0 {
		t.Error("must not move an account that couldn't be described")
	}
}

func TestAdopt_RequiresIDAndParent(t *testing.T) {
	p := newTestProv(&mockOrg{})
	if _, err := p.Adopt(context.Background(), "", "ou-1"); err == nil {
		t.Error("expected an error for an empty account id")
	}
	if _, err := p.Adopt(context.Background(), "1", ""); err == nil {
		t.Error("expected an error for an empty parent")
	}
}

func TestTag_AppliesTags(t *testing.T) {
	m := &mockOrg{}
	p := newTestProv(m)
	if err := p.Tag(context.Background(), "123", map[string]string{"data-class": "GENOMIC"}); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	if m.gotTags["data-class"] != "GENOMIC" {
		t.Errorf("tags = %v", m.gotTags)
	}
	// No tags → no API call.
	if err := p.Tag(context.Background(), "123", nil); err != nil {
		t.Fatalf("Tag(nil): %v", err)
	}
	if m.tagCalls != 1 {
		t.Errorf("expected 1 TagResource call, got %d", m.tagCalls)
	}
}

// Children must follow NextToken — a large org must not be silently truncated,
// which would make an existing OU look missing and misplace an account.
func TestChildren_Paginates(t *testing.T) {
	m := &mockOrg{ouPages: map[string][][]OU{
		"r-root": {
			{{ID: "ou-1", Name: "Security"}},
			{{ID: "ou-2", Name: "SensitiveResearch"}},
		},
	}}
	got, err := newTestProv(m).Children(context.Background(), "r-root")
	if err != nil {
		t.Fatalf("Children: %v", err)
	}
	if len(got) != 2 || got[1].Name != "SensitiveResearch" {
		t.Errorf("expected both pages, got %+v", got)
	}
}

// The live provisioner must satisfy both interfaces the orchestrator needs.
func TestAWSProvisionerImplementsInterfaces(t *testing.T) {
	var _ Provisioner = (*AWSProvisioner)(nil)
	var _ OULister = (*AWSProvisioner)(nil)
}
