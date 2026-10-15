package emailprovider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/resend/resend-go/v3"
	rtime "go.miloapis.com/email-provider-resend/internal/resend"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ResendEmailProvider is an implementation of EmailProvider that delivers e-mails
// using Resend (https://resend.com/).
type ResendEmailProvider struct {
	client *resend.Client
}

// contactGroupsPageSize is how many segments each list request asks for.
const contactGroupsPageSize = 100

// NewResendEmailProvider instantiates the provider from the given API key.
func NewResendEmailProvider(apiKey string) *ResendEmailProvider {
	return newResendEmailProvider(apiKey, nil)
}

// newResendEmailProvider is NewResendEmailProvider with the API base URL
// overridable for tests. A nil baseURL keeps the SDK's default.
func newResendEmailProvider(apiKey string, baseURL *url.URL) *ResendEmailProvider {
	httpClient := &http.Client{
		Timeout:   time.Minute,
		Transport: notFoundTransport{base: http.DefaultTransport},
	}
	client := resend.NewCustomClient(httpClient, apiKey)
	if baseURL != nil {
		client.BaseURL = baseURL
	}
	return &ResendEmailProvider{client: client}
}

// SendEmail satisfies the EmailProvider interface. It returns the resend delivery id of the email.
func (r *ResendEmailProvider) SendEmail(ctx context.Context, input SendEmailInput) (SendEmailOutput, error) {
	output := SendEmailOutput{
		DeliveryID: "",
	}

	tags := make([]resend.Tag, 0, len(input.Tags))
	for _, tag := range input.Tags {
		tags = append(tags, resend.Tag{Name: tag.Name, Value: tag.Value})
	}

	resp, err := r.client.Emails.Send(&resend.SendEmailRequest{
		From:    input.From,
		ReplyTo: input.ReplyTo,
		To:      input.To,
		Cc:      input.Cc,
		Bcc:     input.Bcc,
		Subject: input.Subject,
		Html:    input.HtmlBody,
		Text:    input.TextBody,
		Tags:    tags,
		Headers: map[string]string{
			"IdempotencyKey": input.IdempotencyKey,
		},
	})
	if err != nil {
		return output, fmt.Errorf("failed to send email using resend: %w", err)
	}

	output.DeliveryID = resp.Id

	return output, nil
}

// CreateContactGroup satisfies the EmailProvider interface. It returns the resend contact group id of the contact group.
func (r *ResendEmailProvider) CreateContactGroup(ctx context.Context, input CreateContactGroupInput) (CreateContactGroupOutput, error) {
	output := CreateContactGroupOutput{
		ContactGroupID: "",
	}

	resp, err := r.client.Segments.Create(&resend.CreateSegmentRequest{
		Name: input.DisplayName,
	})
	if err != nil {
		return output, fmt.Errorf("failed to create contact group using resend: %w", err)
	}

	output.ContactGroupID = resp.Id

	return output, nil
}

// GetContactGroup satisfies the EmailProvider interface. It returns the resend contact group id of the contact group.
func (r *ResendEmailProvider) GetContactGroup(ctx context.Context, input GetContactGroupInput) (GetContactGroupOutput, error) {
	output := GetContactGroupOutput{}

	resp, err := r.client.Segments.GetWithContext(ctx, input.ContactGroupID)
	if err != nil {
		return output, TranslateResendError(err, schema.GroupResource{Group: "resend", Resource: "segments"}, input.ContactGroupID)
	}

	output.ContactGroupID = resp.Id
	output.DisplayName = resp.Name

	// Parse the timestamp returned by Resend using the resilient ResendTime helper.
	var rt rtime.ResendTime
	if err := rt.UnmarshalJSON([]byte(fmt.Sprintf("%q", resp.CreatedAt))); err != nil {
		return output, fmt.Errorf("failed to parse created at get contact group using resend: %w", err)
	}
	output.CreatedAt = rt.Time

	return output, nil
}

// DeleteContactGroup satisfies the EmailProvider interface. It returns the resend contact group id of the contact group.
func (r *ResendEmailProvider) DeleteContactGroup(ctx context.Context, input DeleteContactGroupInput) (DeleteContactGroupOutput, error) {
	output := DeleteContactGroupOutput{}

	resp, err := r.client.Segments.RemoveWithContext(ctx, input.ContactGroupID)
	if err != nil {
		return output, TranslateResendError(err, schema.GroupResource{Group: "resend", Resource: "segments"}, input.ContactGroupID)
	}

	output.Deleted = resp.Deleted
	output.ContactGroupID = input.ContactGroupID

	return output, nil
}

