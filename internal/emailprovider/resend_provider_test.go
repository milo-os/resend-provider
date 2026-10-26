package emailprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// newTestProvider returns a provider that talks to a fake Resend API served by
// handler.
func newTestProvider(t *testing.T, handler http.HandlerFunc) *ResendEmailProvider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	return newResendEmailProvider("re_test", base)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestGetContactGroup_NotFoundStatusIsNotFound(t *testing.T) {
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{"statusCode": 404, "name": "not_found", "message": "Segment missing"})
	})

	_, err := p.GetContactGroup(context.Background(), GetContactGroupInput{ContactGroupID: "seg-1"})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected a NotFound error for a 404, got %v", err)
	}
}

func TestGetContactGroup_OtherStatusMentioningNotFoundIsNotNotFound(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusInternalServerError} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, status, map[string]any{"statusCode": status, "name": "error", "message": "Contact not found in audience"})
			})

			_, err := p.GetContactGroup(context.Background(), GetContactGroupInput{ContactGroupID: "seg-1"})
			if err == nil {
				t.Fatal("expected an error")
			}
			if apierrors.IsNotFound(err) {
				t.Fatalf("a %d response must not be treated as NotFound even though its message says \"not found\": %v", status, err)
			}
		})
	}
}

func TestDeleteContact_NotFoundStatusIsNotFound(t *testing.T) {
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{"statusCode": 404, "name": "not_found", "message": "Contact missing"})
	})

	_, err := p.DeleteContact(context.Background(), DeleteContactInput{ContactId: "c-1"})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected a NotFound error for a 404, got %v", err)
	}
}

func TestListContactGroups_FollowsPagination(t *testing.T) {
	const total = contactGroupsPageSize + 3
	var requests atomic.Int32
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/segments" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("limit"); got != strconv.Itoa(contactGroupsPageSize) {
			t.Errorf("expected limit=%d, got %q", contactGroupsPageSize, got)
		}

		start := 0
		if after := r.URL.Query().Get("after"); after != "" {
			n, err := strconv.Atoi(after[len("seg-"):])
			if err != nil {
				t.Errorf("unexpected after %q", after)
			}
			start = n + 1
		}
		end := min(start+contactGroupsPageSize, total)
		data := []map[string]string{}
		for i := start; i < end; i++ {
			data = append(data, map[string]string{"id": fmt.Sprintf("seg-%d", i), "name": fmt.Sprintf("group-%d", i), "object": "segment"})
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data, "has_more": end < total})
	})

	out, err := p.ListContactGroups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.ContactGroups) != total {
		t.Fatalf("expected %d contact groups across pages, got %d", total, len(out.ContactGroups))
	}
	if last := out.ContactGroups[total-1]; last.ContactGroupID != fmt.Sprintf("seg-%d", total-1) || last.DisplayName != fmt.Sprintf("group-%d", total-1) {
		t.Fatalf("unexpected last contact group %+v", last)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("expected 2 list requests, got %d", got)
	}
}

func TestGetContactGroup_UsesContext(t *testing.T) {
	var requests atomic.Int32
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"id": "seg-1", "name": "group", "object": "segment"})
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.GetContactGroup(ctx, GetContactGroupInput{ContactGroupID: "seg-1"}); err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("expected no request with a cancelled context, got %d", got)
	}
}

// requestLog records the requests a fake Resend API receives.
type requestLog struct {
	mu       sync.Mutex
	requests []loggedRequest
}

type loggedRequest struct {
	method, path string
	header       http.Header
	body         []byte
}

// wrap returns handler, recording each request before it is answered.
func (l *requestLog) wrap(t *testing.T, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		l.mu.Lock()
		l.requests = append(l.requests, loggedRequest{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: body})
		l.mu.Unlock()
		handler(w, r)
	}
}

// posts counts the POST requests to path.
func (l *requestLog) posts(path string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, r := range l.requests {
		if r.method == http.MethodPost && r.path == path {
			n++
		}
	}
	return n
}

func TestSendEmail_SendsIdempotencyKeyAsRequestHeader(t *testing.T) {
	var log requestLog
	p := newTestProvider(t, log.wrap(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"id": "email-1"})
	}))

	out, err := p.SendEmail(context.Background(), SendEmailInput{
		From: "from@example.com", To: []string{"to@example.com"}, Subject: "s",
		HtmlBody: "<p>h</p>", IdempotencyKey: "uid-123",
	})
	if err != nil {
		t.Fatalf("SendEmail: %v", err)
	}
	if out.DeliveryID != "email-1" {
		t.Fatalf("DeliveryID = %q, want email-1", out.DeliveryID)
	}
	if len(log.requests) != 1 {
		t.Fatalf("got %d requests, want 1", len(log.requests))
	}
	req := log.requests[0]
	if got := req.header.Get("Idempotency-Key"); got != "uid-123" {
		t.Errorf("Idempotency-Key header = %q, want uid-123", got)
	}
	var body map[string]any
	if err := json.Unmarshal(req.body, &body); err != nil {
		t.Fatalf("decoding request body: %v", err)
	}
	if headers, ok := body["headers"]; ok {
		t.Errorf("request body sets email headers %v; the idempotency key must not be added to the delivered email", headers)
	}
	if strings.Contains(string(req.body), "uid-123") {
		t.Errorf("request body contains the idempotency key: %s", req.body)
	}
}

