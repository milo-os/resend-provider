/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"

	iammiloapiscomv1alpha1 "go.miloapis.com/milo/pkg/apis/iam/v1alpha1"
	notificationmiloapiscomv1alpha1 "go.miloapis.com/milo/pkg/apis/notification/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"go.miloapis.com/email-provider-resend/internal/config"
	"go.miloapis.com/email-provider-resend/internal/emailprovider"
)

// EmailRecipientUserNotFoundReason is a terminal reason set on the Email's
// Delivered condition when the referenced recipient User no longer exists.
// Unlike the generic delivery-failure reason, this condition is NOT retried.
const EmailRecipientUserNotFoundReason = "EmailRecipientUserNotFound"

// EmailReconciler reconciles a Email object
type EmailController struct {
	Client client.Client
	// APIReader reads Emails directly from the API server. Emails carry their
	// rendered bodies, so the controller watches only their metadata and must
	// not read them through the cache-backed Client, which would start a
	// full-object informer.
	APIReader     client.Reader
	EmailProvider emailprovider.Service
	Config        config.EmailControllerConfig
}

// +kubebuilder:rbac:groups=notification.miloapis.com,resources=emails,verbs=get;list;watch
// +kubebuilder:rbac:groups=notification.miloapis.com,resources=emails/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=notification.miloapis.com,resources=emailtemplates,verbs=get
// +kubebuilder:rbac:groups=iam.miloapis.com,resources=users,verbs=get

// Reconcile is the main function that reconciles the Email object.
func (r *EmailController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithName("email-reconciler")

	log.Info("Starting reconciliation", "namespacedName", req.String(), "name", req.Name, "namespace", req.Namespace)

	// Get Email with retry logic to handle potential eventual consistency issues
	email := &notificationmiloapiscomv1alpha1.Email{}
	err := r.APIReader.Get(ctx, req.NamespacedName, email)
	if errors.IsNotFound(err) {
		log.Info("Email not found. Probably deleted.", "namespacedName", req.String(), "name", req.Name, "namespace", req.Namespace)
		return ctrl.Result{}, nil
	} else if err != nil {
		log.Error(err, "Failed to get Email", "namespacedName", req.String(), "name", req.Name, "namespace", req.Namespace)
		return ctrl.Result{}, fmt.Errorf("failed to get Email: %w", err)
	}
	base := email.DeepCopy()

	// Skip sent Emails before looking up their template and recipient: on
	// startup every stored Email is reconciled, and a sent Email must not fail
	// because its template or User has since been deleted.
	if isEmailAlreadySent(email) {
		log.Info("Email was already sent. Probably reconciling because of webhook update.")
		return ctrl.Result{}, nil
	}

	log.Info("Reconciling Email", "email", email.Name, "template", email.Spec.TemplateRef.Name, "recipient", email.Spec.Recipient)

	// Terminal failure: the recipient User no longer exists, the email can
	// never be delivered, and retrying would loop forever. Skip all work.
	if cond := meta.FindStatusCondition(email.Status.Conditions, notificationmiloapiscomv1alpha1.EmailDeliveredCondition); cond != nil && cond.Reason == EmailRecipientUserNotFoundReason {
		log.Info("Email is in terminal failed state, recipient user not found. Skipping reconciliation.", "email", email.Name)
		return ctrl.Result{}, nil
	}

	// Get EmailTemplate
	emailTemplate := &notificationmiloapiscomv1alpha1.EmailTemplate{} // Cluster scoped resource
	err = r.Client.Get(ctx, client.ObjectKey{Name: email.Spec.TemplateRef.Name}, emailTemplate)
	if err != nil {
		// emailTemplate is warranty to exist. As it is checked on a webhook on Milo.
		log.Error(err, "Failed to get EmailTemplate", "email", email.Spec.TemplateRef.Name)
		return ctrl.Result{}, fmt.Errorf("failed to get EmailTemplate: %w", err)
	}

	// Get EmailRecipient
	recipientEmailAddress, err := r.getRecipientEmailAddress(ctx, email.DeepCopy())
	if err != nil {
		// If the recipient references a User that no longer exists, the email
		// can never be delivered. Record a terminal failure instead of
		// retrying forever.
		if errors.IsNotFound(err) {
			log.Info("Recipient user not found. Marking email as failed.", "email", email.Name, "userRef", email.Spec.Recipient.UserRef.Name)
			if statusErr := r.updateEmailStatus(ctx, email, metav1.Condition{
				Type:               notificationmiloapiscomv1alpha1.EmailDeliveredCondition,
				Status:             metav1.ConditionFalse,
				Reason:             EmailRecipientUserNotFoundReason,
				Message:            fmt.Sprintf("Recipient user %q no longer exists; email cannot be delivered", email.Spec.Recipient.UserRef.Name),
				LastTransitionTime: metav1.Now(),
			}); statusErr != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update Email status: %w", statusErr)
			}
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get recipient email address", "email", email.Name)
		return ctrl.Result{}, fmt.Errorf("failed to get recipient email address: %w", err)
	}

	log.Info("Sending email")

	// Send email
	output, err := r.EmailProvider.Send(ctx, email.DeepCopy(), emailTemplate.DeepCopy(), recipientEmailAddress)
	if err != nil {
		log.Error(err, "Failed to send email", "email", email.Name)
		if err := r.updateEmailStatus(ctx, base, email, metav1.Condition{
			Type:               notificationmiloapiscomv1alpha1.EmailDeliveredCondition,
			Status:             metav1.ConditionFalse,
			Reason:             notificationmiloapiscomv1alpha1.EmailDeliveryFailedReason,
			Message:            fmt.Sprintf("Email delivery failed: %s", err.Error()),
			LastTransitionTime: metav1.Now(),
		}); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update Email status: %w", err)
		}
		return ctrl.Result{RequeueAfter: r.Config.GetWaitTimeBeforeRetry(email.Spec.Priority)}, nil
	}
	log.Info("Email sent", "email", email.Name, "deliveryID", output.DeliveryID)

	// Record provider ID and mark delivery as pending (Status=Unknown).
	// We set this ONLY on the first successful send so that future webhook
	// updates (Delivered / Failed) are not overwritten by subsequent
	// reconciliations.
	email.Status.ProviderID = output.DeliveryID
	email.Status.HTMLBody = output.HTMLBody
	email.Status.TextBody = output.TextBody
	email.Status.Subject = output.Subject
	email.Status.EmailAddress = output.RecipientEmailAddress

	// EmailProvider.Send (resend implementation) uses an idempotency mechanism using the Email.Name as idempotency key.
	// In case of a failure updating the status, the email won't be sent again, and the return value from EmailProvider.Send
	// will be the same one as the original one. The idempotency only lasts for 24 hours.
	if err := r.updateEmailStatus(ctx, base, email, metav1.Condition{
		Type:               notificationmiloapiscomv1alpha1.EmailDeliveredCondition,
		Status:             metav1.ConditionUnknown,
		Reason:             notificationmiloapiscomv1alpha1.EmailDeliveryPendingReason,
		Message:            fmt.Sprintf("Email accepted for delivery. Provider ID: %s", output.DeliveryID),
		LastTransitionTime: metav1.Now(),
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update Email status: %w", err)
	}

	log.Info("Email reconciled")
	return ctrl.Result{}, nil
}

