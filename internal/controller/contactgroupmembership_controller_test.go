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

	ginkgo "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
	"go.miloapis.com/email-provider-resend/internal/emailprovider"
	"go.miloapis.com/email-provider-resend/internal/emailprovider/mockprovider"
	notificationv1 "go.miloapis.com/milo/pkg/apis/notification/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	finalizerpkg "sigs.k8s.io/controller-runtime/pkg/finalizer"
)

const testMembershipProviderID = "cgm-1"

var _ = ginkgo.Describe("ContactGroupMembershipController", func() {
	var (
		ctx        context.Context
		k8sClient  client.Client
		controller *ContactGroupMembershipController

		contact      *notificationv1.Contact
		contactGroup *notificationv1.ContactGroup
		membership   *notificationv1.ContactGroupMembership
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()

		contact = &notificationv1.Contact{
			TypeMeta:   metav1.TypeMeta{APIVersion: "notification.miloapis.com/v1alpha1", Kind: "Contact"},
			ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "default"},
			Spec: notificationv1.ContactSpec{
				FamilyName: "Doe",
				GivenName:  "Alice",
				Email:      "alice@example.com",
				SubjectRef: &notificationv1.SubjectReference{Kind: "User", Name: "alice-user"},
			},
		}

		contactGroup = &notificationv1.ContactGroup{
			TypeMeta:   metav1.TypeMeta{APIVersion: "notification.miloapis.com/v1alpha1", Kind: "ContactGroup"},
			ObjectMeta: metav1.ObjectMeta{Name: "devs", Namespace: "default"},
			Spec:       notificationv1.ContactGroupSpec{DisplayName: "Developers"},
			Status:     notificationv1.ContactGroupStatus{ProviderID: "cg-1"},
		}

		membership = &notificationv1.ContactGroupMembership{
			TypeMeta:   metav1.TypeMeta{APIVersion: "notification.miloapis.com/v1alpha1", Kind: "ContactGroupMembership"},
			ObjectMeta: metav1.ObjectMeta{Name: "m-1", Namespace: "default"},
			Spec: notificationv1.ContactGroupMembershipSpec{
				ContactRef:      notificationv1.ContactReference{Name: contact.Name, Namespace: contact.Namespace},
				ContactGroupRef: notificationv1.ContactGroupReference{Name: contactGroup.Name, Namespace: contactGroup.Namespace},
			},
		}

		sch := scheme.Scheme
		gomega.Expect(notificationv1.AddToScheme(sch)).To(gomega.Succeed())

		k8sClient = fake.NewClientBuilder().
			WithScheme(sch).
			WithStatusSubresource(&notificationv1.ContactGroupMembership{}).
			WithObjects(contact.DeepCopy(), contactGroup.DeepCopy(), membership.DeepCopy()).
			Build()

		prov := &mockprovider.MockEmailProvider{
			CreateContactGroupOutput:           emailprovider.CreateContactGroupOutput{ContactGroupID: "cg-1"},
			CreateContactGroupMembershipOutput: emailprovider.CreateContactGroupMembershipOutput{ContactGroupMembershipID: testMembershipProviderID},
		}
		svc := emailprovider.NewService(prov, "from@example.com", "reply@example.com")
		controller = &ContactGroupMembershipController{Client: k8sClient, EmailProvider: *svc}
		controller.Finalizers = finalizerpkg.NewFinalizers()
	})

	ginkgo.Context("Reconcile", func() {
		ginkgo.It("creates membership on provider and sets Resend-specific condition", func() {
			_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: membership.Name, Namespace: membership.Namespace}})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			fetched := &notificationv1.ContactGroupMembership{}
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: membership.Name, Namespace: membership.Namespace}, fetched)).To(gomega.Succeed())

			gomega.Expect(fetched.Status.ProviderID).To(gomega.Equal(testMembershipProviderID))
			// Check status.username is set from contact's SubjectRef
			gomega.Expect(fetched.Status.Username).To(gomega.Equal("alice-user"))
			// Check Resend-specific condition (this controller only manages Resend)
			resendCond := meta.FindStatusCondition(fetched.Status.Conditions, ResendContactGroupMembershipReadyCondition)
			gomega.Expect(resendCond).NotTo(gomega.BeNil())
			gomega.Expect(resendCond.Reason).To(gomega.Equal(notificationv1.ContactGroupMembershipCreatedReason))

		})
	})

	ginkgo.Describe("contactGroupMembershipFinalizer", func() {
		var (
			k8sClientFinal client.Client
			finalizer      *contactGroupMembershipFinalizer
			svc            *emailprovider.Service
			fakeProv       *mockprovider.MockEmailProvider
		)

		ginkgo.BeforeEach(func() {
			sch := scheme.Scheme
			gomega.Expect(notificationv1.AddToScheme(sch)).To(gomega.Succeed())

			// membership with ProviderID
			membershipWithID := membership.DeepCopy()
			membershipWithID.Status.ProviderID = testMembershipProviderID

			// corresponding removal object expected to be deleted by finalizer
			removal := &notificationv1.ContactGroupMembershipRemoval{
				TypeMeta:   metav1.TypeMeta{APIVersion: "notification.miloapis.com/v1alpha1", Kind: "ContactGroupMembershipRemoval"},
				ObjectMeta: metav1.ObjectMeta{Name: "remove-1", Namespace: "default"},
				Spec: notificationv1.ContactGroupMembershipRemovalSpec{
					ContactRef:      notificationv1.ContactReference{Name: contact.Name, Namespace: contact.Namespace},
					ContactGroupRef: notificationv1.ContactGroupReference{Name: contactGroup.Name, Namespace: contactGroup.Namespace},
				},
			}

			k8sClientFinal = fake.NewClientBuilder().
				WithScheme(sch).
				WithObjects(contactGroup.DeepCopy(), membershipWithID, removal).
				WithIndex(&notificationv1.ContactGroupMembershipRemoval{}, contactAndContactGroupTupleIndexKey, func(raw client.Object) []string {
					cgmr := raw.(*notificationv1.ContactGroupMembershipRemoval)
					return []string{buildContactAndContactGroupTupleIndexKey(cgmr.Spec.ContactRef, cgmr.Spec.ContactGroupRef)}
				}).
				Build()

			fakeProv = &mockprovider.MockEmailProvider{}
			svc = emailprovider.NewService(fakeProv, "from", "reply")
			finalizer = &contactGroupMembershipFinalizer{Client: k8sClientFinal, EmailProvider: *svc}
		})

		ginkgo.It("completes when the referenced contact no longer exists", func() {
			// The fake client in BeforeEach does NOT contain the referenced
			// contact, simulating a teardown where the contact was already
			// deleted. The finalizer must complete rather than failing.
			obj := membership.DeepCopy()
			obj.Status.ProviderID = testMembershipProviderID
			res, err := finalizer.Finalize(ctx, obj)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(res).To(gomega.Equal(finalizerpkg.Result{}))
		})

		ginkgo.It("deletes membership on provider when the referenced resources exist", func() {
			// The referenced contact must be synced to the provider (has a
			// Status.ProviderID) for the finalizer to reach the provider delete
			// call.
			syncedContact := contact.DeepCopy()
			syncedContact.Status.ProviderID = "c-1"
			sch := scheme.Scheme
			gomega.Expect(notificationv1.AddToScheme(sch)).To(gomega.Succeed())
			membershipWithID := membership.DeepCopy()
			membershipWithID.Status.ProviderID = testMembershipProviderID
			k8sClientFinal = fake.NewClientBuilder().
				WithScheme(sch).
				WithObjects(syncedContact, contactGroup.DeepCopy(), membershipWithID).
				Build()
			finalizer = &contactGroupMembershipFinalizer{Client: k8sClientFinal, EmailProvider: *svc}

			obj := membership.DeepCopy()
			obj.Status.ProviderID = testMembershipProviderID
			res, err := finalizer.Finalize(ctx, obj)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(res).To(gomega.Equal(finalizerpkg.Result{}))
			// Mock default returns Deleted:true.
			gomega.Expect(fakeProv.DeleteContactGroupMembershipInputs).To(gomega.HaveLen(1))
		})

		ginkgo.It("completes when the provider reports the membership already deleted", func() {
			// Same client as the previous test: the referenced resources exist
			// and the contact was synced to the provider.
			syncedContact := contact.DeepCopy()
			syncedContact.Status.ProviderID = "c-1"
			sch := scheme.Scheme
			gomega.Expect(notificationv1.AddToScheme(sch)).To(gomega.Succeed())
			membershipWithID := membership.DeepCopy()
			membershipWithID.Status.ProviderID = testMembershipProviderID
			k8sClientFinal = fake.NewClientBuilder().
				WithScheme(sch).
				WithObjects(syncedContact, contactGroup.DeepCopy(), membershipWithID).
				Build()
			finalizer = &contactGroupMembershipFinalizer{Client: k8sClientFinal, EmailProvider: *svc}

			// Provider returns Deleted:false with no error (idempotent no-op).
			fakeProv.DeleteContactGroupMembershipOutputSet = true
			fakeProv.DeleteContactGroupMembershipOutput = emailprovider.DeleteContactGroupMembershipOutput{Deleted: false}

			obj := membership.DeepCopy()
			obj.Status.ProviderID = testMembershipProviderID
			res, err := finalizer.Finalize(ctx, obj)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(res).To(gomega.Equal(finalizerpkg.Result{}))
			gomega.Expect(fakeProv.DeleteContactGroupMembershipInputs).To(gomega.HaveLen(1))
		})

		ginkgo.It("completes when the provider IDs were never set (nothing to delete)", func() {
			// The referenced contact exists but was never synced to the
			// provider (no Status.ProviderID), so the finalizer must complete
			// without calling the provider.
			sch := scheme.Scheme
			gomega.Expect(notificationv1.AddToScheme(sch)).To(gomega.Succeed())
			notSyncedContact := contact.DeepCopy()
			notSyncedContact.Status.ProviderID = ""
			k8sClientFinal = fake.NewClientBuilder().
				WithScheme(sch).
				WithObjects(notSyncedContact, contactGroup.DeepCopy(), membership.DeepCopy()).
				Build()
			finalizer = &contactGroupMembershipFinalizer{Client: k8sClientFinal, EmailProvider: *svc}

			obj := membership.DeepCopy()
			obj.Status.ProviderID = testMembershipProviderID
			res, err := finalizer.Finalize(ctx, obj)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(res).To(gomega.Equal(finalizerpkg.Result{}))
			gomega.Expect(fakeProv.DeleteContactGroupMembershipInputs).To(gomega.BeEmpty())
		})
	})
})
