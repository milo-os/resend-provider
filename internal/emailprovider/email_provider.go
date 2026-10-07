package emailprovider

import (
	"context"
	"time"

	rtime "go.miloapis.com/email-provider-resend/internal/resend"
)

// SendEmailInput contains all the data required to send an email regardless of the underlying provider.
type SendEmailInput struct {
	From           string
	ReplyTo        string
	IdempotencyKey string
	To             []string
	Cc             []string
	Bcc            []string
	Subject        string
	HtmlBody       string
	TextBody       string
	// Tags are attached to the email and echoed back in webhook events.
	Tags []rtime.Tag
}

// SendEmailOutput contains the output of the email provider
type SendEmailOutput struct {
	DeliveryID string
}

// CreateContactGroupInput contains the input of the email provider
type CreateContactGroupInput struct {
	DisplayName string
}

// CreateContactGroupOutput contains the output of the email provider
type CreateContactGroupOutput struct {
	ContactGroupID string
}

// GetContactGroupInput contains the input of the email provider
type GetContactGroupInput struct {
	ContactGroupID string
}

// GetContactGroupOutput contains the output of the email provider
type GetContactGroupOutput struct {
	ContactGroupID string
	DisplayName    string
	CreatedAt      time.Time
}

// DeleteContactGroupInput contains the input of the email provider
type DeleteContactGroupInput struct {
	ContactGroupID string
}

// DeleteContactGroupOutput contains the output of the email provider
type DeleteContactGroupOutput struct {
	ContactGroupID string
	Deleted        bool
}

// ListContactGroupsOutput contains the output of the email provider
type ListContactGroupsOutput struct {
	ContactGroups []GetContactGroupOutput
}

// CreateContactGroupMembershipInput contains the input of the email provider
type CreateContactGroupMembershipInput struct {
	ContactId      string
	ContactGroupId string
}

// CreateContactGroupMembershipOutput contains the output of the email provider
type CreateContactGroupMembershipOutput struct {
	ContactGroupMembershipID string
}

// DeleteContactGroupMembershipInput contains the input of the email provider
type DeleteContactGroupMembershipInput struct {
	ContactId      string
	ContactGroupId string
}

// DeleteContactGroupMembershipOutput contains the output of the email provider
type DeleteContactGroupMembershipOutput struct {
	ContactGroupMembershipID string
	Deleted                  bool
}

// CreateContactInput contains the input of the email provider
type CreateContactInput struct {
	Email      string
	GivenName  string
	FamilyName string
}

// CreateContactOutput contains the output of the email provider
type CreateContactOutput struct {
	ContactId string
}

// DeleteContactInput contains the input of the email provider
type DeleteContactInput struct {
	ContactId string
}

// DeleteContactOutput contains the output of the email provider
type DeleteContactOutput struct {
	Deleted bool
}

// EmailProvider defines the contract every e-mail provider (Resend, SES, Mailgun, …) must fulfil.
type EmailProvider interface {
	SendEmail(ctx context.Context, input SendEmailInput) (SendEmailOutput, error)
	CreateContactGroup(ctx context.Context, input CreateContactGroupInput) (CreateContactGroupOutput, error)
	GetContactGroup(ctx context.Context, input GetContactGroupInput) (GetContactGroupOutput, error)
	DeleteContactGroup(ctx context.Context, input DeleteContactGroupInput) (DeleteContactGroupOutput, error)
	ListContactGroups(ctx context.Context) (ListContactGroupsOutput, error)
	CreateContactGroupMembership(ctx context.Context, input CreateContactGroupMembershipInput) (CreateContactGroupMembershipOutput, error)
	DeleteContactGroupMembership(ctx context.Context, input DeleteContactGroupMembershipInput) (DeleteContactGroupMembershipOutput, error)
	CreateContact(ctx context.Context, input CreateContactInput) (CreateContactOutput, error)
	DeleteContact(ctx context.Context, input DeleteContactInput) (DeleteContactOutput, error)
}
