package webhook

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"go.miloapis.com/email-provider-resend/internal/resend"
	notificationmiloapiscomv1alpha1 "go.miloapis.com/milo/pkg/apis/notification/v1alpha1"
)

// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create
// +kubebuilder:rbac:groups=notification.miloapis.com,resources=emails,verbs=get;list
// +kubebuilder:rbac:groups=notification.miloapis.com,resources=emails/status,verbs=patch

// emailScanPageSize bounds how many Emails are held in memory at once while
// searching for an untagged event's Email.
const emailScanPageSize = 50

// errEmailNotFound reports that no Email matches a webhook event.
var errEmailNotFound = errors.New("email not found")

// NewResendEmailWebhookV1 returns the webhook that records Resend delivery
// events on Emails.
//
// Emails are read through reader, which must not be cache-backed: Emails carry
// their rendered bodies, and caching every Email made the webhook's memory grow
// with the number of stored Emails. Writes go through k8sClient.
//
// senderAddress is the bare address Emails are sent from. Untagged events from
// any other sender are acknowledged without searching for their Email, since
// the Resend account may also send mail that has no Email resource. When
// senderAddress is empty, every untagged event is searched.
func NewResendEmailWebhookV1(k8sClient client.Client, reader client.Reader, senderAddress string) *Webhook {
	return &Webhook{
		Handler: HandlerFunc(func(ctx context.Context, req Request) Response {
			emailEvent := req.EmailEvent
			log := logf.FromContext(ctx).WithName("resend-webhook")
			log.Info("Received event", "event", emailEvent.Envelope.Type)

			if _, tagged := emailEvent.Base.EmailRef(); !tagged && !isFromSender(emailEvent.Base.From, senderAddress) {
				log.Info("Ignoring untagged event from another sender", "event", emailEvent.Envelope.Type)
				return OkResponse()
			}

			email, err := findEmail(ctx, reader, emailEvent.Base)
			if errors.Is(err, errEmailNotFound) {
				log.Info("No email found with providerID", "providerID", emailEvent.Base.EmailID, "reason", err.Error())
				return NotFoundResponse()
			}
			if err != nil {
				log.Error(err, "Failed to find email by providerID", "providerID", emailEvent.Base.EmailID)
				return InternalServerErrorResponse()
			}
			base := email.DeepCopy()

			emailCondition := resend.GetEmailCondition(emailEvent.Envelope.Type)

			// Update email status
			condition := metav1.Condition{
				Type:               notificationmiloapiscomv1alpha1.EmailDeliveredCondition,
				Status:             emailCondition.Status,
				Reason:             emailCondition.EmailDeliveredReason,
				Message:            fmt.Sprintf("Updated Email status from webhook event: %s", emailEvent.Envelope.Type),
				LastTransitionTime: metav1.Now(),
			}
			meta.SetStatusCondition(&email.Status.Conditions, condition)
			patch := client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
			if err := k8sClient.Status().Patch(ctx, email, patch); err != nil {
				log.Error(err, "Failed to update Email status", "email", email.Name)
				return InternalServerErrorResponse()
			}

			// Emit Kubernetes Event for observability
			webhookEvent := &eventsv1.Event{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: fmt.Sprintf("%s-", email.Name),
					Namespace:    email.Namespace,
				},
				Action:              "Update",
				Reason:              condition.Reason,
				Note:                condition.Message,
				Type:                emailCondition.CoreEventType,
				EventTime:           metav1.MicroTime{Time: time.Now()},
				ReportingController: "email-provider-resend-webhook",
				ReportingInstance:   "email-provider-resend-webhook-1",
				Regarding: corev1.ObjectReference{
					Kind:            "Email",
					Namespace:       email.Namespace,
					Name:            email.Name,
					UID:             email.UID,
					ResourceVersion: email.ResourceVersion,
					APIVersion:      notificationmiloapiscomv1alpha1.SchemeGroupVersion.String(),
				},
			}
			if err := k8sClient.Create(ctx, webhookEvent); err != nil {
				log.Error(err, "Failed to create Event", "email", email.Name)
				return InternalServerErrorResponse()
			}

			log.Info("Updated Email status from webhook", "email", email.Name, "condition", condition)

			return OkResponse()
		}),
		Endpoint: "/apis/emailnotification.k8s.io/v1/resend/emails",
	}
}

// findEmail returns the Email that a webhook event belongs to.
//
// Emails sent by this provider are tagged with their namespace and name, so
// the Email is fetched directly. The Email's provider ID must match the
// event's, otherwise the event is refused rather than recorded on the wrong
// Email. Events without those tags, such as those for Emails sent before
// tagging was introduced, fall back to a paginated search by provider ID.
func findEmail(
	ctx context.Context, reader client.Reader, event resend.EmailBase,
) (*notificationmiloapiscomv1alpha1.Email, error) {
	if event.EmailID == "" {
		return nil, fmt.Errorf("%w: event has no email_id", errEmailNotFound)
	}

	if ref, ok := event.EmailRef(); ok {
		email := &notificationmiloapiscomv1alpha1.Email{}
		if err := reader.Get(ctx, ref, email); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("%w: tagged email %s does not exist", errEmailNotFound, ref)
			}
			return nil, fmt.Errorf("failed to get email %s: %w", ref, err)
		}
		if email.Status.ProviderID != event.EmailID {
			return nil, fmt.Errorf("%w: tagged email %s has providerID %q", errEmailNotFound, ref, email.Status.ProviderID)
		}
		return email, nil
	}

	emails := &notificationmiloapiscomv1alpha1.EmailList{}
	opts := []client.ListOption{client.Limit(emailScanPageSize)}
	for {
		if err := reader.List(ctx, emails, opts...); err != nil {
			return nil, fmt.Errorf("failed to list emails: %w", err)
		}
		for i := range emails.Items {
			if emails.Items[i].Status.ProviderID == event.EmailID {
				return &emails.Items[i], nil
			}
		}
		if emails.Continue == "" {
			return nil, fmt.Errorf("%w: untagged event matches no email", errEmailNotFound)
		}
		opts = []client.ListOption{client.Limit(emailScanPageSize), client.Continue(emails.Continue)}
	}
}

// isFromSender reports whether from, which may include a display name, is
// senderAddress. An empty senderAddress matches every sender.
func isFromSender(from, senderAddress string) bool {
	if senderAddress == "" {
		return true
	}
	addr, err := mail.ParseAddress(from)
	if err != nil {
		return false
	}
	return strings.EqualFold(addr.Address, senderAddress)
}
