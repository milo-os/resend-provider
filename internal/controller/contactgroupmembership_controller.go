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
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/finalizer"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	contactGroupMembershipFinalizerKey = "notification.miloapis.com/contactgroupmembership"
	cgmByContactNamespacedNameIndexKey = "cgm-by-contact-namespaced-name-index"
)

const (
	ResendContactGroupMembershipReadyCondition = "ResendContactGroupMembershipReady"
	LoopsContactGroupMembershipReadyCondition  = "LoopsContactGroupMembershipReady"
)

// ContactGroupMembershipReconciler reconciles a ContactGroupMembership object
type ContactGroupMembershipController struct {
	Client        client.Client
	EmailProvider emailprovider.Service
	Finalizers    finalizer.Finalizers
}

type contactGroupMembershipFinalizer struct {
	Client        client.Client
	EmailProvider emailprovider.Service
}

// Finalize is the finalizer for the ContactGroupMembership object
func (f *contactGroupMembershipFinalizer) Finalize(ctx context.Context, obj client.Object) (finalizer.Result, error) {
	log := logf.FromContext(ctx).WithValues("finalizer", "ContactGroupMembershipFinalizer", "trigger", obj.GetName())
	log.Info("Finalizing ContactGroupMembership")

	// Type assertion
	contactGroupMembership, ok := obj.(*notificationmiloapiscomv1alpha1.ContactGroupMembership)
	if !ok {
		log.Error(fmt.Errorf("object is not a ContactGroupMembership"), "Failed to finalize ContactGroupMembership")
		return finalizer.Result{}, fmt.Errorf("object is not a ContactGroupMembership")
	}

	// Get Referenced ContactGroup
	contactGroup := &notificationmiloapiscomv1alpha1.ContactGroup{}
	err := f.Client.Get(ctx, client.ObjectKey{Name: contactGroupMembership.Spec.ContactGroupRef.Name, Namespace: contactGroupMembership.Spec.ContactGroupRef.Namespace}, contactGroup)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("Referenced ContactGroup not found. Probably deleted. ContactGroupMembership finalizer completed.")
			return finalizer.Result{}, nil
		}
		log.Error(err, "Failed to get ContactGroup")
		return finalizer.Result{}, fmt.Errorf("failed to get ContactGroup: %w", err)
	}

	// Get Referenced Contact
	contact := &notificationmiloapiscomv1alpha1.Contact{}
	err = f.Client.Get(ctx, client.ObjectKey{Name: contactGroupMembership.Spec.ContactRef.Name, Namespace: contactGroupMembership.Spec.ContactRef.Namespace}, contact)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("Referenced Contact not found. Probably deleted. ContactGroupMembership finalizer completed.")
			return finalizer.Result{}, nil
		}
		log.Error(err, "Failed to get Contact")
		return finalizer.Result{}, fmt.Errorf("failed to get Contact: %w", err)
	}

	// The provider-side membership can only exist if both the contact and the
	// contact group were successfully synced to the email provider. If either
	// provider ID is empty there is nothing to delete on the provider side, so
	// the finalizer can complete.
	if contact.Status.ProviderID == "" || contactGroup.Status.ProviderID == "" {
		log.Info("Contact or ContactGroup was never synced to email provider. Nothing to delete. ContactGroupMembership finalizer completed.")
		return finalizer.Result{}, nil
	}

	// Delete ContactGroupMembership from email provider
	deleted, err := f.EmailProvider.DeleteContactGroupMembershipIdempotent(ctx, *contactGroup, *contact)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("ContactGroupMembership not found on email provider. Probably deleted. ContactGroupMembership finalizer completed.")
			return finalizer.Result{}, nil
		}
		log.Error(err, "Failed to delete ContactGroupMembership from email provider")
		return finalizer.Result{}, fmt.Errorf("failed to delete ContactGroupMembership from email provider: %w", err)
	}
	// The delete call is idempotent: a nil error with Deleted=false means the
	// membership was already absent on the provider, which is the desired final
	// state, so the finalizer can complete.
	if !deleted.Deleted {
		log.Info("ContactGroupMembership already deleted on email provider. ContactGroupMembership finalizer completed.")
	}

	return finalizer.Result{}, nil
}

