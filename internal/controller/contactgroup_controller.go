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
)

const (
	contactGroupFinalizerKey             = "notification.miloapis.com/contactgroup"
	contactGroupNamespacedIndexKey       = "contactgroup-namespaced-index"
	contactGroupToCgmrNamespacedIndexKey = "contactgroup-to-cgmr-namespaced-index"
)

// buildContactGroupNamespacedIndexKey returns "<group-ns>|<group-name>"
func buildContactGroupNamespacedIndexKey(cgName, cgNamespace string) string {
	return fmt.Sprintf("%s|%s", cgNamespace, cgName)
}

// ContactGroupReconciler reconciles a ContactGroup object
type ContactGroupController struct {
	Client        client.Client
	Finalizers    finalizer.Finalizers
	EmailProvider emailprovider.Service
}

// contactGroupFinalizer is a finalizer for the ContactGroup object
type contactGroupFinalizer struct {
	Client        client.Client
	EmailProvider emailprovider.Service
}

// Finalize is the finalizer for the ContactGroup object
func (f *contactGroupFinalizer) Finalize(ctx context.Context, obj client.Object) (finalizer.Result, error) {
	log := logf.FromContext(ctx).WithValues("finalizer", "ContactGroupFinalizer", "trigger", obj.GetName())
	log.Info("Finalizing ContactGroup")

	// Type assertion
	contactGroup, ok := obj.(*notificationmiloapiscomv1alpha1.ContactGroup)
	if !ok {
		log.Error(fmt.Errorf("object is not a ContactGroup"), "Failed to finalize ContactGroup")
		return finalizer.Result{}, fmt.Errorf("object is not a ContactGroup")
	}

	// Get associated ContactGroupMemberships to contact group name
	contactGroupMemberships := &notificationmiloapiscomv1alpha1.ContactGroupMembershipList{}
	err := f.Client.List(ctx, contactGroupMemberships, client.MatchingFields{contactGroupNamespacedIndexKey: buildContactGroupNamespacedIndexKey(contactGroup.Name, contactGroup.Namespace)})
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

	// Get associated ContactGroupMemberships to contact group name
	// GroupMembership needs to be fully removed before deleting the contact group
	notDeletedContactGroupMemberships := &notificationmiloapiscomv1alpha1.ContactGroupMembershipList{}
	err = f.Client.List(ctx, notDeletedContactGroupMemberships, client.MatchingFields{contactGroupNamespacedIndexKey: buildContactGroupNamespacedIndexKey(contactGroup.Name, contactGroup.Namespace)})
	if err != nil {
		log.Error(err, "Failed to list ContactGroupMemberships")
		return finalizer.Result{}, fmt.Errorf("failed to list ContactGroupMemberships: %w", err)
	}
	if len(notDeletedContactGroupMemberships.Items) > 0 {
		log.Info("Waiting for ContactGroupMembership deletions", "count", len(notDeletedContactGroupMemberships.Items))
		return finalizer.Result{}, fmt.Errorf("waiting for %d ContactGroupMembership deletions", len(notDeletedContactGroupMemberships.Items))
	}

	// Get ContactGroupMembershipRemoval that references this ContactGroup
	contactGroupMembershipRemovals := &notificationmiloapiscomv1alpha1.ContactGroupMembershipRemovalList{}
	err = f.Client.List(ctx, contactGroupMembershipRemovals, client.MatchingFields{contactGroupToCgmrNamespacedIndexKey: buildContactGroupNamespacedIndexKey(contactGroup.Name, contactGroup.Namespace)})
	if err != nil {
		log.Error(err, "Failed to list ContactGroupMembershipRemoval")
		return finalizer.Result{}, fmt.Errorf("failed to list ContactGroupMembershipRemoval: %w", err)
	}
	// Delete ContactGroupMembershipRemoval that references this contact group
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

	// Delete email provider contact group
	// 1. Get contact group from email provider
	_, err = f.EmailProvider.GetContactGroup(ctx, *contactGroup)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("ContactGroup not found on email provider. Probably deleted. Contact Group finalizer completed.")
			return finalizer.Result{}, nil
		}
		log.Error(err, "Failed to get ContactGroup from email provider")
		return finalizer.Result{}, fmt.Errorf("failed to get ContactGroup from email provider: %w", err)
	}
	// 2. Delete contact group from email provider if not deleted yet
	delResult, err := f.EmailProvider.DeleteContactGroup(ctx, *contactGroup)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("ContactGroup not found on email provider. Probably deleted. Contact Group finalizer completed.")
			return finalizer.Result{}, nil
		}
		log.Error(err, "Failed to delete ContactGroup from email provider")
		return finalizer.Result{}, fmt.Errorf("failed to delete ContactGroup from email provider: %w", err)
	}
	// The delete call is idempotent: a nil error with Deleted=false means the
	// contact group was already absent on the provider, which is the desired
	// final state, so the finalizer can complete.
	if !delResult.Deleted {
		log.Info("ContactGroup already deleted on email provider. Contact Group finalizer completed.")
	}

	log.Info("Contact group finalizer completed")

	return finalizer.Result{}, nil
}

// +kubebuilder:rbac:groups=notification.miloapis.com,resources=contactgroups,verbs=get;list;watch
// +kubebuilder:rbac:groups=notification.miloapis.com,resources=contactgroups/status,verbs=get;update
// +kubebuilder:rbac:groups=notification.miloapis.com,resources=contactgroups/finalizers,verbs=update
// +kubebuilder:rbac:groups=notification.miloapis.com,resources=contactgroupmemberships,verbs=get;list;watch;delete

