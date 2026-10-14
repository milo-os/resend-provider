package webhook

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"go.miloapis.com/email-provider-resend/internal/resend"
	notificationv1alpha1 "go.miloapis.com/milo/pkg/apis/notification/v1alpha1"
	crtclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func buildScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := notificationv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add notification scheme: %v", err)
	}
	if err := eventsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add events scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add corev1 scheme: %v", err)
	}
	return scheme
}

// pagingReader paginates Email lists the way the API server does, which the
// fake client does not, and counts the pages it serves.
type pagingReader struct {
	crtclient.Reader
	pages int
	// failList makes every List fail the test.
	failList *testing.T
}

func (r *pagingReader) List(ctx context.Context, list crtclient.ObjectList, opts ...crtclient.ListOption) error {
	if r.failList != nil {
		r.failList.Fatalf("List must not be called")
	}
	emails, ok := list.(*notificationv1alpha1.EmailList)
	if !ok {
		return r.Reader.List(ctx, list, opts...)
	}
	listOpts := (&crtclient.ListOptions{}).ApplyOptions(opts)
	all := &notificationv1alpha1.EmailList{}
	if err := r.Reader.List(ctx, all); err != nil {
		return err
	}
	start := 0
	if listOpts.Continue != "" {
		var err error
		if start, err = strconv.Atoi(listOpts.Continue); err != nil {
			return err
		}
	}
	end := len(all.Items)
	if listOpts.Limit > 0 && start+int(listOpts.Limit) < end {
		end = start + int(listOpts.Limit)
	}
	emails.Items = all.Items[start:end]
	emails.Continue = ""
	if end < len(all.Items) {
		emails.Continue = strconv.Itoa(end)
	}
	r.pages++
	return nil
}

func newEmail(name, providerID string) *notificationv1alpha1.Email {
	return &notificationv1alpha1.Email{
		ObjectMeta: metav1.ObjectMeta{Namespace: "milo-system", Name: name},
		Status: notificationv1alpha1.EmailStatus{
			ProviderID: providerID,
			HTMLBody:   "<p>html body of " + name + "</p>",
			TextBody:   "text body of " + name,
		},
	}
}

const testSender = "support@transactional.example.com"

func newWebhook(t *testing.T, emails ...*notificationv1alpha1.Email) (*Webhook, crtclient.Client, *pagingReader) {
	t.Helper()
	return newWebhookWithSender(t, testSender, emails...)
}

func newWebhookWithSender(
	t *testing.T, sender string, emails ...*notificationv1alpha1.Email,
) (*Webhook, crtclient.Client, *pagingReader) {
	t.Helper()
	builder := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&notificationv1alpha1.Email{})
	for _, e := range emails {
		builder = builder.WithObjects(e)
	}
	k8sClient := builder.Build()
	reader := &pagingReader{Reader: k8sClient}
	return NewResendEmailWebhookV1(k8sClient, reader, sender), k8sClient, reader
}

func deliveredEvent(providerID string, tags resend.Tags) *resend.ParsedEvent {
	return deliveredEventFrom("Datum <"+testSender+">", providerID, tags)
}

func deliveredEventFrom(from, providerID string, tags resend.Tags) *resend.ParsedEvent {
	return &resend.ParsedEvent{
		Envelope: resend.EventEnvelope{Type: resend.EventTypeDelivered},
		Base:     resend.EmailBase{EmailID: providerID, From: from, Tags: tags},
	}
}

func getEmail(t *testing.T, c crtclient.Client, namespace, name string) *notificationv1alpha1.Email {
	t.Helper()
	email := &notificationv1alpha1.Email{}
	if err := c.Get(context.TODO(), crtclient.ObjectKey{Namespace: namespace, Name: name}, email); err != nil {
		t.Fatalf("failed to get email %s/%s: %v", namespace, name, err)
	}
	return email
}

func assertDelivered(t *testing.T, email *notificationv1alpha1.Email) {
	t.Helper()
	if !meta.IsStatusConditionTrue(email.Status.Conditions, notificationv1alpha1.EmailDeliveredCondition) {
		t.Fatalf("email %s: delivered condition not true: %+v", email.Name, email.Status.Conditions)
	}
	if email.Status.HTMLBody != "<p>html body of "+email.Name+"</p>" || email.Status.TextBody != "text body of "+email.Name {
		t.Fatalf("email %s: bodies not preserved: html=%q text=%q", email.Name, email.Status.HTMLBody, email.Status.TextBody)
	}
}

func assertUnchanged(t *testing.T, c crtclient.Client, want *notificationv1alpha1.Email) {
	t.Helper()
	got := getEmail(t, c, want.Namespace, want.Name)
	if len(got.Status.Conditions) != 0 {
		t.Fatalf("email %s should be unchanged, got conditions %+v", want.Name, got.Status.Conditions)
	}
}

