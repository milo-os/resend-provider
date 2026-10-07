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

	"go.miloapis.com/email-provider-resend/internal/emailprovider"
	notificationmiloapiscomv1alpha1 "go.miloapis.com/milo/pkg/apis/notification/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/finalizer"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	contactFinalizerKey       = "notification.miloapis.com/contact"
	contactNamespacedIndexKey = "contact-namespaced-index"
)

const (
	// ResendContactReadyCondition is a condition that is set to true when the contact is ready
	ResendContactReadyCondition = "ResendContactReady"
	// ResendContactCreatedReason is a reason that is set when the contact is created
	ResendContactCreatedReason = "ResendContactCreated"
	// ResendContactPendingReason is a reason that is set when the contact is pending
	ResendContactPendingReason = "ResendContactPending"
)

const (
	LoopsContactReadyCondition = "LoopsContactReady"
)

// buildContactNamespacedIndexKey returns "<contact-ns>|<contact-name>"
func buildContactNamespacedIndexKey(contactName, contactNamespace string) string {
	return fmt.Sprintf("%s|%s", contactNamespace, contactName)
}

// ContactReconciler reconciles a Contact object
type ContactController struct {
	Client        client.Client
	Finalizers    finalizer.Finalizers
	EmailProvider emailprovider.Service
}

// contactFinalizer is a finalizer for the Contact object
type contactFinalizer struct {
	Client        client.Client
	EmailProvider emailprovider.Service
}

func (f *contactFinalizer) Finalize(ctx context.Context, obj client.Object) (finalizer.Result, error) {
	log := logf.FromContext(ctx).WithValues("finalizer", "ContactFinalizer", "trigger", obj.GetName())
	log.Info("Finalizing Contact")

	// Type assertion
	contact, ok := obj.(*notificationmiloapiscomv1alpha1.Contact)
	if !ok {
		log.Error(fmt.Errorf("object is not a Contact"), "Failed to finalize Contact")
		return finalizer.Result{}, fmt.Errorf("object is not a Contact")
	}

	// Delete Contact on email provider
	deleted, err := f.EmailProvider.DeleteContact(ctx, *contact)
	if err != nil && !errors.IsNotFound(err) {
		log.Error(err, "Failed to delete Contact on email provider")
		return finalizer.Result{}, fmt.Errorf("failed to delete Contact on email provider: %w", err)
	}
	// The delete call is idempotent: a nil error with Deleted=false means the
	// contact was already absent on the provider, which is the desired final
	// state, so the finalizer can complete.
	if err == nil && !deleted.Deleted {
		log.Info("Contact already deleted on email provider. Contact finalizer completed.")
	}

	// Get associated ContactGroupMemberships to contact name
	contactGroupMemberships := &notificationmiloapiscomv1alpha1.ContactGroupMembershipList{}
	err = f.Client.List(ctx, contactGroupMemberships, client.MatchingFields{contactNamespacedIndexKey: buildContactNamespacedIndexKey(contact.Name, contact.Namespace)})
	if err != nil {
		log.Error(err, "Failed to list ContactGroupMemberships")
		return finalizer.Result{}, fmt.Errorf("failed to list ContactGroupMemberships: %w", err)
	}

	// Delete associated ContactGroupMemberships
	for _, cgm := range contactGroupMemberships.Items {
		if err := f.Client.Delete(ctx, &cgm); err != nil {
			if errors.IsNotFound(err) {
				log.Info("ContactGroupMembership not found. Probably deleted.")
				continue
			}
			log.Error(err, "Failed to delete ContactGroupMembership")
			return finalizer.Result{}, fmt.Errorf("failed to delete ContactGroupMembership: %w", err)
		}
	}

	// Get associated ContactGroupMemberships to contact name
	// GroupMembership needs to be fully removed before deleting the contact
	notDeletedContactGroupMemberships := &notificationmiloapiscomv1alpha1.ContactGroupMembershipList{}
	err = f.Client.List(ctx, notDeletedContactGroupMemberships, client.MatchingFields{contactNamespacedIndexKey: buildContactNamespacedIndexKey(contact.Name, contact.Namespace)})
	if err != nil {
		log.Error(err, "Failed to list ContactGroupMemberships")
		return finalizer.Result{}, fmt.Errorf("failed to list ContactGroupMemberships: %w", err)
	}
	if len(notDeletedContactGroupMemberships.Items) > 0 {
		log.Info("Waiting for ContactGroupMembership deletions", "count", len(notDeletedContactGroupMemberships.Items))
		return finalizer.Result{}, fmt.Errorf("waiting for %d ContactGroupMembership deletions", len(notDeletedContactGroupMemberships.Items))
	}

	// Get ContactGroupMembershipRemoval that references this Contact
	contactGroupMembershipRemovals := &notificationmiloapiscomv1alpha1.ContactGroupMembershipRemovalList{}
	err = f.Client.List(ctx, contactGroupMembershipRemovals, client.MatchingFields{contactNamespacedIndexKey: buildContactNamespacedIndexKey(contact.Name, contact.Namespace)})
	if err != nil {
		log.Error(err, "Failed to list ContactGroupMembershipRemoval")
		return finalizer.Result{}, fmt.Errorf("failed to list ContactGroupMembershipRemoval: %w", err)
	}
	// Delete ContactGroupMembershipRemoval that references this contact
	for _, cgmr := range contactGroupMembershipRemovals.Items {
		if err := f.Client.Delete(ctx, &cgmr); err != nil {
			if errors.IsNotFound(err) {
				log.Info("ContactGroupMembershipRemoval not found. Probably deleted.")
				continue
			}
			log.Error(err, "Failed to delete ContactGroupMembershipRemoval")
			return finalizer.Result{}, fmt.Errorf("failed to delete ContactGroupMembershipRemoval: %w", err)
		}
	}

	log.Info("Contac finalizer completed")

	return finalizer.Result{}, nil
}

