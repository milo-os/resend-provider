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

	// Contacts by their Resend contact ID. Contacts with the same email address
	// share one Resend contact, so the Contact controller reads this before
	// deleting a Resend contact that another Contact may still use.
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &notificationmiloapiscomv1alpha1.Contact{}, contactProviderIDIndexKey, contactProviderIDIndex); err != nil {
		return fmt.Errorf("failed to index contact by provider ID: %w", err)
	}
	return nil
}

// contactProviderIDIndexKey indexes Contacts by status.providerID.
const contactProviderIDIndexKey = "contact-status-providerID"

func contactProviderIDIndex(rawObj client.Object) []string {
	contact := rawObj.(*notificationmiloapiscomv1alpha1.Contact)
	if contact.Status.ProviderID == "" {
		return nil
	}
	return []string{contact.Status.ProviderID}
}

// resendContactInUseByOthers reports whether a Contact other than contact uses
// the same Resend contact and isn't being deleted itself.
//
// Contacts with the same email address share one Resend contact, because
// creating a contact reuses an existing one with that address. Deleting the
// Resend contact while another Contact still points at it would remove that
// Contact's subscriptions too, so the Resend contact is only deleted by the
// last Contact using it. Contacts being deleted don't count as users: when
// several are deleted together, each may delete the shared Resend contact, and
// the ones that run after the first get a NotFound, which they tolerate.
func resendContactInUseByOthers(ctx context.Context, c client.Reader, contact *notificationmiloapiscomv1alpha1.Contact) (bool, error) {
	if contact.Status.ProviderID == "" {
		return false, nil
	}
	contacts := &notificationmiloapiscomv1alpha1.ContactList{}
	if err := c.List(ctx, contacts, client.MatchingFields{contactProviderIDIndexKey: contact.Status.ProviderID}); err != nil {
		return false, fmt.Errorf("failed to list contacts by provider ID: %w", err)
	}
	for _, other := range contacts.Items {
		if other.Namespace == contact.Namespace && other.Name == contact.Name {
			continue
		}
		if other.DeletionTimestamp != nil {
			continue
		}
		return true, nil
	}
	return false, nil
}