// ListContactGroups satisfies the EmailProvider interface. It returns every
// contact group, following Resend's pagination until the last page.
func (r *ResendEmailProvider) ListContactGroups(ctx context.Context) (ListContactGroupsOutput, error) {
	output := ListContactGroupsOutput{}

	limit := contactGroupsPageSize
	options := &resend.ListOptions{Limit: &limit}
	for {
		resp, err := r.client.Segments.ListWithOptions(ctx, options)
		if err != nil {
			return output, fmt.Errorf("failed to list contact groups using resend: %w", err)
		}

		for _, segment := range resp.Data {
			output.ContactGroups = append(output.ContactGroups, GetContactGroupOutput{
				ContactGroupID: segment.Id,
				DisplayName:    segment.Name,
			})
		}

		if !resp.HasMore || len(resp.Data) == 0 {
			return output, nil
		}
		after := resp.Data[len(resp.Data)-1].Id
		options = &resend.ListOptions{Limit: &limit, After: &after}
	}
}

// CreateContactGroupMembership satisfies the EmailProvider interface. It returns the resend contact group membership id of the contact group membership.
func (r *ResendEmailProvider) CreateContactGroupMembership(ctx context.Context, input CreateContactGroupMembershipInput) (CreateContactGroupMembershipOutput, error) {
	output := CreateContactGroupMembershipOutput{
		ContactGroupMembershipID: "",
	}

	resp, err := r.client.Contacts.Segments.Add(&resend.AddContactSegmentRequest{
		ContactId: input.ContactId,
		SegmentId: input.ContactGroupId,
	})
	if err != nil {
		return output, TranslateResendError(err, schema.GroupResource{Group: "resend", Resource: "contacts"}, input.ContactGroupId)
	}

	output.ContactGroupMembershipID = resp.Id

	return output, nil
}

// DeleteContactGroupMembership satisfies the EmailProvider interface. It returns the resend contact group membership id of the contact group membership.
// This is an idempotent operation.
func (r *ResendEmailProvider) DeleteContactGroupMembership(ctx context.Context, input DeleteContactGroupMembershipInput) (DeleteContactGroupMembershipOutput, error) {
	output := DeleteContactGroupMembershipOutput{
		ContactGroupMembershipID: "",
		Deleted:                  false,
	}

	resp, err := r.client.Contacts.Segments.RemoveWithContext(ctx, &resend.RemoveContactSegmentRequest{
		ContactId: input.ContactId,
		SegmentId: input.ContactGroupId,
	})
	if err != nil {
		return output, TranslateResendError(err, schema.GroupResource{Group: "resend", Resource: "contacts"}, input.ContactGroupId)
	}

	output.Deleted = resp.Deleted
	return output, nil
}

// CreateContact satisfies the EmailProvider interface. It returns the resend contact email of the contact.
func (r *ResendEmailProvider) CreateContact(ctx context.Context, input CreateContactInput) (CreateContactOutput, error) {
	output := CreateContactOutput{}

	resp, err := r.client.Contacts.Create(&resend.CreateContactRequest{
		Email:        input.Email,
		FirstName:    input.GivenName,
		LastName:     input.FamilyName,
		Unsubscribed: false,
	})
	if err != nil {
		return output, fmt.Errorf("failed to create contact using resend: %w", err)
	}

	output.ContactId = resp.Id

	return output, nil
}

// DeleteContact satisfies the EmailProvider interface. It returns the resend contact id of the contact.
func (r *ResendEmailProvider) DeleteContact(ctx context.Context, input DeleteContactInput) (DeleteContactOutput, error) {
	output := DeleteContactOutput{}

	resp, err := r.client.Contacts.RemoveWithContext(ctx, &resend.RemoveContactOptions{
		Id: input.ContactId,
	})
	if err != nil {
		return output, TranslateResendError(err, schema.GroupResource{Group: "resend", Resource: "contacts"}, input.ContactId)
	}

	output.Deleted = resp.Deleted
	return output, nil
}