func TestNewResendWebhookV1_Endpoint(t *testing.T) {
	wh := NewResendEmailWebhookV1(nil, nil, "")
	expected := "/apis/emailnotification.k8s.io/v1/resend/emails"
	if wh.Endpoint != expected {
		t.Fatalf("unexpected endpoint: got %s want %s", wh.Endpoint, expected)
	}
}

func TestNewResendWebhookV1_EmailNotFound(t *testing.T) {
	wh, _, _ := newWebhook(t)

	resp := wh.Handler.Handle(context.TODO(), Request{EmailEvent: deliveredEvent("provider-1", nil)})
	if resp.HttpStatus != http.StatusNotFound {
		t.Fatalf("expected %d got %d", http.StatusNotFound, resp.HttpStatus)
	}
}

func TestNewResendWebhookV1_TaggedEvent(t *testing.T) {
	// The name contains dots, which tags encode as underscores.
	target := newEmail("user.welcome.abc", "provider-xyz")
	other := newEmail("other", "provider-other")
	wh, k8sClient, reader := newWebhook(t, target, other)

	tags := resend.EmailRefTags(target.Namespace, target.Name)
	resp := wh.Handler.Handle(context.TODO(), Request{EmailEvent: deliveredEvent("provider-xyz", tags)})
	if resp.HttpStatus != http.StatusOK {
		t.Fatalf("expected %d got %d", http.StatusOK, resp.HttpStatus)
	}
	if reader.pages != 0 {
		t.Fatalf("tagged event should not list emails, listed %d pages", reader.pages)
	}

	assertDelivered(t, getEmail(t, k8sClient, target.Namespace, target.Name))
	assertUnchanged(t, k8sClient, other)

	evList := &eventsv1.EventList{}
	if err := k8sClient.List(context.TODO(), evList); err != nil {
		t.Fatalf("failed to list events: %v", err)
	}
	if len(evList.Items) != 1 || evList.Items[0].Regarding.Name != target.Name {
		t.Fatalf("expected 1 event regarding %s, got %+v", target.Name, evList.Items)
	}
}

// A tag that names an Email whose provider ID differs from the event's must
// be refused, not recorded on the named Email or on the Email that does have
// that provider ID.
func TestNewResendWebhookV1_TaggedEventProviderIDMismatchRefused(t *testing.T) {
	named := newEmail("named", "provider-named")
	owner := newEmail("owner", "provider-owner")
	wh, k8sClient, reader := newWebhook(t, named, owner)

	tags := resend.EmailRefTags(named.Namespace, named.Name)
	resp := wh.Handler.Handle(context.TODO(), Request{EmailEvent: deliveredEvent("provider-owner", tags)})
	if resp.HttpStatus != http.StatusNotFound {
		t.Fatalf("expected %d got %d", http.StatusNotFound, resp.HttpStatus)
	}
	if reader.pages != 0 {
		t.Fatalf("refused tagged event should not fall back to listing, listed %d pages", reader.pages)
	}
	assertUnchanged(t, k8sClient, named)
	assertUnchanged(t, k8sClient, owner)
}

func TestNewResendWebhookV1_TaggedEmailMissing(t *testing.T) {
	wh, _, reader := newWebhook(t, newEmail("other", "provider-xyz"))

	tags := resend.EmailRefTags("milo-system", "deleted")
	resp := wh.Handler.Handle(context.TODO(), Request{EmailEvent: deliveredEvent("provider-xyz", tags)})
	if resp.HttpStatus != http.StatusNotFound {
		t.Fatalf("expected %d got %d", http.StatusNotFound, resp.HttpStatus)
	}
	if reader.pages != 0 {
		t.Fatalf("tagged event should not list emails, listed %d pages", reader.pages)
	}
}

// Events for Emails sent before tagging was introduced carry no tags and are
// found by searching every page until the provider ID matches.
func TestNewResendWebhookV1_UntaggedEventFallsBackToPagedSearch(t *testing.T) {
	emails := make([]*notificationv1alpha1.Email, 0, 2*emailScanPageSize+1)
	for i := range 2*emailScanPageSize + 1 {
		emails = append(emails, newEmail(fmt.Sprintf("email-%03d", i), fmt.Sprintf("provider-%03d", i)))
	}
	target := emails[emailScanPageSize+1] // on the second page
	wh, k8sClient, reader := newWebhook(t, emails...)

	resp := wh.Handler.Handle(context.TODO(), Request{EmailEvent: deliveredEvent(target.Status.ProviderID, nil)})
	if resp.HttpStatus != http.StatusOK {
		t.Fatalf("expected %d got %d", http.StatusOK, resp.HttpStatus)
	}
	if reader.pages != 2 {
		t.Fatalf("expected search to stop on page 2, listed %d pages", reader.pages)
	}

	assertDelivered(t, getEmail(t, k8sClient, target.Namespace, target.Name))
	assertUnchanged(t, k8sClient, emails[0])
	assertUnchanged(t, k8sClient, emails[len(emails)-1])
}