// Reconcile is the main function that reconciles the ContactGroup object
func (r *ContactGroupController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("controller", "ContactGroupController", "trigger", req.NamespacedName)
	log.Info("Starting reconciliation", "namespacedName", req.String(), "name", req.Name, "namespace", req.Namespace)

	// Get ContactGroup
	contactGroup := &notificationmiloapiscomv1alpha1.ContactGroup{}
	err := r.Client.Get(ctx, req.NamespacedName, contactGroup)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("ContactGroup not found. Probably deleted.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get ContactGroup")
		return ctrl.Result{}, fmt.Errorf("failed to get ContactGroup: %w", err)
	}

	// Run finalizers
	finalizeResult, err := r.Finalizers.Finalize(ctx, contactGroup)
	if err != nil {
		log.Error(err, "Failed to run finalizers for ContactGroup")
		return ctrl.Result{}, fmt.Errorf("failed to run finalizers for ContactGroup: %w", err)
	}
	if finalizeResult.Updated {
		log.Info("finalizer updated the contact group object, updating API server")
		if updateErr := r.Client.Update(ctx, contactGroup); updateErr != nil {
			log.Error(updateErr, "Failed to update ContactGroup after finalizer update")
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{}, nil
	}

	oldStatus := contactGroup.Status.DeepCopy()
	existingCond := meta.FindStatusCondition(contactGroup.Status.Conditions, notificationmiloapiscomv1alpha1.ContactGroupReadyCondition)

	switch {
	// First creation – condition not present yet
	case existingCond == nil:
		// Create ContactGroup on email provider
		emailProviderContactGroup, err := r.EmailProvider.CreateContactGroup(ctx, *contactGroup)
		if err != nil {
			log.Error(err, "Failed to create ContactGroup on email provider")
			return ctrl.Result{}, fmt.Errorf("failed to create ContactGroup on email provider: %w", err)
		}
		contactGroup.Status.ProviderID = emailProviderContactGroup.ContactGroupID
		meta.SetStatusCondition(&contactGroup.Status.Conditions, metav1.Condition{
			Type:               notificationmiloapiscomv1alpha1.ContactGroupReadyCondition,
			Status:             metav1.ConditionTrue,
			Reason:             notificationmiloapiscomv1alpha1.ContactGroupCreatedReason,
			Message:            "Contact group created on email provider",
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: contactGroup.GetGeneration(),
		})

	// Update – generation changed since we last processed the object
	case existingCond.ObservedGeneration != contactGroup.GetGeneration():
		// No update on email provider needed, displayName change do not impact the contact group on the email provider.
		meta.SetStatusCondition(&contactGroup.Status.Conditions, metav1.Condition{
			Type:               notificationmiloapiscomv1alpha1.ContactGroupUpdatedCondition,
			Status:             metav1.ConditionTrue,
			Reason:             notificationmiloapiscomv1alpha1.ContactGroupUpdatedReason,
			Message:            "Contact group updated",
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: contactGroup.GetGeneration(),
		})
	}

	// Update status if it changed
	if !equality.Semantic.DeepEqual(oldStatus, &contactGroup.Status) {
		if err := r.Client.Status().Update(ctx, contactGroup); err != nil {
			log.Error(err, "Failed to update contact status")
			return ctrl.Result{}, fmt.Errorf("failed to update contact group status: %w", err)
		}
	} else {
		log.Info("Contact group status unchanged, skipping update")
	}

	log.Info("Contact group reconciled")

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ContactGroupController) SetupWithManager(mgr ctrl.Manager) error {
	// Index by contact name for efficient contact group membership lookup
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &notificationmiloapiscomv1alpha1.ContactGroupMembership{}, contactGroupNamespacedIndexKey, func(rawObj client.Object) []string {
		cgm := rawObj.(*notificationmiloapiscomv1alpha1.ContactGroupMembership)
		return []string{buildContactGroupNamespacedIndexKey(cgm.Spec.ContactGroupRef.Name, cgm.Spec.ContactGroupRef.Namespace)}
	}); err != nil {
		return fmt.Errorf("failed to index contactgroupmembership by contact name: %w", err)
	}

	// Index by contact group membership removal for efficient contact group membership removal lookup
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &notificationmiloapiscomv1alpha1.ContactGroupMembershipRemoval{}, contactGroupToCgmrNamespacedIndexKey, func(rawObj client.Object) []string {
		cgmr := rawObj.(*notificationmiloapiscomv1alpha1.ContactGroupMembershipRemoval)
		return []string{buildContactGroupNamespacedIndexKey(cgmr.Spec.ContactGroupRef.Name, cgmr.Spec.ContactGroupRef.Namespace)}
	}); err != nil {
		return fmt.Errorf("failed to index contactgroupmembership by contact name: %w", err)
	}

	r.Finalizers = finalizer.NewFinalizers()
	if err := r.Finalizers.Register(contactGroupFinalizerKey, &contactGroupFinalizer{
		Client:        r.Client,
		EmailProvider: r.EmailProvider}); err != nil {
		return fmt.Errorf("failed to register contact group finalizer: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&notificationmiloapiscomv1alpha1.ContactGroup{}).
		Named("contactgroup").
		Complete(r)
}
