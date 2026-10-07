package emailprovider

import (
	"context"
	"fmt"

	emailtemplating "go.miloapis.com/email-provider-resend/internal/emailtemplanting"
	"go.miloapis.com/email-provider-resend/internal/resend"
	notificationmiloapiscomv1alpha1 "go.miloapis.com/milo/pkg/apis/notification/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Service ties rendering logic with an underlying EmailProvider.
type Service struct {
	provider EmailProvider // Actual email provider
	from     string        // Default From address
	replyTo  string        // Default Reply-To address
}

// NewService creates a new Service.
func NewService(provider EmailProvider, from, replyTo string) *Service {
	return &Service{
		provider: provider,
		from:     from,
		replyTo:  replyTo,
	}
}

// SendEmailRenderedOutput is the output of the SendEmail function.
// It contains the delivery ID, the HTML body, the text body, the subject and the recipient email address.
type SendEmailRenderedOutput struct {
	DeliveryID            string `json:"deliveryID"`
	HTMLBody              string `json:"htmlBody"`
	TextBody              string `json:"textBody,omitempty"`
	Subject               string `json:"subject,omitempty"`
	RecipientEmailAddress string `json:"recipientEmailAddress,omitempty"`
}

// Send takes the CRD objects coming from Milo, renders the templates and finally
// hands the result to the configured Provider.
func (s *Service) Send(ctx context.Context,
	email *notificationmiloapiscomv1alpha1.Email,
	template *notificationmiloapiscomv1alpha1.EmailTemplate,
	recipientEmailAddress string,
) (SendEmailRenderedOutput, error) {
	// variables are already validated by Milo webhooks
	// to match the referenced template
	vars := email.Spec.Variables

	output := SendEmailRenderedOutput{
		RecipientEmailAddress: recipientEmailAddress,
	}

	htmlBody, err := emailtemplating.RenderHTMLBodyTemplate(vars, template)
	if err != nil {
		return output, fmt.Errorf("render HTML body: %w", err)
	}
	output.HTMLBody = htmlBody

	textBody, err := emailtemplating.RenderTextBodyTemplate(vars, template)
	if err != nil {
		return output, fmt.Errorf("render text body: %w", err)
	}
	output.TextBody = textBody

	subject, err := emailtemplating.RenderSubjectTemplate(vars, template)
	if err != nil {
		return output, fmt.Errorf("render subject: %w", err)
	}
	output.Subject = subject

	providerOutput, err := s.provider.SendEmail(ctx, SendEmailInput{
		From:           s.from,
		ReplyTo:        s.replyTo,
		To:             []string{recipientEmailAddress},
		Cc:             email.Spec.CC,
		Bcc:            email.Spec.BCC,
		Subject:        subject,
		HtmlBody:       htmlBody,
		TextBody:       textBody,
		IdempotencyKey: string(email.UID),
		Tags:           resend.EmailRefTags(email.Namespace, email.Name),
	})
	if err != nil {
		return output, fmt.Errorf("error sending email: %w", err)
	}
	output.DeliveryID = providerOutput.DeliveryID

	return output, nil
}

// CreateContactGroup creates a contact group on the email provider.
func (s *Service) CreateContactGroup(ctx context.Context, cg notificationmiloapiscomv1alpha1.ContactGroup) (CreateContactGroupOutput, error) {
	displayName := GetDeterministicContactGroupDisplayName(&cg)
	return s.provider.CreateContactGroup(ctx, CreateContactGroupInput{
		DisplayName: displayName,
	})

}

// GetContactGroup returns the email provider contact group id of the contact group.
func (s *Service) GetContactGroup(ctx context.Context, cg notificationmiloapiscomv1alpha1.ContactGroup) (GetContactGroupOutput, error) {
	return s.provider.GetContactGroup(ctx, GetContactGroupInput{
		ContactGroupID: cg.Status.ProviderID,
	})
}

// DeleteContactGroup deletes a contact group on the email provider.
func (s *Service) DeleteContactGroup(ctx context.Context, cg notificationmiloapiscomv1alpha1.ContactGroup) (DeleteContactGroupOutput, error) {
	return s.provider.DeleteContactGroup(ctx, DeleteContactGroupInput{
		ContactGroupID: cg.Status.ProviderID,
	})
}

// GetContactGroupByDisplayName returns the email provider contact group by display name.
func (s *Service) GetContactGroupByDisplayName(ctx context.Context, displayName string) (GetContactGroupOutput, error) {
	contactGroups, err := s.provider.ListContactGroups(ctx)
	if err != nil {
		return GetContactGroupOutput{}, err
	}
	for _, contactGroup := range contactGroups.ContactGroups {
		if contactGroup.DisplayName == displayName {
			return contactGroup, nil
		}
	}
	return GetContactGroupOutput{}, errors.NewNotFound(schema.GroupResource{Group: "resend", Resource: "audiences"}, displayName)
}

// DeleteContactGroupMembership deletes a contact group membership on the email provider.
// This is an idempotent operation.
func (s *Service) DeleteContactGroupMembershipIdempotent(
	ctx context.Context,
	contactGroup notificationmiloapiscomv1alpha1.ContactGroup,
	contact notificationmiloapiscomv1alpha1.Contact) (DeleteContactGroupMembershipOutput, error) {
	return s.provider.DeleteContactGroupMembership(ctx, DeleteContactGroupMembershipInput{
		ContactId:      contact.Status.ProviderID,
		ContactGroupId: contactGroup.Status.ProviderID,
	})
}

// CreateContactGroupMembershipIdempotent creates a contact group membership on the email provider.
// This is an idempotent operation.
func (s *Service) CreateContactGroupMembershipIdempotent(ctx context.Context, contactGroup notificationmiloapiscomv1alpha1.ContactGroup, contact notificationmiloapiscomv1alpha1.Contact) (CreateContactGroupMembershipOutput, error) {
	return s.provider.CreateContactGroupMembership(ctx, CreateContactGroupMembershipInput{
		ContactGroupId: contactGroup.Status.ProviderID,
		ContactId:      contact.Status.ProviderID,
	})
}

// CreateContact creates a contact on the email provider.
// This is an idempotent operation.
func (s *Service) CreateContactIdempotent(ctx context.Context, contact notificationmiloapiscomv1alpha1.Contact) (CreateContactOutput, error) {
	return s.provider.CreateContact(ctx, CreateContactInput{
		Email:      contact.Spec.Email,
		GivenName:  contact.Spec.GivenName,
		FamilyName: contact.Spec.FamilyName,
	})
}

// DeleteContact deletes a contact on the email provider.
func (s *Service) DeleteContact(ctx context.Context, contact notificationmiloapiscomv1alpha1.Contact) (DeleteContactOutput, error) {
	return s.provider.DeleteContact(ctx, DeleteContactInput{
		ContactId: contact.Status.ProviderID,
	})
}

// UpdateContactIdempotent updates a contact on the email provider.
// It deletes the existing contact and creates a new one, as Resend does not support updating the email address.
// This is an idempotent operation.
func (s *Service) UpdateContactIdempotent(ctx context.Context, contact notificationmiloapiscomv1alpha1.Contact) (CreateContactOutput, error) {
	deleted, err := s.DeleteContact(ctx, contact)
	if err != nil {
		if !errors.IsNotFound(err) {
			return CreateContactOutput{}, fmt.Errorf("failed to delete contact during update: %w", err)
		}
		return s.CreateContactIdempotent(ctx, contact)
	}

	if !deleted.Deleted {
		return CreateContactOutput{}, fmt.Errorf("failed to delete contact during update")
	}
	return s.CreateContactIdempotent(ctx, contact)
}