// Untagged events for mail another sender sent through the same Resend
// account have no Email; they are acknowledged without searching.
func TestNewResendWebhookV1_UntaggedForeignSenderNotSearched(t *testing.T) {
	email := newEmail("email-1", "provider-1")
	wh, k8sClient, reader := newWebhook(t, email)
	reader.failList = t

	for _, from := range []string{"Other <noreply@other.example.com>", "noreply@other.example.com", "not an address"} {
		resp := wh.Handler.Handle(context.TODO(), Request{EmailEvent: deliveredEventFrom(from, "provider-1", nil)})
		if resp.HttpStatus != http.StatusOK {
			t.Fatalf("from %q: expected %d got %d", from, http.StatusOK, resp.HttpStatus)
		}
	}
	assertUnchanged(t, k8sClient, email)
}

func TestNewResendWebhookV1_UntaggedOurSenderSearched(t *testing.T) {
	for _, from := range []string{
		testSender,
		"Datum <" + testSender + ">",
		"SUPPORT@Transactional.Example.com",
		`"Datum Support" <Support@transactional.example.COM>`,
	} {
		t.Run(from, func(t *testing.T) {
			email := newEmail("email-1", "provider-1")
			wh, k8sClient, reader := newWebhook(t, email)

			resp := wh.Handler.Handle(context.TODO(), Request{EmailEvent: deliveredEventFrom(from, "provider-1", nil)})
			if resp.HttpStatus != http.StatusOK {
				t.Fatalf("expected %d got %d", http.StatusOK, resp.HttpStatus)
			}
			if reader.pages == 0 {
				t.Fatalf("expected a search")
			}
			assertDelivered(t, getEmail(t, k8sClient, email.Namespace, email.Name))
		})
	}
}

// Without a configured sender, untagged events from any sender are searched.
func TestNewResendWebhookV1_UntaggedNoSenderConfiguredSearched(t *testing.T) {
	email := newEmail("email-1", "provider-1")
	wh, k8sClient, reader := newWebhookWithSender(t, "", email)

	resp := wh.Handler.Handle(context.TODO(),
		Request{EmailEvent: deliveredEventFrom("Other <noreply@other.example.com>", "provider-1", nil)})
	if resp.HttpStatus != http.StatusOK {
		t.Fatalf("expected %d got %d", http.StatusOK, resp.HttpStatus)
	}
	if reader.pages == 0 {
		t.Fatalf("expected a search")
	}
	assertDelivered(t, getEmail(t, k8sClient, email.Namespace, email.Name))
}

// Tags identify our Email regardless of the sender.
func TestNewResendWebhookV1_TaggedForeignSenderStillHandled(t *testing.T) {
	email := newEmail("email-1", "provider-1")
	wh, k8sClient, reader := newWebhook(t, email)
	reader.failList = t

	tags := resend.EmailRefTags(email.Namespace, email.Name)
	resp := wh.Handler.Handle(context.TODO(),
		Request{EmailEvent: deliveredEventFrom("noreply@other.example.com", "provider-1", tags)})
	if resp.HttpStatus != http.StatusOK {
		t.Fatalf("expected %d got %d", http.StatusOK, resp.HttpStatus)
	}
	assertDelivered(t, getEmail(t, k8sClient, email.Namespace, email.Name))
}

const orderedProviderID = "provider-1"

// taggedEvent is a Resend event for email's send, which has provider ID
// orderedProviderID.
func taggedEvent(eventType resend.EmailEventType, email *notificationv1alpha1.Email) *resend.ParsedEvent {
	return &resend.ParsedEvent{
		Envelope: resend.EventEnvelope{Type: eventType},
		Base: resend.EmailBase{
			EmailID: orderedProviderID,
			From:    "Datum <" + testSender + ">",
			Tags:    resend.EmailRefTags(email.Namespace, email.Name),
		},
	}
}

func handle(t *testing.T, wh *Webhook, event *resend.ParsedEvent, want int) {
	t.Helper()
	if resp := wh.Handler.Handle(context.TODO(), Request{EmailEvent: event}); resp.HttpStatus != want {
		t.Fatalf("%s: expected %d got %d", event.Envelope.Type, want, resp.HttpStatus)
	}
}

func assertDeliveredCondition(t *testing.T, email *notificationv1alpha1.Email, status metav1.ConditionStatus, reason string) {
	t.Helper()
	cond := meta.FindStatusCondition(email.Status.Conditions, notificationv1alpha1.EmailDeliveredCondition)
	if cond == nil || cond.Status != status || cond.Reason != reason {
		t.Fatalf("email %s: want Delivered=%s (%s), got %+v", email.Name, status, reason, cond)
	}
}

