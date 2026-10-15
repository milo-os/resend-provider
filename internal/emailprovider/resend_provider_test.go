package emailprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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
