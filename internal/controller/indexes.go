package controller

import (
	"context"
	"fmt"

	notificationmiloapiscomv1alpha1 "go.miloapis.com/milo/pkg/apis/notification/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SetupIndexes registers the field indexes that more than one controller
// reads. Call it once, before setting up the controllers: registering the
// same index twice fails.
func SetupIndexes(mgr ctrl.Manager) error {
	// ContactGroupMemberships by the Contact they reference. The Contact
	// controller reads it to find a Contact's memberships, and the
	// ContactGroupMembership controller to enqueue them on Contact updates.
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &notificationmiloapiscomv1alpha1.ContactGroupMembership{}, contactNamespacedIndexKey, func(rawObj client.Object) []string {
		cgm := rawObj.(*notificationmiloapiscomv1alpha1.ContactGroupMembership)
		return []string{buildContactNamespacedIndexKey(cgm.Spec.ContactRef.Name, cgm.Spec.ContactRef.Namespace)}
	}); err != nil {
		return fmt.Errorf("failed to index contactgroupmembership by contact name: %w", err)
	}
	return nil
}
