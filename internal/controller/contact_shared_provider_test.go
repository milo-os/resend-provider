package controller

import (
	"context"

	ginkgo "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
	notificationv1 "go.miloapis.com/milo/pkg/apis/notification/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	finalizerpkg "sigs.k8s.io/controller-runtime/pkg/finalizer"

	"go.miloapis.com/email-provider-resend/internal/emailprovider"
	"go.miloapis.com/email-provider-resend/internal/emailprovider/mockprovider"
)

// Contacts with the same email address share one Resend contact. These tests
// check it is only deleted by the last Contact using it.
var _ = ginkgo.Describe("Contacts sharing a Resend contact", func() {
	const sharedID = "resend-contact-shared"

	var ctx context.Context

	newContact := func(namespace, name string) *notificationv1.Contact {
		return &notificationv1.Contact{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: 1},
			Spec:       notificationv1.ContactSpec{Email: "shared@example.com"},
			Status:     notificationv1.ContactStatus{ProviderID: sharedID},
		}
	}

	beingDeleted := func(c *notificationv1.Contact) *notificationv1.Contact {
		now := metav1.Now()
		c.DeletionTimestamp = &now
		c.Finalizers = []string{contactFinalizerKey}
		return c
	}

	newClient := func(objs ...client.Object) client.Client {
		return fake.NewClientBuilder().
			WithScheme(scheme.Scheme).
			WithStatusSubresource(&notificationv1.Contact{}).
			WithObjects(objs...).
			WithIndex(&notificationv1.Contact{}, contactProviderIDIndexKey, contactProviderIDIndex).
			WithIndex(&notificationv1.ContactGroupMembership{}, contactNamespacedIndexKey, func(raw client.Object) []string {
				c := raw.(*notificationv1.ContactGroupMembership)
				return []string{buildContactNamespacedIndexKey(c.Spec.ContactRef.Name, c.Spec.ContactRef.Namespace)}
			}).
			WithIndex(&notificationv1.ContactGroupMembershipRemoval{}, contactNamespacedIndexKey, func(raw client.Object) []string {
				c := raw.(*notificationv1.ContactGroupMembershipRemoval)
				return []string{buildContactNamespacedIndexKey(c.Spec.ContactRef.Name, c.Spec.ContactRef.Namespace)}
			}).
			Build()
	}

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		gomega.Expect(notificationv1.AddToScheme(scheme.Scheme)).To(gomega.Succeed())
	})

	ginkgo.It("keeps the Resend contact while another Contact uses it", func() {
		deleting := beingDeleted(newContact("ns-a", "contact-a"))
		k8sClient := newClient(deleting, newContact("ns-b", "contact-b"))
		prov := &mockprovider.MockEmailProvider{DeleteContactOutput: emailprovider.DeleteContactOutput{Deleted: true}}
		f := &contactFinalizer{Client: k8sClient, EmailProvider: *emailprovider.NewService(prov, "from", "reply")}

		res, err := f.Finalize(ctx, deleting.DeepCopy())
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "the finalizer must complete")
		gomega.Expect(res).To(gomega.Equal(finalizerpkg.Result{}))
		gomega.Expect(prov.DeleteContactCallCount).To(gomega.Equal(0),
			"contact-b still uses the Resend contact, so it must not be deleted")
	})

	ginkgo.It("deletes the Resend contact when the last Contact using it is deleted", func() {
		deleting := beingDeleted(newContact("ns-a", "contact-a"))
		other := newContact("ns-b", "contact-b")
		other.Status.ProviderID = "resend-contact-other"
		k8sClient := newClient(deleting, other)
		prov := &mockprovider.MockEmailProvider{DeleteContactOutput: emailprovider.DeleteContactOutput{Deleted: true}}
		f := &contactFinalizer{Client: k8sClient, EmailProvider: *emailprovider.NewService(prov, "from", "reply")}

		_, err := f.Finalize(ctx, deleting.DeepCopy())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(prov.DeleteContactCallCount).To(gomega.Equal(1))
		gomega.Expect(prov.LastDeleteContactInput.ContactId).To(gomega.Equal(sharedID))
	})

	ginkgo.It("deletes the Resend contact when every Contact using it is deleted together", func() {
		a := beingDeleted(newContact("ns-a", "contact-a"))
		b := beingDeleted(newContact("ns-b", "contact-b"))
		k8sClient := newClient(a, b)

		// The first delete succeeds; the second finds the contact gone.
		provA := &mockprovider.MockEmailProvider{DeleteContactOutput: emailprovider.DeleteContactOutput{Deleted: true}}
		provB := &mockprovider.MockEmailProvider{
			DeleteContactErr: apierrors.NewNotFound(schema.GroupResource{Group: "resend", Resource: "contacts"}, sharedID),
		}
		fa := &contactFinalizer{Client: k8sClient, EmailProvider: *emailprovider.NewService(provA, "from", "reply")}
		fb := &contactFinalizer{Client: k8sClient, EmailProvider: *emailprovider.NewService(provB, "from", "reply")}

		_, err := fa.Finalize(ctx, a.DeepCopy())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = fb.Finalize(ctx, b.DeepCopy())
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "a NotFound from the second delete must be tolerated")
		gomega.Expect(provA.DeleteContactCallCount+provB.DeleteContactCallCount).To(gomega.BeNumerically(">=", 1),
			"the shared Resend contact must be deleted when no Contact is left using it")
	})

	ginkgo.It("keeps the old Resend contact on an address change while another Contact uses it, and still creates the new one", func() {
		changed := newContact("ns-a", "contact-a")
		changed.Generation = 2
		changed.Spec.Email = "new@example.com"
		meta.SetStatusCondition(&changed.Status.Conditions, metav1.Condition{
			Type:               notificationv1.ContactUpdatedCondition,
			Status:             metav1.ConditionTrue,
			Reason:             notificationv1.ContactUpdatedReason,
			ObservedGeneration: 1,
		})
		meta.SetStatusCondition(&changed.Status.Conditions, metav1.Condition{
			Type:   ResendContactReadyCondition,
			Status: metav1.ConditionTrue,
			Reason: ResendContactCreatedReason,
		})
		k8sClient := newClient(changed, newContact("ns-b", "contact-b"))
		prov := &mockprovider.MockEmailProvider{
			DeleteContactOutput: emailprovider.DeleteContactOutput{Deleted: true},
			CreateContactOutput: emailprovider.CreateContactOutput{ContactId: "resend-contact-new"},
		}
		controller := &ContactController{Client: k8sClient, EmailProvider: *emailprovider.NewService(prov, "from", "reply")}
		controller.Finalizers = finalizerpkg.NewFinalizers()

		_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns-a", Name: "contact-a"}})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(prov.DeleteContactCallCount).To(gomega.Equal(0),
			"contact-b still uses the old Resend contact, so it must not be deleted")
		gomega.Expect(prov.CreateContactCallCount).To(gomega.Equal(1))
		gomega.Expect(prov.LastCreateContactInput.Email).To(gomega.Equal("new@example.com"))

		updated := &notificationv1.Contact{}
		gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "ns-a", Name: "contact-a"}, updated)).To(gomega.Succeed())
		gomega.Expect(updated.Status.ProviderID).To(gomega.Equal("resend-contact-new"))
	})

	ginkgo.It("deletes the old Resend contact on an address change when no other Contact uses it", func() {
		changed := newContact("ns-a", "contact-a")
		changed.Generation = 2
		changed.Spec.Email = "new@example.com"
		meta.SetStatusCondition(&changed.Status.Conditions, metav1.Condition{
			Type:               notificationv1.ContactUpdatedCondition,
			Status:             metav1.ConditionTrue,
			Reason:             notificationv1.ContactUpdatedReason,
			ObservedGeneration: 1,
		})
		meta.SetStatusCondition(&changed.Status.Conditions, metav1.Condition{
			Type:   ResendContactReadyCondition,
			Status: metav1.ConditionTrue,
			Reason: ResendContactCreatedReason,
		})
		k8sClient := newClient(changed)
		prov := &mockprovider.MockEmailProvider{
			DeleteContactOutput: emailprovider.DeleteContactOutput{Deleted: true},
			CreateContactOutput: emailprovider.CreateContactOutput{ContactId: "resend-contact-new"},
		}
		controller := &ContactController{Client: k8sClient, EmailProvider: *emailprovider.NewService(prov, "from", "reply")}
		controller.Finalizers = finalizerpkg.NewFinalizers()

		_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns-a", Name: "contact-a"}})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(prov.DeleteContactCallCount).To(gomega.Equal(1))
		gomega.Expect(prov.LastDeleteContactInput.ContactId).To(gomega.Equal(sharedID))
		gomega.Expect(prov.CreateContactCallCount).To(gomega.Equal(1))
	})
})
