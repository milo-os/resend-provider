package mockprovider

import (
	"context"

	"go.miloapis.com/email-provider-resend/internal/emailprovider"
)

// MockEmailProvider implements emailprovider.EmailProvider for tests.
// It records inputs and exposes configurable outputs/errors per method.
type MockEmailProvider struct {
	// SendEmail
	SendEmailOutput    emailprovider.SendEmailOutput
	SendEmailErr       error
	SendEmailCallCount int
	LastSendEmailInput emailprovider.SendEmailInput

	// CreateContactGroup
	CreateContactGroupOutput emailprovider.CreateContactGroupOutput
	CreateContactGroupErr    error
	CreatedGroups            []emailprovider.CreateContactGroupInput

	// GetContactGroup
	GetContactGroupOutput emailprovider.GetContactGroupOutput
	GetContactGroupErr    error

	// DeleteContactGroup
	DeleteContactGroupOutput emailprovider.DeleteContactGroupOutput
	DeleteContactGroupErr    error
	DeletedGroupID           string

	// ListContactGroups
	ListContactGroupsOutput emailprovider.ListContactGroupsOutput
	ListContactGroupsErr    error

	// CreateContactGroupMembership
	CreateContactGroupMembershipOutput    emailprovider.CreateContactGroupMembershipOutput
	CreateContactGroupMembershipErr       error
	CreatedMembershipInputs               []emailprovider.CreateContactGroupMembershipInput
	CreateContactGroupMembershipCallCount int

	// DeleteContactGroupMembership
	DeleteContactGroupMembershipOutput    emailprovider.DeleteContactGroupMembershipOutput
	DeleteContactGroupMembershipOutputSet bool
	DeleteContactGroupMembershipErr       error
	DeleteContactGroupMembershipInputs    []emailprovider.DeleteContactGroupMembershipInput

	// CreateContact tracking
	CreateContactOutput    emailprovider.CreateContactOutput
	CreateContactErr       error
	CreateContactCallCount int
	LastCreateContactInput emailprovider.CreateContactInput

	// DeleteContact tracking
	DeleteContactOutput    emailprovider.DeleteContactOutput
	DeleteContactErr       error
	DeleteContactCallCount int
	LastDeleteContactInput emailprovider.DeleteContactInput
}

func (m *MockEmailProvider) SendEmail(ctx context.Context, input emailprovider.SendEmailInput) (emailprovider.SendEmailOutput, error) {
	m.SendEmailCallCount++
	m.LastSendEmailInput = input
	return m.SendEmailOutput, m.SendEmailErr
}

func (m *MockEmailProvider) CreateContactGroup(ctx context.Context, input emailprovider.CreateContactGroupInput) (emailprovider.CreateContactGroupOutput, error) {
	m.CreatedGroups = append(m.CreatedGroups, input)
	return m.CreateContactGroupOutput, m.CreateContactGroupErr
}

func (m *MockEmailProvider) GetContactGroup(ctx context.Context, input emailprovider.GetContactGroupInput) (emailprovider.GetContactGroupOutput, error) {
	// Default to echoing back ID if not explicitly set
	if (m.GetContactGroupOutput == emailprovider.GetContactGroupOutput{}) {
		return emailprovider.GetContactGroupOutput{ContactGroupID: input.ContactGroupID}, m.GetContactGroupErr
	}
	return m.GetContactGroupOutput, m.GetContactGroupErr
}

func (m *MockEmailProvider) DeleteContactGroup(ctx context.Context, input emailprovider.DeleteContactGroupInput) (emailprovider.DeleteContactGroupOutput, error) {
	m.DeletedGroupID = input.ContactGroupID
	if (m.DeleteContactGroupOutput == emailprovider.DeleteContactGroupOutput{}) {
		return emailprovider.DeleteContactGroupOutput{ContactGroupID: input.ContactGroupID, Deleted: true}, m.DeleteContactGroupErr
	}
	return m.DeleteContactGroupOutput, m.DeleteContactGroupErr
}

func (m *MockEmailProvider) ListContactGroups(ctx context.Context) (emailprovider.ListContactGroupsOutput, error) {
	return m.ListContactGroupsOutput, m.ListContactGroupsErr
}

func (m *MockEmailProvider) CreateContactGroupMembership(ctx context.Context, input emailprovider.CreateContactGroupMembershipInput) (emailprovider.CreateContactGroupMembershipOutput, error) {
	m.CreateContactGroupMembershipCallCount++
	m.CreatedMembershipInputs = append(m.CreatedMembershipInputs, input)
	return m.CreateContactGroupMembershipOutput, m.CreateContactGroupMembershipErr
}

func (m *MockEmailProvider) DeleteContactGroupMembership(ctx context.Context, input emailprovider.DeleteContactGroupMembershipInput) (emailprovider.DeleteContactGroupMembershipOutput, error) {
	m.DeleteContactGroupMembershipInputs = append(m.DeleteContactGroupMembershipInputs, input)
	if !m.DeleteContactGroupMembershipOutputSet {
		return emailprovider.DeleteContactGroupMembershipOutput{Deleted: true}, m.DeleteContactGroupMembershipErr
	}
	return m.DeleteContactGroupMembershipOutput, m.DeleteContactGroupMembershipErr
}

func (m *MockEmailProvider) CreateContact(ctx context.Context, input emailprovider.CreateContactInput) (emailprovider.CreateContactOutput, error) {
	m.CreateContactCallCount++
	m.LastCreateContactInput = input
	return m.CreateContactOutput, m.CreateContactErr
}

func (m *MockEmailProvider) DeleteContact(ctx context.Context, input emailprovider.DeleteContactInput) (emailprovider.DeleteContactOutput, error) {
	m.DeleteContactCallCount++
	m.LastDeleteContactInput = input
	return m.DeleteContactOutput, m.DeleteContactErr
}