// Resend doesn't guarantee event order, so delivery status only moves forward:
// pending < delivered < failed.
func TestNewResendWebhookV1_DeliveryStatusOnlyMovesForward(t *testing.T) {
	const delivered, failed = notificationv1alpha1.EmailDeliveredReason, notificationv1alpha1.EmailDeliveryFailedReason
	tests := []struct {
		name       string
		events     []resend.EmailEventType
		wantStatus metav1.ConditionStatus
		wantReason string
	}{
		{"delivered then sent stays delivered",
			[]resend.EmailEventType{resend.EventTypeDelivered, resend.EventTypeSent},
			metav1.ConditionTrue, delivered},
		{"delivered then delayed stays delivered",
			[]resend.EmailEventType{resend.EventTypeDelivered, resend.EventTypeDeliveredDelayed},
			metav1.ConditionTrue, delivered},
		{"delivered then bounced records the bounce",
			[]resend.EmailEventType{resend.EventTypeDelivered, resend.EventTypeBounced},
			metav1.ConditionFalse, failed},
		{"bounced then delivered keeps the bounce",
			[]resend.EmailEventType{resend.EventTypeBounced, resend.EventTypeDelivered},
			metav1.ConditionFalse, failed},
		{"bounced then sent keeps the bounce",
			[]resend.EmailEventType{resend.EventTypeBounced, resend.EventTypeSent},
			metav1.ConditionFalse, failed},
		{"sent then delivered is delivered",
			[]resend.EmailEventType{resend.EventTypeSent, resend.EventTypeDelivered},
			metav1.ConditionTrue, delivered},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			email := newEmail("ordered", orderedProviderID)
			wh, k8sClient, _ := newWebhook(t, email)
			for _, e := range tt.events {
				handle(t, wh, taggedEvent(e, email), http.StatusOK)
			}
			got := getEmail(t, k8sClient, email.Namespace, email.Name)
			assertDeliveredCondition(t, got, tt.wantStatus, tt.wantReason)
			if got.Status.HTMLBody != "<p>html body of ordered</p>" {
				t.Fatalf("bodies not preserved: %q", got.Status.HTMLBody)
			}
		})
	}
}

// A late event that's ignored is still recorded as an Event.
func TestNewResendWebhookV1_LateEventRecorded(t *testing.T) {
	email := newEmail("late", orderedProviderID)
	wh, k8sClient, _ := newWebhook(t, email)
	handle(t, wh, taggedEvent(resend.EventTypeDelivered, email), http.StatusOK)
	handle(t, wh, taggedEvent(resend.EventTypeSent, email), http.StatusOK)

	evList := &eventsv1.EventList{}
	if err := k8sClient.List(context.TODO(), evList); err != nil {
		t.Fatalf("failed to list events: %v", err)
	}
	reasons := make([]string, 0, len(evList.Items))
	for _, ev := range evList.Items {
		reasons = append(reasons, ev.Reason)
	}
	if len(reasons) != 2 || !slices.Contains(reasons, lateEventIgnoredReason) {
		t.Fatalf("expected a delivery Event and a %s Event, got %v", lateEventIgnoredReason, reasons)
	}
}

// The sequence seen in production on 12 October 2026 (#38): Resend's first
// events arrive before the controller records the provider ID, are refused,
// and are retried in reverse order.
func TestNewResendWebhookV1_ProductionRetrySequenceEndsDelivered(t *testing.T) {
	email := newEmail("suspicious-login", "")
	wh, k8sClient, _ := newWebhook(t, email)

	handle(t, wh, taggedEvent(resend.EventTypeSent, email), http.StatusNotFound)
	handle(t, wh, taggedEvent(resend.EventTypeDelivered, email), http.StatusNotFound)

	// The controller records the provider ID after the send.
	sent := getEmail(t, k8sClient, email.Namespace, email.Name)
	sent.Status.ProviderID = orderedProviderID
	meta.SetStatusCondition(&sent.Status.Conditions, metav1.Condition{
		Type:   notificationv1alpha1.EmailDeliveredCondition,
		Status: metav1.ConditionUnknown,
		Reason: notificationv1alpha1.EmailDeliveryPendingReason,
	})
	if err := k8sClient.Status().Update(context.TODO(), sent); err != nil {
		t.Fatalf("failed to record provider ID: %v", err)
	}

	// Resend's retries arrive delivered first, then sent.
	handle(t, wh, taggedEvent(resend.EventTypeDelivered, email), http.StatusOK)
	handle(t, wh, taggedEvent(resend.EventTypeSent, email), http.StatusOK)

	assertDeliveredCondition(t, getEmail(t, k8sClient, email.Namespace, email.Name),
		metav1.ConditionTrue, notificationv1alpha1.EmailDeliveredReason)
}
