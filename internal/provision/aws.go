// SPDX-FileCopyrightText: 2026 Playground Logic LLC
// SPDX-License-Identifier: Apache-2.0

package provision

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
)

// orgAPI is the slice of AWS Organizations the live provisioner uses. Narrow on
// purpose: it names exactly the calls vendor makes, so the blast radius is
// reviewable and the adapter is mockable.
type orgAPI interface {
	ListRoots(ctx context.Context, in *organizations.ListRootsInput, optFns ...func(*organizations.Options)) (*organizations.ListRootsOutput, error)
	ListOrganizationalUnitsForParent(ctx context.Context, in *organizations.ListOrganizationalUnitsForParentInput, optFns ...func(*organizations.Options)) (*organizations.ListOrganizationalUnitsForParentOutput, error)
	ListParents(ctx context.Context, in *organizations.ListParentsInput, optFns ...func(*organizations.Options)) (*organizations.ListParentsOutput, error)
	DescribeAccount(ctx context.Context, in *organizations.DescribeAccountInput, optFns ...func(*organizations.Options)) (*organizations.DescribeAccountOutput, error)
	CreateAccount(ctx context.Context, in *organizations.CreateAccountInput, optFns ...func(*organizations.Options)) (*organizations.CreateAccountOutput, error)
	DescribeCreateAccountStatus(ctx context.Context, in *organizations.DescribeCreateAccountStatusInput, optFns ...func(*organizations.Options)) (*organizations.DescribeCreateAccountStatusOutput, error)
	MoveAccount(ctx context.Context, in *organizations.MoveAccountInput, optFns ...func(*organizations.Options)) (*organizations.MoveAccountOutput, error)
	TagResource(ctx context.Context, in *organizations.TagResourceInput, optFns ...func(*organizations.Options)) (*organizations.TagResourceOutput, error)
}

// AWSProvisioner is the live AWS Organizations implementation of Provisioner and
// OULister. It must run in the organization's **management account**.
type AWSProvisioner struct {
	api orgAPI
	// sleep is overridable in tests so the CreateAccount poll doesn't really wait.
	sleep func(context.Context, time.Duration) error
	// pollInterval and pollTimeout bound the async CreateAccount handshake.
	pollInterval time.Duration
	pollTimeout  time.Duration
}

// NewAWSProvisioner builds a live provisioner from an AWS config.
func NewAWSProvisioner(cfg aws.Config) *AWSProvisioner {
	return newAWSProvisioner(organizations.NewFromConfig(cfg))
}

func newAWSProvisioner(api orgAPI) *AWSProvisioner {
	return &AWSProvisioner{
		api:          api,
		sleep:        sleepCtx,
		pollInterval: 10 * time.Second,
		pollTimeout:  15 * time.Minute,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Roots implements OULister.
func (p *AWSProvisioner) Roots(ctx context.Context) ([]string, error) {
	out, err := p.api.ListRoots(ctx, &organizations.ListRootsInput{})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Roots))
	for _, r := range out.Roots {
		ids = append(ids, aws.ToString(r.Id))
	}
	return ids, nil
}

// Children implements OULister, paginating so a large org isn't silently truncated.
func (p *AWSProvisioner) Children(ctx context.Context, parentID string) ([]OU, error) {
	var ous []OU
	var next *string
	for {
		out, err := p.api.ListOrganizationalUnitsForParent(ctx, &organizations.ListOrganizationalUnitsForParentInput{
			ParentId:  aws.String(parentID),
			NextToken: next,
		})
		if err != nil {
			return nil, err
		}
		for _, ou := range out.OrganizationalUnits {
			ous = append(ous, OU{ID: aws.ToString(ou.Id), Name: aws.ToString(ou.Name)})
		}
		if out.NextToken == nil {
			return ous, nil
		}
		next = out.NextToken
	}
}

// Create vends a NEW account and moves it under parentOU.
//
// This is the irreversible call in the entire suite: an AWS account can be closed
// (90-day suspension) but never deleted, and its root email can't be reused. Two
// consequences shape this implementation:
//
//  1. CreateAccount is **asynchronous** — it returns a request id, and the account
//     id only exists once the request reaches SUCCEEDED. Polling is mandatory, not
//     an optimization; there is no synchronous form.
//  2. The account is created at the **root**, then moved. If the move fails, the
//     account exists but is unplaced — so that error says exactly that, with the
//     account id, because the operator must not re-run Create (they'd vend a
//     second orphan) but instead move the existing one.
func (p *AWSProvisioner) Create(ctx context.Context, req CreateRequest) (*Account, error) {
	if req.Name == "" || req.Email == "" || req.ParentOU == "" {
		return nil, fmt.Errorf("name, email, and parent OU are all required to create an account")
	}

	out, err := p.api.CreateAccount(ctx, &organizations.CreateAccountInput{
		AccountName: aws.String(req.Name),
		Email:       aws.String(req.Email),
	})
	if err != nil {
		return nil, fmt.Errorf("organizations:CreateAccount: %w", err)
	}
	if out.CreateAccountStatus == nil {
		return nil, errors.New("organizations:CreateAccount returned no status")
	}

	accountID, err := p.awaitCreate(ctx, aws.ToString(out.CreateAccountStatus.Id))
	if err != nil {
		return nil, err
	}

	// Placement is a separate call; the account already exists by now.
	if _, err := p.moveToParent(ctx, accountID, req.ParentOU); err != nil {
		return nil, fmt.Errorf("account %s was CREATED but could not be placed under %s: %w\n"+
			"  The account exists — do NOT re-run create (that would vend a second account, and an "+
			"account can never be deleted). Move this one instead: vendor adopt %s --parent %s",
			accountID, req.ParentOU, err, accountID, req.ParentOU)
	}
	return &Account{ID: accountID, Name: req.Name}, nil
}

