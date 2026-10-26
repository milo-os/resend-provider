package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"

	ginko "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"

	iammiloapiscomv1alpha1 "go.miloapis.com/milo/pkg/apis/iam/v1alpha1"
	notificationmiloapiscomv1alpha1 "go.miloapis.com/milo/pkg/apis/notification/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"go.miloapis.com/email-provider-resend/internal/config"
	"go.miloapis.com/email-provider-resend/internal/emailprovider"
)

var _ = ginko.Describe("EmailController idempotent sends", func() {
	ginko.It("sends the retry after a failed status write with the same Idempotency-Key", func() {
		ctx := context.Background()

		// A fake Resend API that records each send's Idempotency-Key.
		var (
			mu   sync.Mutex
			keys []string
		)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/emails" {
				mu.Lock()
				keys = append(keys, r.Header.Get("Idempotency-Key"))
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"delivery-1"}`))
				return
			}
			http.NotFound(w, r)
		}))
		defer srv.Close()
		base, err := url.Parse(srv.URL + "/")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		email := &notificationmiloapiscomv1alpha1.Email{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "retry-email", UID: types.UID("uid-retry")},
			Spec: notificationmiloapiscomv1alpha1.EmailSpec{
				TemplateRef: notificationmiloapiscomv1alpha1.TemplateReference{Name: "retry-template"},
				Recipient: notificationmiloapiscomv1alpha1.EmailRecipient{
					UserRef: notificationmiloapiscomv1alpha1.EmailUserReference{Name: "retry-user"},
				},
				Priority: notificationmiloapiscomv1alpha1.EmailPriorityNormal,
			},
		}
		template := &notificationmiloapiscomv1alpha1.EmailTemplate{
			ObjectMeta: metav1.ObjectMeta{Name: "retry-template"},
			Spec:       notificationmiloapiscomv1alpha1.EmailTemplateSpec{Subject: "Welcome"},
		}
		user := &iammiloapiscomv1alpha1.User{
			ObjectMeta: metav1.ObjectMeta{Name: "retry-user"},
			Spec:       iammiloapiscomv1alpha1.UserSpec{Email: "recipient@example.com"},
		}

		sch := scheme.Scheme
		gomega.Expect(iammiloapiscomv1alpha1.AddToScheme(sch)).To(gomega.Succeed())
		gomega.Expect(notificationmiloapiscomv1alpha1.AddToScheme(sch)).To(gomega.Succeed())

		// The first status write fails, as it would if Milo were unavailable
		// right after Resend accepted the email.
		failedOnce := false
		k8sClient := fake.NewClientBuilder().
			WithScheme(sch).
			WithStatusSubresource(&notificationmiloapiscomv1alpha1.Email{}).
			WithObjects(email, template, user).
			WithInterceptorFuncs(interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					if !failedOnce {
						failedOnce = true
						return errors.New("milo unavailable")
					}
					return c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
				},
			}).
			Build()

		conf, err := config.NewEmailControllerConfig(time.Second, time.Second, time.Second)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		service := emailprovider.NewService(emailprovider.NewResendEmailProviderWithBaseURL("re_test", base), "from@example.com", "reply@example.com")
		controller := &EmailController{Client: k8sClient, APIReader: k8sClient, EmailProvider: *service, Config: *conf}

		req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "retry-email"}}
		_, err = controller.Reconcile(ctx, req)
		gomega.Expect(err).To(gomega.HaveOccurred()) // the status write failed
		_, err = controller.Reconcile(ctx, req)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		mu.Lock()
		defer mu.Unlock()
		gomega.Expect(keys).To(gomega.Equal([]string{"uid-retry", "uid-retry"}),
			"both sends must carry the Email's UID as Idempotency-Key, so Resend returns the original email instead of sending it again")

		fetched := &notificationmiloapiscomv1alpha1.Email{}
		gomega.Expect(k8sClient.Get(ctx, req.NamespacedName, fetched)).To(gomega.Succeed())
		gomega.Expect(fetched.Status.ProviderID).To(gomega.Equal("delivery-1"))
	})
})
