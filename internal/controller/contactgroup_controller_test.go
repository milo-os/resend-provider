package controller

import (
	"context"

	ginkgo "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
	notificationv1 "go.miloapis.com/milo/pkg/apis/notification/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	finalizerpkg "sigs.k8s.io/controller-runtime/pkg/finalizer"

	"go.miloapis.com/email-provider-resend/internal/emailprovider"
	"go.miloapis.com/email-provider-resend/internal/emailprovider/mockprovider"
)

var _ = ginkgo.Describe("ContactGroupController", func() {
	var (
		ctx        context.Context
		k8sClient  client.Client
		controller *ContactGroupController
		group      *notificationv1.ContactGroup
		provider   *mockprovider.MockEmailProvider
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		provider = &mockprovider.MockEmailProvider{}
		svc := emailprovider.NewService(provider, "from@example.com", "reply@example.com")

		group = &notificationv1.ContactGroup{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "dev-team",
			},
			Spec: notificationv1.ContactGroupSpec{
				DisplayName: "Developers",
			},
		}

		sch := scheme.Scheme
		gomega.Expect(notificationv1.AddToScheme(sch)).To(gomega.Succeed())

		k8sClient = fake.NewClientBuilder().
			WithScheme(sch).
			WithStatusSubresource(&notificationv1.ContactGroup{}).
			WithObjects(group.DeepCopy()).
			Build()

		controller = &ContactGroupController{Client: k8sClient, EmailProvider: *svc}
		controller.Finalizers = finalizerpkg.NewFinalizers()
	})

	ginkgo.Context("when the contact group is created for the first time", func() {
		ginkgo.It("sets the Ready condition and providerID", func() {
			// Stub provider so that ListContactGroups returns empty, triggering CreateContactGroup.
			provider.ListContactGroupsOutput = emailprovider.ListContactGroupsOutput{}
			provider.CreateContactGroupOutput = emailprovider.CreateContactGroupOutput{ContactGroupID: "cg-123"}

			_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: group.Name, Namespace: group.Namespace}})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			fetched := &notificationv1.ContactGroup{}
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: group.Name, Namespace: group.Namespace}, fetched)).To(gomega.Succeed())

			cond := meta.FindStatusCondition(fetched.Status.Conditions, notificationv1.ContactGroupReadyCondition)
			gomega.Expect(cond).NotTo(gomega.BeNil())
			gomega.Expect(cond.Reason).To(gomega.Equal(notificationv1.ContactGroupCreatedReason))
			gomega.Expect(fetched.Status.ProviderID).To(gomega.Equal("cg-123"))
			gomega.Expect(provider.CreatedGroups).To(gomega.HaveLen(1))
		})
	})

	ginkgo.Context("when the contact group is updated", func() {
		ginkgo.It("sets the Updated condition", func() {
			// First reconcile to create it
			provider.ListContactGroupsOutput = emailprovider.ListContactGroupsOutput{}
			provider.CreateContactGroupOutput = emailprovider.CreateContactGroupOutput{ContactGroupID: "cg-123"}
			_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: group.Name, Namespace: group.Namespace}})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// Simulate spec update and generation bump
			fetched := &notificationv1.ContactGroup{}
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: group.Name, Namespace: group.Namespace}, fetched)).To(gomega.Succeed())
			fetched.Spec.DisplayName = "Devs"
			gomega.Expect(k8sClient.Update(ctx, fetched)).To(gomega.Succeed())
			fetched.Generation = 2
			gomega.Expect(k8sClient.Update(ctx, fetched)).To(gomega.Succeed())

			_, err = controller.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: group.Name, Namespace: group.Namespace}})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: group.Name, Namespace: group.Namespace}, fetched)).To(gomega.Succeed())
			cond := meta.FindStatusCondition(fetched.Status.Conditions, notificationv1.ContactGroupUpdatedCondition)
			gomega.Expect(cond).NotTo(gomega.BeNil())
			gomega.Expect(cond.ObservedGeneration).To(gomega.Equal(int64(2)))
		})
	})
})

