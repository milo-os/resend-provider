package webhook

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
	notificationv1alpha1 "go.miloapis.com/milo/pkg/apis/notification/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"go.miloapis.com/email-provider-resend/internal/resend"
)

const (
	recipientAddress = "jane.doe@example.org"
	foreignSender    = "news@other.example.net"
	contactAddress   = "john.roe@example.org"
	contactGivenName = "Johnathan"
	contactFamily    = "Roeberts"
)

// capturingContext returns a context whose logger records every line, at
// every verbosity, without redaction, so the handlers' own fields are tested.
func capturingContext(out *strings.Builder) context.Context {
	return logf.IntoContext(context.Background(), funcr.New(func(prefix, args string) {
		out.WriteString(prefix + " " + args + "\n")
	}, funcr.Options{Verbosity: 10}))
}

func assertNotLogged(t *testing.T, out string, personal ...string) {
	t.Helper()
	if out == "" {
		t.Fatal("nothing was logged; the logger was not used")
	}
	for _, p := range personal {
		if strings.Contains(out, p) {
			t.Fatalf("%q was logged:\n%s", p, out)
		}
	}
}

func TestEmailWebhookDoesNotLogAddresses(t *testing.T) {
	email := newEmail("welcome", "provider-1")
	wh, _, _ := newWebhook(t, email)

	events := []*resend.ParsedEvent{
		// Tagged, from our sender: updates the Email.
		deliveredEvent("provider-1", resend.EmailRefTags(email.Namespace, email.Name)),
		// Untagged, from another sender: skipped.
		deliveredEventFrom("News <"+foreignSender+">", "provider-2", nil),
		// Untagged, from our sender, matching nothing: not found.
		deliveredEvent("provider-3", nil),
	}

	var out strings.Builder
	ctx := capturingContext(&out)
	for _, evt := range events {
		evt.Base.To = []string{recipientAddress}
		evt.Base.Subject = "Welcome " + recipientAddress
		wh.Handler.Handle(ctx, Request{EmailEvent: evt})
	}

	assertNotLogged(t, out.String(), recipientAddress, foreignSender, testSender)
}

func TestContactWebhookDoesNotLogContactDetails(t *testing.T) {
	contact := &notificationv1alpha1.Contact{
		ObjectMeta: metav1.ObjectMeta{Name: "user-1", Namespace: "default"},
		Spec: notificationv1alpha1.ContactSpec{
			Email: contactAddress, GivenName: contactGivenName, FamilyName: contactFamily,
		},
		Status: notificationv1alpha1.ContactStatus{ProviderID: "contact-1"},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(buildContactScheme(t)).
		WithStatusSubresource(&notificationv1alpha1.Contact{}).
		WithIndex(&notificationv1alpha1.Contact{}, contactStatusProviderIDIndexKey, contactProviderIDIndex).
		WithObjects(contact).Build()
	wh := NewResendContactWebhookV1(k8sClient)

	var out strings.Builder
	ctx := capturingContext(&out)
	for _, id := range []string{"contact-1", "contact-unknown"} {
		wh.Handler.Handle(ctx, Request{ContactEvent: &resend.ParsedContactEvent{
			Envelope: resend.ContactEventEnvelope{Type: resend.ContactUpdated},
			Contact: resend.ContactBase{
				ID: id, Email: contactAddress, FirstName: contactGivenName, LastName: contactFamily,
			},
		}})
	}

	assertNotLogged(t, out.String(), contactAddress, contactGivenName, contactFamily)
}