// awaitCreate polls the async CreateAccount request to a terminal state.
func (p *AWSProvisioner) awaitCreate(ctx context.Context, requestID string) (string, error) {
	deadline := p.pollTimeout
	var waited time.Duration
	for {
		out, err := p.api.DescribeCreateAccountStatus(ctx, &organizations.DescribeCreateAccountStatusInput{
			CreateAccountRequestId: aws.String(requestID),
		})
		if err != nil {
			return "", fmt.Errorf("poll CreateAccount request %s: %w", requestID, err)
		}
		st := out.CreateAccountStatus
		if st == nil {
			return "", fmt.Errorf("poll CreateAccount request %s: no status returned", requestID)
		}

		switch st.State {
		case orgtypes.CreateAccountStateSucceeded:
			id := aws.ToString(st.AccountId)
			if id == "" {
				return "", fmt.Errorf("CreateAccount %s SUCCEEDED but returned no account id", requestID)
			}
			return id, nil
		case orgtypes.CreateAccountStateFailed:
			// FailureReason is the actionable part (EMAIL_ALREADY_EXISTS,
			// ACCOUNT_LIMIT_EXCEEDED, …) — surface it verbatim.
			return "", fmt.Errorf("CreateAccount %s FAILED: %s", requestID, st.FailureReason)
		}

		if waited >= deadline {
			return "", fmt.Errorf("CreateAccount %s still IN_PROGRESS after %s — the account may yet be created; "+
				"check 'aws organizations describe-create-account-status --create-account-request-id %s' before retrying, "+
				"because re-running create would vend a second account",
				requestID, waited, requestID)
		}
		if err := p.sleep(ctx, p.pollInterval); err != nil {
			return "", fmt.Errorf("CreateAccount %s: %w (the request is still in flight; verify before retrying)", requestID, err)
		}
		waited += p.pollInterval
	}
}

// Adopt places an EXISTING account under parentOU — the reversible path used to
// validate the whole pipeline before Create is ever exercised live.
//
// It verifies the account exists first, so a typo'd id fails on a read rather
// than a write, and it is idempotent: an account already in the target OU is a
// no-op success rather than an InvalidInput error (re-running an adopt to
// re-apply tags/compile must not fail on placement).
func (p *AWSProvisioner) Adopt(ctx context.Context, accountID, parentOU string) (*Account, error) {
	if accountID == "" || parentOU == "" {
		return nil, fmt.Errorf("account id and parent OU are required to adopt")
	}
	desc, err := p.api.DescribeAccount(ctx, &organizations.DescribeAccountInput{AccountId: aws.String(accountID)})
	if err != nil {
		return nil, fmt.Errorf("describe account %s (is it in this organization?): %w", accountID, err)
	}
	name := ""
	if desc.Account != nil {
		name = aws.ToString(desc.Account.Name)
	}
	if _, err := p.moveToParent(ctx, accountID, parentOU); err != nil {
		return nil, err
	}
	return &Account{ID: accountID, Name: name}, nil
}

// moveToParent moves accountID under parentID, resolving its current parent
// (MoveAccount requires the source). Returns false when the account was already
// there (no call made).
func (p *AWSProvisioner) moveToParent(ctx context.Context, accountID, parentID string) (bool, error) {
	parents, err := p.api.ListParents(ctx, &organizations.ListParentsInput{ChildId: aws.String(accountID)})
	if err != nil {
		return false, fmt.Errorf("list current parent of %s: %w", accountID, err)
	}
	if len(parents.Parents) == 0 {
		return false, fmt.Errorf("account %s has no parent in this organization", accountID)
	}
	source := aws.ToString(parents.Parents[0].Id)
	if source == parentID {
		return false, nil // already placed — idempotent
	}
	if _, err := p.api.MoveAccount(ctx, &organizations.MoveAccountInput{
		AccountId:           aws.String(accountID),
		SourceParentId:      aws.String(source),
		DestinationParentId: aws.String(parentID),
	}); err != nil {
		return false, fmt.Errorf("organizations:MoveAccount %s from %s to %s: %w", accountID, source, parentID, err)
	}
	return true, nil
}

// Tag applies the SRE type's tags to the account.
func (p *AWSProvisioner) Tag(ctx context.Context, accountID string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	awsTags := make([]orgtypes.Tag, 0, len(tags))
	for k, v := range tags {
		awsTags = append(awsTags, orgtypes.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	if _, err := p.api.TagResource(ctx, &organizations.TagResourceInput{
		ResourceId: aws.String(accountID),
		Tags:       awsTags,
	}); err != nil {
		return fmt.Errorf("organizations:TagResource %s: %w", accountID, err)
	}
	return nil
}

// IsNotInOrgErr reports whether err looks like "this isn't the management account
// / there's no organization", the most common first-run misconfiguration.
func IsNotInOrgErr(err error) bool {
	if err == nil {
		return false
	}
	var aae *orgtypes.AWSOrganizationsNotInUseException
	if errors.As(err, &aae) {
		return true
	}
	var ade *orgtypes.AccessDeniedForDependencyException
	if errors.As(err, &ade) {
		return true
	}
	return strings.Contains(err.Error(), "AWSOrganizationsNotInUseException")
}