// +kubebuilder:rbac:groups=notification.miloapis.com,resources=contactgroupmemberships,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=notification.miloapis.com,resources=contactgroupmemberships/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=notification.miloapis.com,resources=contactgroupmemberships/finalizers,verbs=update
// +kubebuilder:rbac:groups=notification.miloapis.com,resources=contactgroupmembershipremovals,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=notification.miloapis.com,resources=contacts,verbs=get
// +kubebuilder:rbac:groups=notification.miloapis.com,resources=contactgroups,verbs=get

// Reconcile is the main function that reconciles the ContactGroupMembership object
func (r *ContactGroupMembershipController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("controller", "ContactGroupMembershipController", "trigger", req.NamespacedName)
	log.Info("Starting reconciliation", "namespacedName", req.String(), "name", req.Name, "namespace", req.Namespace)

	// Get ContactGroupMembership
	contactGroupMembership := &notificationmiloapiscomv1alpha1.ContactGroupMembership{}
	err := r.Client.Get(ctx, req.NamespacedName, contactGroupMembership)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("ContactGroupMembership not found. Probably deleted.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get ContactGroupMembership")
		return ctrl.Result{}, fmt.Errorf("failed to get ContactGroupMembership: %w", err)
	}

	// Run finalizers
	finalizeResult, err := r.Finalizers.Finalize(ctx, contactGroupMembership)
	if err != nil {
		log.Error(err, "Failed to run finalizers for ContactGroupMembership")
		return ctrl.Result{}, fmt.Errorf("failed to run finalizers for ContactGroupMembership: %w", err)
	}
	if finalizeResult.Updated {
		log.Info("finalizer updated the contact group membership object, updating API server")
		if updateErr := r.Client.Update(ctx, contactGroupMembership); updateErr != nil {
			log.Error(updateErr, "Failed to update ContactGroupMembership after finalizer update")
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{}, nil
	}

	// Get Referenced Contact
	contact := &notificationmiloapiscomv1alpha1.Contact{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: contactGroupMembership.Spec.ContactRef.Name, Namespace: contactGroupMembership.Spec.ContactRef.Namespace}, contact)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("Referenced Contact not found. Probably deleted. Skipping ContactGroupMembership reconciliation.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get Contact")
		return ctrl.Result{}, fmt.Errorf("failed to get Contact: %w", err)
	}

	// Get Referenced ContactGroup
	contactGroup := &notificationmiloapiscomv1alpha1.ContactGroup{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: contactGroupMembership.Spec.ContactGroupRef.Name, Namespace: contactGroupMembership.Spec.ContactGroupRef.Namespace}, contactGroup)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("Referenced ContactGroup not found. Probably deleted. Skipping ContactGroupMembership reconciliation.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get ContactGroup")
		return ctrl.Result{}, fmt.Errorf("failed to get ContactGroup: %w", err)
	}

	// Set Status.Username based on Contact.Spec.SubjectRef.Kind
	if contact.Spec.SubjectRef != nil && contact.Spec.SubjectRef.Kind == "User" {
		contactGroupMembership.Status.Username = contact.Spec.SubjectRef.Name
	}

	oldStatus := contactGroupMembership.Status.DeepCopy()
	readyCond := meta.FindStatusCondition(contactGroupMembership.Status.Conditions, ResendContactGroupMembershipReadyCondition)
	updatedCond := meta.FindStatusCondition(contactGroupMembership.Status.Conditions, notificationmiloapiscomv1alpha1.ContactGroupMembershipUpdatedCondition)

	switch {
	case readyCond == nil:
		log.Info("ContactGroupMembership first creation")
		// First creation – condition not present yet
		// Create ContactGroupMembership on email provider
		emailProviderContactGroupMembership, err := r.EmailProvider.CreateContactGroupMembershipIdempotent(ctx, *contactGroup, *contact)
		if err != nil {
			log.Error(err, "Failed to create ContactGroupMembership on email provider")
			return ctrl.Result{}, fmt.Errorf("failed to create ContactGroupMembership on email provider: %w", err)
		}
		contactGroupMembership.Status.ProviderID = emailProviderContactGroupMembership.ContactGroupMembershipID
		meta.SetStatusCondition(&contactGroupMembership.Status.Conditions, metav1.Condition{
			Type:               ResendContactGroupMembershipReadyCondition,
			Status:             metav1.ConditionTrue,
			Reason:             notificationmiloapiscomv1alpha1.ContactGroupMembershipCreatedReason,
			Message:            "ContactGroupMembership created and synced with email provider",
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: contactGroupMembership.GetGeneration(),
		})

		contactGroupMembership.Status.Providers = []notificationmiloapiscomv1alpha1.ContactProviderStatus{
			{
				Name: "Resend",
				ID:   emailProviderContactGroupMembership.ContactGroupMembershipID,
			},
		}

	// Update requested – Updated condition is False with the specific reason
	case updatedCond != nil && updatedCond.Status == metav1.ConditionFalse && updatedCond.Reason == notificationmiloapiscomv1alpha1.ContactGroupMembershipUpdateRequestedReason:
		log.Info("ContactGroupMembership update requested")
		// Create ContactGroupMembership on email provider again
		// As Resend does not supports updating the Contact email address, on Contact update, the contact has been deleted (which removes the Contacts from all ContactGroups on resend side),
		// and created again with the new email address, so we need to create the ContactGroupMembership again.
		emailProviderContactGroupMembership, err := r.EmailProvider.CreateContactGroupMembershipIdempotent(ctx, *contactGroup, *contact)
		if err != nil {
			log.Error(err, "Failed to update ContactGroupMembership on email provider")
			return ctrl.Result{}, fmt.Errorf("failed to update ContactGroupMembership on email provider: %w", err)
		}
		contactGroupMembership.Status.ProviderID = emailProviderContactGroupMembership.ContactGroupMembershipID

		// Update ContactGroupMembership status to updated. This will avoid multiple updates to the email provider.
		meta.SetStatusCondition(&contactGroupMembership.Status.Conditions, metav1.Condition{
			Type:               notificationmiloapiscomv1alpha1.ContactGroupMembershipUpdatedCondition,
			Status:             metav1.ConditionTrue,
			Reason:             notificationmiloapiscomv1alpha1.ContactGroupMembershipUpdatedReason,
			Message:            "ContactGroupMembership updated, and synced with email provider",
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: contactGroupMembership.GetGeneration(),
		})
	}

	r.verifyContactGroupMembershipReadyCondition(contactGroupMembership)

	// Update status if it changed
	if !equality.Semantic.DeepEqual(oldStatus, &contactGroupMembership.Status) {
		if err := r.Client.Status().Update(ctx, contactGroupMembership); err != nil {
			log.Error(err, "Failed to update contact group membership status")
			return ctrl.Result{}, fmt.Errorf("failed to update contact group membership status: %w", err)
		}
	} else {
		log.Info("Contact group membership status unchanged, skipping update")
	}

	log.Info("Contact group membership reconciled")

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ContactGroupMembershipController) SetupWithManager(mgr ctrl.Manager) error {
	// Index by contact ref and contact group ref for efficient contact group membership removal lookup
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &notificationmiloapiscomv1alpha1.ContactGroupMembershipRemoval{}, contactAndContactGroupTupleIndexKey, func(rawObj client.Object) []string {
		cgmr := rawObj.(*notificationmiloapiscomv1alpha1.ContactGroupMembershipRemoval)
		return []string{buildContactAndContactGroupTupleIndexKey(cgmr.Spec.ContactRef, cgmr.Spec.ContactGroupRef)}
	}); err != nil {
		return fmt.Errorf("failed to index contactgroupmembership by contact ref and contact group ref: %w", err)
	}
	// Index by contact namespaced key for efficient lookup from contact updates
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &notificationmiloapiscomv1alpha1.ContactGroupMembership{}, cgmByContactNamespacedNameIndexKey, func(rawObj client.Object) []string {
		cgm := rawObj.(*notificationmiloapiscomv1alpha1.ContactGroupMembership)
		return []string{buildContactNamespacedIndexKey(cgm.Spec.ContactRef.Name, cgm.Spec.ContactRef.Namespace)}
	}); err != nil {
		return fmt.Errorf("failed to index contactgroupmembership by contact name: %w", err)
	}

	r.Finalizers = finalizer.NewFinalizers()
	if err := r.Finalizers.Register(contactGroupMembershipFinalizerKey, &contactGroupMembershipFinalizer{
		Client:        r.Client,
		EmailProvider: r.EmailProvider}); err != nil {
		return fmt.Errorf("failed to register contact group membership finalizer: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&notificationmiloapiscomv1alpha1.ContactGroupMembership{}).
		Watches(
			&notificationmiloapiscomv1alpha1.Contact{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueContactForContactGroupMembershipUpdate),
			builder.WithPredicates(predicate.Funcs{
				CreateFunc:  func(e event.CreateEvent) bool { return false },
				DeleteFunc:  func(e event.DeleteEvent) bool { return false },
				GenericFunc: func(e event.GenericEvent) bool { return false },
				UpdateFunc:  func(e event.UpdateEvent) bool { return true },
			}),
		).
		Named("contactgroupmembership").
		Complete(r)
}

func (r *ContactGroupMembershipController) enqueueContactForContactGroupMembershipUpdate(ctx context.Context, obj client.Object) []reconcile.Request {
	log := logf.FromContext(ctx).WithValues("controller", "enqueueContactForContactGroupMembershipUpdate", "trigger", obj.GetName())

	contact, ok := obj.(*notificationmiloapiscomv1alpha1.Contact)
	if !ok {
		log.Error(fmt.Errorf("object is not a Contact"), "Failed to enqueue ContactGroupMembership for update")
		return nil
	}

	cgmList := &notificationmiloapiscomv1alpha1.ContactGroupMembershipList{}
	err := r.Client.List(ctx, cgmList, client.MatchingFields{cgmByContactNamespacedNameIndexKey: buildContactNamespacedIndexKey(contact.Name, contact.Namespace)})
	if err != nil {
		log.Error(err, "Failed to list ContactGroupMembership")
		return nil
	}

	var reqs = make([]reconcile.Request, 0, len(cgmList.Items))
	for _, cgm := range cgmList.Items {
		reqs = append(reqs, ctrl.Request{
			NamespacedName: client.ObjectKey{Name: cgm.Name, Namespace: cgm.Namespace},
		})
	}

	return reqs
}

// verifyContactReadyCondition is a function that verifies the ContactReadyCondition
func (r *ContactGroupMembershipController) verifyContactGroupMembershipReadyCondition(cgm *notificationmiloapiscomv1alpha1.ContactGroupMembership) {
	// Update contact status if it changed
	// Aggregate readiness across providers: Loops and Resend
	loopsCond := meta.FindStatusCondition(cgm.Status.Conditions, LoopsContactGroupMembershipReadyCondition)
	resendCond := meta.FindStatusCondition(cgm.Status.Conditions, ResendContactGroupMembershipReadyCondition)
	allReady := loopsCond != nil && loopsCond.Status == metav1.ConditionTrue &&
		resendCond != nil && resendCond.Status == metav1.ConditionTrue

	if allReady {
		meta.SetStatusCondition(&cgm.Status.Conditions, metav1.Condition{
			Type:               notificationmiloapiscomv1alpha1.ContactGroupMembershipReadyCondition,
			Status:             metav1.ConditionTrue,
			Reason:             "AllProvidersReady",
			Message:            "Loops and Resend contact group membership are ready",
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: cgm.GetGeneration(),
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
		meta.SetStatusCondition(&cgm.Status.Conditions, metav1.Condition{
			Type:               notificationmiloapiscomv1alpha1.ContactGroupMembershipReadyCondition,
			Status:             metav1.ConditionFalse,
			Reason:             "ProvidersNotReady",
			Message:            notReadyMsg,
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: cgm.GetGeneration(),
		})
	}
}