// isEmailAlreadyDelivered checks if the email has already been successfully sent
func isEmailAlreadySent(email *notificationmiloapiscomv1alpha1.Email) bool {
	// If we already have a ProviderID, we know the email was at leastaccepted by email provider.
	if email.Status.ProviderID != "" {
		return true
	}

	// If the Delivered condition is already True we also skip.
	if meta.IsStatusConditionTrue(email.Status.Conditions, notificationmiloapiscomv1alpha1.EmailDeliveredCondition) {
		return true
	}

	return false
}

// updateEmailStatus sets the given condition on the email and patches the
// status fields that changed since base. The patch fails on a conflict, as an
// update would.
func (r *EmailController) updateEmailStatus(
	ctx context.Context,
	base, email *notificationmiloapiscomv1alpha1.Email,
	condition metav1.Condition,
) error {
	log := logf.FromContext(ctx).WithName("email-reconciler")

	meta.SetStatusCondition(&email.Status.Conditions, condition)

	patch := client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
	if err := r.Client.Status().Patch(ctx, email, patch); err != nil {
		log.Error(err, "failed to update Email status", "email", email.Name)
		return fmt.Errorf("failed to update Email status: %w", err)
	}
	log.Info("Email status updated")

	return nil
}

// SetupWithManager sets up the controller with the Manager.
//
// Emails are watched as metadata only, so the cache does not hold their
// rendered bodies and a resync lists only metadata. Reconcile reads each Email
// from the API server instead. A restart therefore enqueues every stored
// Email; the priority queue processes those initial-list items after Emails
// created or changed since, so new mail is not stuck behind the backlog.
func (r *EmailController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&notificationmiloapiscomv1alpha1.Email{}, builder.OnlyMetadata).
		WithOptions(controller.Options{UsePriorityQueue: ptr.To(true)}).
		Named("email").
		Complete(r)
}

func (r *EmailController) getRecipientEmailAddress(ctx context.Context, email *notificationmiloapiscomv1alpha1.Email) (string, error) {
	log := logf.FromContext(ctx).WithName("email-reconciler")

	if r.hasUserRef(email) {
		log.Info("Getting recipient email address from user reference")
		emailRecipient := &iammiloapiscomv1alpha1.User{} // Cluster scoped resource
		if err := r.Client.Get(ctx, client.ObjectKey{Name: email.Spec.Recipient.UserRef.Name}, emailRecipient); err != nil {
			// emailRecipient is warranty to exist. As it is checked on a webhook on Milo.
			log.Error(err, "Failed to get EmailRecipient", "email", email.Spec.Recipient.UserRef.Name)
			return "", fmt.Errorf("failed to get EmailRecipient: %w", err)
		}
		return emailRecipient.Spec.Email, nil
	}

	if r.hasEmailAddress(email) {
		log.Info("Getting recipient email address from email address")
		return email.Spec.Recipient.EmailAddress, nil
	}

	return "", fmt.Errorf("no recipient found")
}

// hasUserRef checks if the Email has a user reference as recipient
func (r *EmailController) hasUserRef(email *notificationmiloapiscomv1alpha1.Email) bool {
	return email.Spec.Recipient.UserRef.Name != ""
}

// hasEmailAddress checks if the Email has an email address as recipient
func (r *EmailController) hasEmailAddress(email *notificationmiloapiscomv1alpha1.Email) bool {
	return email.Spec.Recipient.EmailAddress != ""
}