var _ = ginkgo.Describe("contactGroupFinalizer", func() {
	var (
		ctx       context.Context
		k8sClient client.Client
		finalizer *contactGroupFinalizer
		group     *notificationv1.ContactGroup
		cgm       *notificationv1.ContactGroupMembership
		provider  *mockprovider.MockEmailProvider
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		provider = &mockprovider.MockEmailProvider{}
		svc := emailprovider.NewService(provider, "from@example.com", "reply@example.com")

		group = &notificationv1.ContactGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "dev-team", Namespace: "default"},
			Status:     notificationv1.ContactGroupStatus{ProviderID: "cg-123"},
		}

		cgm = &notificationv1.ContactGroupMembership{
			ObjectMeta: metav1.ObjectMeta{Name: "member-1", Namespace: "default"},
			Spec: notificationv1.ContactGroupMembershipSpec{
				ContactRef:      notificationv1.ContactReference{Name: "john", Namespace: "default"},
				ContactGroupRef: notificationv1.ContactGroupReference{Name: group.Name, Namespace: group.Namespace},
			},
		}

		sch := scheme.Scheme
		gomega.Expect(notificationv1.AddToScheme(sch)).To(gomega.Succeed())

		k8sClient = fake.NewClientBuilder().
			WithScheme(sch).
			WithObjects(group.DeepCopy(), cgm.DeepCopy()).
			WithIndex(&notificationv1.ContactGroupMembership{}, contactGroupNamespacedIndexKey, func(raw client.Object) []string {
				c := raw.(*notificationv1.ContactGroupMembership)
				return []string{buildContactGroupNamespacedIndexKey(c.Spec.ContactGroupRef.Name, c.Spec.ContactGroupRef.Namespace)}
			}).
			WithIndex(&notificationv1.ContactGroupMembershipRemoval{}, contactGroupToCgmrNamespacedIndexKey, func(raw client.Object) []string {
				c := raw.(*notificationv1.ContactGroupMembershipRemoval)
				return []string{buildContactGroupNamespacedIndexKey(c.Spec.ContactGroupRef.Name, c.Spec.ContactGroupRef.Namespace)}
			}).
			Build()

		finalizer = &contactGroupFinalizer{Client: k8sClient, EmailProvider: *svc}
	})

	ginkgo.It("deletes associated membership and provider contact group", func() {
		res, err := finalizer.Finalize(ctx, group.DeepCopy())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(res).To(gomega.Equal(finalizerpkg.Result{}))

		list := &notificationv1.ContactGroupMembershipList{}
		gomega.Expect(k8sClient.List(ctx, list)).To(gomega.Succeed())
		gomega.Expect(list.Items).To(gomega.BeEmpty())
		gomega.Expect(provider.DeletedGroupID).To(gomega.Equal("cg-123"))
	})

	ginkgo.It("ignores memberships in other namespaces", func() {
		other := cgm.DeepCopy()
		other.Spec.ContactGroupRef.Namespace = "other-ns"

		sch := scheme.Scheme
		k8sClient = fake.NewClientBuilder().
			WithScheme(sch).
			WithObjects(group.DeepCopy(), other).
			WithIndex(&notificationv1.ContactGroupMembership{}, contactGroupNamespacedIndexKey, func(raw client.Object) []string {
				c := raw.(*notificationv1.ContactGroupMembership)
				return []string{buildContactGroupNamespacedIndexKey(c.Spec.ContactGroupRef.Name, c.Spec.ContactGroupRef.Namespace)}
			}).
			WithIndex(&notificationv1.ContactGroupMembershipRemoval{}, contactGroupToCgmrNamespacedIndexKey, func(raw client.Object) []string {
				c := raw.(*notificationv1.ContactGroupMembershipRemoval)
				return []string{buildContactGroupNamespacedIndexKey(c.Spec.ContactGroupRef.Name, c.Spec.ContactGroupRef.Namespace)}
			}).
			Build()
		finalizer = &contactGroupFinalizer{Client: k8sClient, EmailProvider: *emailprovider.NewService(provider, "from@example.com", "reply@example.com")}

		_, err := finalizer.Finalize(ctx, group.DeepCopy())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		list := &notificationv1.ContactGroupMembershipList{}
		gomega.Expect(k8sClient.List(ctx, list)).To(gomega.Succeed())
		gomega.Expect(list.Items).To(gomega.HaveLen(1)) // still present
	})

	ginkgo.It("completes when the provider reports the contact group already deleted", func() {
		// Provider returns Deleted:false with nil error (idempotent no-op):
		// the contact group was already absent on the provider, so the
		// finalizer must complete rather than failing.
		provider = &mockprovider.MockEmailProvider{DeleteContactGroupOutput: emailprovider.DeleteContactGroupOutput{ContactGroupID: "cg-123", Deleted: false}}
		finalizer = &contactGroupFinalizer{Client: k8sClient, EmailProvider: *emailprovider.NewService(provider, "from@example.com", "reply@example.com")}

		res, err := finalizer.Finalize(ctx, group.DeepCopy())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(res).To(gomega.Equal(finalizerpkg.Result{}))
		gomega.Expect(provider.DeletedGroupID).To(gomega.Equal("cg-123"))
	})
})
