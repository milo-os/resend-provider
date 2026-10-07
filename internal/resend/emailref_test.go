package resend

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func TestParseEmailEvent_TagShapes(t *testing.T) {
	cases := map[string]string{
		"object": `{"milo-email-namespace": "milo-system", "milo-email-name": "user_welcome", "category": "x"}`,
		"array": `[{"name": "milo-email-namespace", "value": "milo-system"},
			{"name": "milo-email-name", "value": "user_welcome"}]`,
	}
	for name, tags := range cases {
		t.Run(name, func(t *testing.T) {
			body := `{"type": "email.delivered", "created_at": "2026-02-22T23:41:12.126Z",
				"data": {"created_at": "2026-02-22T23:41:11.894Z", "email_id": "id-1", "tags": ` + tags + `}}`
			event, err := ParseEmailEvent([]byte(body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			ref, ok := event.Base.EmailRef()
			want := types.NamespacedName{Namespace: "milo-system", Name: "user.welcome"}
			if !ok || ref != want {
				t.Fatalf("got ref %v (ok=%v), want %v", ref, ok, want)
			}
		})
	}
}

func TestParseEmailEvent_NoTags(t *testing.T) {
	for _, tags := range []string{``, `, "tags": null`, `, "tags": {}`, `, "tags": []`, `, "tags": {"n": 1}`, `, "tags": "x"`} {
		body := `{"type": "email.sent", "created_at": "2026-02-22T23:41:12.126Z",
			"data": {"created_at": "2026-02-22T23:41:11.894Z", "email_id": "id-1"` + tags + `}}`
		event, err := ParseEmailEvent([]byte(body))
		if err != nil {
			t.Fatalf("tags %q: unexpected error: %v", tags, err)
		}
		if _, ok := event.Base.EmailRef(); ok {
			t.Fatalf("tags %q: expected no email ref", tags)
		}
	}
}

func TestEmailRefTags_RoundTrip(t *testing.T) {
	want := types.NamespacedName{Namespace: "milo-system", Name: "a.b-c.d"}
	const allowed = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-"
	base := EmailBase{Tags: EmailRefTags(want.Namespace, want.Name)}
	for _, tag := range base.Tags {
		for _, r := range tag.Value {
			if !strings.ContainsRune(allowed, r) {
				t.Fatalf("tag %s value %q has a character Resend rejects: %q", tag.Name, tag.Value, r)
			}
		}
	}
	got, ok := base.EmailRef()
	if !ok || got != want {
		t.Fatalf("got %v (ok=%v), want %v", got, ok, want)
	}
}