func TestCreateContact_ReturnsExistingContact(t *testing.T) {
	var log requestLog
	p := newTestProvider(t, log.wrap(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/contacts/a@example.com" {
			writeJSON(w, http.StatusOK, map[string]string{"id": "contact-existing", "email": "a@example.com"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"id": "contact-new"})
	}))

	out, err := p.CreateContact(context.Background(), CreateContactInput{Email: "a@example.com"})
	if err != nil {
		t.Fatalf("CreateContact: %v", err)
	}
	if out.ContactId != "contact-existing" {
		t.Errorf("ContactId = %q, want contact-existing", out.ContactId)
	}
	if n := log.posts("/contacts"); n != 0 {
		t.Errorf("created the contact %d times; it already existed", n)
	}
}

// A real 404 from the lookup, whose message doesn't say "not found", must still
// lead to a create: missing contacts are detected by status code.
func TestCreateContact_CreatesAfterLookup404(t *testing.T) {
	var log requestLog
	p := newTestProvider(t, log.wrap(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/contacts" {
			writeJSON(w, http.StatusOK, map[string]string{"id": "contact-new"})
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"statusCode": 404, "name": "not_found", "message": "Contact missing"})
	}))

	out, err := p.CreateContact(context.Background(), CreateContactInput{Email: "a@example.com", GivenName: "A"})
	if err != nil {
		t.Fatalf("CreateContact: %v", err)
	}
	if out.ContactId != "contact-new" {
		t.Errorf("ContactId = %q, want contact-new", out.ContactId)
	}
	if n := log.posts("/contacts"); n != 1 {
		t.Errorf("created the contact %d times, want 1", n)
	}
}

func TestCreateContact_LookupFailureDoesNotCreate(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusInternalServerError} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var log requestLog
			p := newTestProvider(t, log.wrap(t, func(w http.ResponseWriter, r *http.Request) {
				// The message mentions "not found", but only a 404 means missing.
				writeJSON(w, status, map[string]any{"statusCode": status, "name": "error", "message": "Contact not found in audience"})
			}))

			if _, err := p.CreateContact(context.Background(), CreateContactInput{Email: "a@example.com"}); err == nil {
				t.Fatal("CreateContact succeeded; want the lookup error")
			}
			if n := log.posts("/contacts"); n != 0 {
				t.Errorf("created the contact %d times after a failed lookup; it may already exist", n)
			}
		})
	}
}

// segmentsHandler serves contact-1's segments in pages and accepts adds to
// seg-target.
func segmentsHandler(pages [][]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/contacts/contact-1/segments":
			page := 0
			if after := r.URL.Query().Get("after"); after != "" {
				for i, p := range pages {
					if len(p) > 0 && p[len(p)-1] == after {
						page = i + 1
					}
				}
			}
			data := []map[string]string{}
			if page < len(pages) {
				for _, id := range pages[page] {
					data = append(data, map[string]string{"id": id, "object": "segment"})
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data, "has_more": page < len(pages)-1})
		case r.Method == http.MethodPost && r.URL.Path == "/contacts/contact-1/segments/seg-target":
			writeJSON(w, http.StatusOK, map[string]string{"id": "seg-target"})
		default:
			writeJSON(w, http.StatusNotFound, map[string]any{"statusCode": 404, "message": "Not found"})
		}
	}
}

func TestCreateContactGroupMembership_SkipsExistingMembership(t *testing.T) {
	// The target segment is on the second page, so the lookup must paginate.
	var log requestLog
	p := newTestProvider(t, log.wrap(t, segmentsHandler([][]string{{"seg-a", "seg-b"}, {"seg-target"}})))

	out, err := p.CreateContactGroupMembership(context.Background(), CreateContactGroupMembershipInput{
		ContactId: "contact-1", ContactGroupId: "seg-target",
	})
	if err != nil {
		t.Fatalf("CreateContactGroupMembership: %v", err)
	}
	if out.ContactGroupMembershipID != "seg-target" {
		t.Errorf("ContactGroupMembershipID = %q, want seg-target", out.ContactGroupMembershipID)
	}
	if n := log.posts("/contacts/contact-1/segments/seg-target"); n != 0 {
		t.Errorf("added the contact to the segment %d times; it was already a member", n)
	}
}

func TestCreateContactGroupMembership_AddsWhenNotMember(t *testing.T) {
	var log requestLog
	p := newTestProvider(t, log.wrap(t, segmentsHandler([][]string{{"seg-a"}})))

	out, err := p.CreateContactGroupMembership(context.Background(), CreateContactGroupMembershipInput{
		ContactId: "contact-1", ContactGroupId: "seg-target",
	})
	if err != nil {
		t.Fatalf("CreateContactGroupMembership: %v", err)
	}
	if out.ContactGroupMembershipID != "seg-target" {
		t.Errorf("ContactGroupMembershipID = %q, want seg-target", out.ContactGroupMembershipID)
	}
	if n := log.posts("/contacts/contact-1/segments/seg-target"); n != 1 {
		t.Errorf("added the contact to the segment %d times, want 1", n)
	}
}
