package resend

import (
	"strings"

	"k8s.io/apimachinery/pkg/types"
)

// Tag names that identify the Email resource an outgoing email was sent for.
// Resend echoes tags back in webhook events, which lets the webhook fetch the
// Email directly instead of searching for it by provider ID.
const (
	EmailNamespaceTag = "milo-email-namespace"
	EmailNameTag      = "milo-email-name"
)

// EmailRefTags returns the tags that identify the Email with the given
// namespace and name.
//
// Resend tag values may only contain ASCII letters, numbers, underscores and
// dashes. Kubernetes names may also contain dots but never underscores, so
// dots are encoded as underscores and decoded unambiguously.
func EmailRefTags(namespace, name string) []Tag {
	return []Tag{
		{Name: EmailNamespaceTag, Value: encodeTagValue(namespace)},
		{Name: EmailNameTag, Value: encodeTagValue(name)},
	}
}

// EmailRef returns the Email identified by the event's tags, if it has them.
func (b EmailBase) EmailRef() (types.NamespacedName, bool) {
	namespace, ok := b.Tags.Get(EmailNamespaceTag)
	if !ok || namespace == "" {
		return types.NamespacedName{}, false
	}
	name, ok := b.Tags.Get(EmailNameTag)
	if !ok || name == "" {
		return types.NamespacedName{}, false
	}
	return types.NamespacedName{
		Namespace: decodeTagValue(namespace),
		Name:      decodeTagValue(name),
	}, true
}

func encodeTagValue(s string) string {
	return strings.ReplaceAll(s, ".", "_")
}

func decodeTagValue(s string) string {
	return strings.ReplaceAll(s, "_", ".")
}
