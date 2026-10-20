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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	finalizerpkg "sigs.k8s.io/controller-runtime/pkg/finalizer"
)

// The membership's overall Ready waits for every provider that serves its group: Resend serves every group, and
// Loops only the groups that list it.
var _ = ginkgo.Describe("ContactGroupMembership overall Ready", func() {
	reconcileReady := func(providers []notificationv1.ContactGroupProviderSpec, conds []metav1.Condition) *metav1.Condition {
		ctx := context.Background()
		contact := &notificationv1.Contact{
			ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "default"},
			Spec:       notificationv1.ContactSpec{GivenName: "Alice", FamilyName: "Doe", Email: "alice@example.com"},
		}
		group := &notificationv1.ContactGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "devs", Namespace: "default"},
			Spec:       notificationv1.ContactGroupSpec{DisplayName: "Developers", Providers: providers},
			Status:     notificationv1.ContactGroupStatus{ProviderID: "cg-1"},
		}
		membership := &notificationv1.ContactGroupMembership{
			ObjectMeta: metav1.ObjectMeta{Name: "m-1", Namespace: "default"},
			Spec: notificationv1.ContactGroupMembershipSpec{
				ContactRef:      notificationv1.ContactReference{Name: "alice", Namespace: "default"},
				ContactGroupRef: notificationv1.ContactGroupReference{Name: "devs", Namespace: "default"},
			},
			Status: notificationv1.ContactGroupMembershipStatus{Conditions: conds},
		}
		sch := scheme.Scheme
		gomega.Expect(notificationv1.AddToScheme(sch)).To(gomega.Succeed())
		c := fake.NewClientBuilder().WithScheme(sch).
			WithStatusSubresource(&notificationv1.ContactGroupMembership{}).
			WithObjects(contact, group, membership).Build()
		prov := &mockprovider.MockEmailProvider{
			CreateContactGroupMembershipOutput: emailprovider.CreateContactGroupMembershipOutput{ContactGroupMembershipID: "cgm-1"},
		}
		r := &ContactGroupMembershipController{Client: c, EmailProvider: *emailprovider.NewService(prov, "from@example.com", "reply@example.com")}
		r.Finalizers = finalizerpkg.NewFinalizers()

		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "m-1", Namespace: "default"}})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fetched := &notificationv1.ContactGroupMembership{}
		gomega.Expect(c.Get(ctx, types.NamespacedName{Name: "m-1", Namespace: "default"}, fetched)).To(gomega.Succeed())
		return meta.FindStatusCondition(fetched.Status.Conditions, notificationv1.ContactGroupMembershipReadyCondition)
	}
	loopsReady := metav1.Condition{
		Type: LoopsContactGroupMembershipReadyCondition, Status: metav1.ConditionTrue,
		Reason: "ContactGroupMembershipCreated", LastTransitionTime: metav1.Now(),
	}

	// The ContactGroup API lists only the providers that need an existing list ID: its provider name admits Loops
	// alone, and Resend serves every group without being listed.
	ginkgo.It("requires Resend for every group and Loops only for a group that lists it", func() {
		names := func(ps []readinessProvider) (out []string) {
			for _, p := range ps {
				out = append(out, p.name)
			}
			return out
		}
		unlisted := &notificationv1.ContactGroup{}
		listed := &notificationv1.ContactGroup{Spec: notificationv1.ContactGroupSpec{
			Providers: []notificationv1.ContactGroupProviderSpec{{Name: "Loops", ID: "list-1"}},
		}}
		gomega.Expect(names(requiredProviders(unlisted))).To(gomega.Equal([]string{"Resend"}))
		gomega.Expect(names(requiredProviders(listed))).To(gomega.Equal([]string{"Loops", "Resend"}))
	})

	ginkgo.It("is ready on Resend alone for a group that does not list Loops", func() {
		ready := reconcileReady(nil, nil)
		gomega.Expect(ready).NotTo(gomega.BeNil())
		gomega.Expect(ready.Status).To(gomega.Equal(metav1.ConditionTrue), "Ready: %s", ready.Message)
	})

	ginkgo.It("waits for Loops for a group that lists Loops", func() {
		ready := reconcileReady([]notificationv1.ContactGroupProviderSpec{{Name: "Loops", ID: "list-1"}}, nil)
		gomega.Expect(ready).NotTo(gomega.BeNil())
		gomega.Expect(ready.Status).To(gomega.Equal(metav1.ConditionFalse))
		gomega.Expect(ready.Message).To(gomega.ContainSubstring("Loops"))
	})

	ginkgo.It("is not ready while Loops reports a failure for a group that lists Loops", func() {
		loopsFailed := loopsReady
		loopsFailed.Status, loopsFailed.Reason = metav1.ConditionFalse, "ContactGroupMembershipNotCreated"
		ready := reconcileReady([]notificationv1.ContactGroupProviderSpec{{Name: "Loops", ID: "list-1"}}, []metav1.Condition{loopsFailed})
		gomega.Expect(ready).NotTo(gomega.BeNil())
		gomega.Expect(ready.Status).To(gomega.Equal(metav1.ConditionFalse))
		gomega.Expect(ready.Message).To(gomega.ContainSubstring("ContactGroupMembershipNotCreated"))
	})

	ginkgo.It("is ready when Loops is ready too for a group that lists Loops", func() {
		ready := reconcileReady([]notificationv1.ContactGroupProviderSpec{{Name: "Loops", ID: "list-1"}}, []metav1.Condition{loopsReady})
		gomega.Expect(ready).NotTo(gomega.BeNil())
		gomega.Expect(ready.Status).To(gomega.Equal(metav1.ConditionTrue), "Ready: %s", ready.Message)
	})
})