// +kubebuilder:rbac:groups=notification.miloapis.com,resources=contacts,verbs=get;list;watch
// +kubebuilder:rbac:groups=notification.miloapis.com,resources=contacts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=notification.miloapis.com,resources=contacts/finalizers,verbs=update
// +kubebuilder:rbac:groups=notification.miloapis.com,resources=contactgroupmemberships,verbs=get;list;watch;delete

// Reconcile is the main function that reconciles the Contact object.
func (r *ContactController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("controller", "ContactController", "trigger", req.NamespacedName)
	log.Info("Starting reconciliation", "namespacedName", req.String(), "name", req.Name, "namespace", req.Namespace)

	// Get Contact
	contact := &notificationmiloapiscomv1alpha1.Contact{}
	err := r.Client.Get(ctx, req.NamespacedName, contact)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("Contact not found. Probably deleted.")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get contact: %w", err)
	}

	// Run finalizers
	finalizeResult, err := r.Finalizers.Finalize(ctx, contact)
	if err != nil {
		log.Error(err, "Failed to run finalizers for Contact")
		return ctrl.Result{}, fmt.Errorf("failed to run finalizers for Contact: %w", err)
	}
	if finalizeResult.Updated {
		log.Info("finalizer updated the contact object, updating API server")
		if updateErr := r.Client.Update(ctx, contact); updateErr != nil {
			if errors.IsConflict(updateErr) {
				log.Info("Conflict updating Loopos Contact after finalizer update; requeuing")
				return ctrl.Result{Requeue: true}, nil
			}
			log.Error(updateErr, "Failed to update Contact after finalizer update")
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{}, nil
	}

	oldStatus := contact.Status.DeepCopy()
	original := contact.DeepCopy()
	resendReadyCond := meta.FindStatusCondition(contact.Status.Conditions, ResendContactReadyCondition)
	updatedCond := meta.FindStatusCondition(contact.Status.Conditions, notificationmiloapiscomv1alpha1.ContactUpdatedCondition)

	switch {
	// First creation – condition not present yet
	case resendReadyCond == nil:
		// Create Contact on email provider
		emailProviderContact, err := r.EmailProvider.CreateContactIdempotent(ctx, *contact)
		if err != nil {
			log.Error(err, "Failed to create Contact on email provider")
			return ctrl.Result{}, fmt.Errorf("failed to create Contact on email provider: %w", err)
		}
		contact.Status.ProviderID = emailProviderContact.ContactId

		log.Info("Contact first creation")
		meta.SetStatusCondition(&contact.Status.Conditions, metav1.Condition{
			Type:               ResendContactReadyCondition,
			Status:             metav1.ConditionTrue,
			Reason:             ResendContactPendingReason,
			Message:            "Contact created on email provider. Waiting for webhook confirmation.",
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: contact.GetGeneration(),
		})

		// Set ContactUpdatedCondition to true to indicate that the contact is ready to accept incoming updates
		meta.SetStatusCondition(&contact.Status.Conditions, metav1.Condition{
			Type:               notificationmiloapiscomv1alpha1.ContactUpdatedCondition,
			Status:             metav1.ConditionTrue,
			Reason:             notificationmiloapiscomv1alpha1.ContactCreatedReason,
			Message:            "Contact created. Contact is ready to accept incoming updates",
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: contact.GetGeneration(),
		})

	// Update – generation changed since we last processed the object
	case updatedCond != nil && updatedCond.ObservedGeneration != contact.GetGeneration():
		log.Info("Contact updated")

		// Update Contact on email provider
		emailProviderContact, err := r.EmailProvider.UpdateContactIdempotent(ctx, *contact)
		if err != nil {
			log.Error(err, "Failed to update Contact on email provider")
			return ctrl.Result{}, fmt.Errorf("failed to update Contact on email provider: %w", err)
		}
		contact.Status.ProviderID = emailProviderContact.ContactId

		// Update condition
		meta.SetStatusCondition(&contact.Status.Conditions, metav1.Condition{
			Type:               notificationmiloapiscomv1alpha1.ContactUpdatedCondition,
			Status:             metav1.ConditionFalse,
			Reason:             notificationmiloapiscomv1alpha1.ContactUpdatePendingReason,
			Message:            "Contact updated. Waiting for email provider webhook confirmation",
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: contact.GetGeneration(),
		})

		// Get associated ContactGroupMemberships to contact
		contactGroupMemberships := &notificationmiloapiscomv1alpha1.ContactGroupMembershipList{}
		err = r.Client.List(ctx, contactGroupMemberships, client.MatchingFields{contactNamespacedIndexKey: buildContactNamespacedIndexKey(contact.Name, contact.Namespace)})
		if err != nil {
			log.Error(err, "Failed to list ContactGroupMemberships")
			return ctrl.Result{}, fmt.Errorf("failed to list ContactGroupMemberships: %w", err)
		}

		// Update ContactGroupMembership conditions
		for _, cgm := range contactGroupMemberships.Items {
			// Update ContactGroupMembership condition
			// ContactGroupMembershipUpdateRequestedReason set so the ContactGroupMembershipController will update the ContactGroupMembership
			meta.SetStatusCondition(&cgm.Status.Conditions, metav1.Condition{
				Type:    notificationmiloapiscomv1alpha1.ContactGroupMembershipUpdatedCondition,
				Status:  metav1.ConditionFalse,
				Reason:  notificationmiloapiscomv1alpha1.ContactGroupMembershipUpdateRequestedReason,
				Message: "ContactGroupMembership update requested from contact update",
			})

			// Update ContactGroupMembership status
			if err := r.Client.Status().Update(ctx, &cgm); err != nil {
				log.Error(err, "Failed to update ContactGroupMembership status")
				return ctrl.Result{}, fmt.Errorf("failed to update ContactGroupMembership status: %w", err)
			}
		}
	}

	r.verifyContactReadyCondition(contact)

	// Update contact status if it changed
	if !equality.Semantic.DeepEqual(oldStatus, &contact.Status) {
		if err := r.Client.Status().Patch(ctx, contact, client.MergeFrom(original), client.FieldOwner("contact-controller")); err != nil {
			if errors.IsConflict(err) {
				log.Info("Conflict patching contact status; requeuing")
				return ctrl.Result{Requeue: true}, nil
			}
			log.Error(err, "Failed to patch contact status")
			return ctrl.Result{}, fmt.Errorf("failed to patch contact status: %w", err)
		}
	} else {
		log.Info("Contact status unchanged, skipping update")
	}

	log.Info("Contact reconciled")

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ContactController) SetupWithManager(mgr ctrl.Manager) error {
	// Index by contact group membership for efficient contact group membership lookup
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &notificationmiloapiscomv1alpha1.ContactGroupMembership{}, contactNamespacedIndexKey, func(rawObj client.Object) []string {
		cgm := rawObj.(*notificationmiloapiscomv1alpha1.ContactGroupMembership)
		return []string{buildContactNamespacedIndexKey(cgm.Spec.ContactRef.Name, cgm.Spec.ContactRef.Namespace)}
	}); err != nil {
		return fmt.Errorf("failed to index contactgroupmembership by contact name: %w", err)
	}

	// Index by contact group membership removal for efficient contact group membership removal lookup
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &notificationmiloapiscomv1alpha1.ContactGroupMembershipRemoval{}, contactNamespacedIndexKey, func(rawObj client.Object) []string {
		cgmr := rawObj.(*notificationmiloapiscomv1alpha1.ContactGroupMembershipRemoval)
		return []string{buildContactNamespacedIndexKey(cgmr.Spec.ContactRef.Name, cgmr.Spec.ContactRef.Namespace)}
	}); err != nil {
		return fmt.Errorf("failed to index contactgroupmembership by contact name: %w", err)
	}

	// Register finalizer
	r.Finalizers = finalizer.NewFinalizers()
	if err := r.Finalizers.Register(contactFinalizerKey, &contactFinalizer{
		Client:        r.Client,
		EmailProvider: r.EmailProvider,
	}); err != nil {
		return fmt.Errorf("failed to register contact finalizer: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&notificationmiloapiscomv1alpha1.Contact{}).
		Named("contact").
		Complete(r)
}

// verifyContactReadyCondition is a function that verifies the ContactReadyCondition
func (r *ContactController) verifyContactReadyCondition(contact *notificationmiloapiscomv1alpha1.Contact) {
	// Update contact status if it changed
	// Aggregate readiness across providers: Loops and Resend
	loopsCond := meta.FindStatusCondition(contact.Status.Conditions, LoopsContactReadyCondition)
	resendCond := meta.FindStatusCondition(contact.Status.Conditions, ResendContactReadyCondition)
	allReady := loopsCond != nil && loopsCond.Status == metav1.ConditionTrue &&
		resendCond != nil && resendCond.Status == metav1.ConditionTrue

	if allReady {
		meta.SetStatusCondition(&contact.Status.Conditions, metav1.Condition{
			Type:               notificationmiloapiscomv1alpha1.ContactReadyCondition,
			Status:             metav1.ConditionTrue,
			Reason:             "AllProvidersReady",
			Message:            "Loops and Resend contacts are ready",
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: contact.GetGeneration(),
		})
	} else {
		// Build informative not-ready message
		notReadyMsg := ""
		if loopsCond == nil {
			notReadyMsg += "Loops: condition missing; "
		} else if loopsCond.Status != metav1.ConditionTrue {
			notReadyMsg += fmt.Sprintf("Loops: %s (%s); ", loopsCond.Reason, loopsCond.Message)
		}
		if resendCond == nil {
			notReadyMsg += "Resend: condition missing; "
		} else if resendCond.Status != metav1.ConditionTrue {
			notReadyMsg += fmt.Sprintf("Resend: %s (%s); ", resendCond.Reason, resendCond.Message)
		}
		meta.SetStatusCondition(&contact.Status.Conditions, metav1.Condition{
			Type:               notificationmiloapiscomv1alpha1.ContactReadyCondition,
			Status:             metav1.ConditionFalse,
			Reason:             "ProvidersNotReady",
			Message:            notReadyMsg,
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: contact.GetGeneration(),
		})
	}
}
