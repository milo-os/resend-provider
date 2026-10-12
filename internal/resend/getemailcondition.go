package resend

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	notificationmiloapiscomv1alpha1 "go.miloapis.com/milo/pkg/apis/notification/v1alpha1"
)

type EmailCondition struct {
	Status               metav1.ConditionStatus
	EmailDeliveredReason string
	CoreEventType        string
}

// GetEmailCondition returns the email condition for the given event type.
func GetEmailCondition(eventType EmailEventType) EmailCondition {
	var conditionStatus metav1.ConditionStatus
	var emailDeliveredReason string
	var coreEventType string

	conditionStatus = metav1.ConditionUnknown
	emailDeliveredReason = notificationmiloapiscomv1alpha1.EmailDeliveryPendingReason
	if slices.Contains(successfulDeliveredEventsDeliveredEvents, eventType) {
		conditionStatus = metav1.ConditionTrue
		emailDeliveredReason = notificationmiloapiscomv1alpha1.EmailDeliveredReason
	}
	if slices.Contains(failedDeliveredEvents, eventType) {
		conditionStatus = metav1.ConditionFalse
		emailDeliveredReason = notificationmiloapiscomv1alpha1.EmailDeliveryFailedReason
	}

	coreEventType = corev1.EventTypeWarning
	if slices.Contains(normalDeliveredEvents, eventType) {
		coreEventType = corev1.EventTypeNormal
	}

	return EmailCondition{
		Status:               conditionStatus,
		EmailDeliveredReason: emailDeliveredReason,
		CoreEventType:        coreEventType,
	}

}

// deliveryRank orders the states of the Delivered condition so that delivery
// status only moves forward:
//
//	pending (Unknown) < delivered (True) < failed (False)
//
// Resend doesn't guarantee the order of events, and it retries events the
// webhook refused, so an event can arrive after a later one. A late sent or
// delivery_delayed event must not turn a delivered or failed Email back to
// pending. Failed outranks delivered because a bounce can be reported after
// delivery, and a late delivered event must not hide that bounce. Complained,
// opened and clicked count as delivered.
func deliveryRank(status metav1.ConditionStatus) int {
	switch status {
	case metav1.ConditionTrue:
		return 1
	case metav1.ConditionFalse:
		return 2
	default:
		return 0
	}
}

// Supersedes reports whether c may replace the existing Delivered condition.
// A condition of equal rank replaces it, so that, for example, an opened
// event updates an Email already marked delivered.
func (c EmailCondition) Supersedes(existing *metav1.Condition) bool {
	if existing == nil {
		return true
	}
	return deliveryRank(c.Status) >= deliveryRank(existing.Status)
}
