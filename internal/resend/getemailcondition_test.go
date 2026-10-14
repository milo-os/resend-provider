package resend

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEmailConditionSupersedes(t *testing.T) {
	condition := func(status metav1.ConditionStatus) *metav1.Condition {
		return &metav1.Condition{Status: status}
	}
	tests := []struct {
		name     string
		event    EmailEventType
		existing *metav1.Condition
		want     bool
	}{
		{"first event", EventTypeSent, nil, true},
		{"pending to pending", EventTypeDeliveredDelayed, condition(metav1.ConditionUnknown), true},
		{"pending to delivered", EventTypeDelivered, condition(metav1.ConditionUnknown), true},
		{"pending to failed", EventTypeBounced, condition(metav1.ConditionUnknown), true},
		{"delivered to opened", EventTypeOpened, condition(metav1.ConditionTrue), true},
		{"delivered to complained", EventTypeComplained, condition(metav1.ConditionTrue), true},
		{"delivered to bounced", EventTypeBounced, condition(metav1.ConditionTrue), true},
		{"late sent after delivered", EventTypeSent, condition(metav1.ConditionTrue), false},
		{"late delayed after delivered", EventTypeDeliveredDelayed, condition(metav1.ConditionTrue), false},
		{"late scheduled after delivered", EventTypeScheduled, condition(metav1.ConditionTrue), false},
		{"late sent after bounced", EventTypeSent, condition(metav1.ConditionFalse), false},
		{"late delivered after bounced", EventTypeDelivered, condition(metav1.ConditionFalse), false},
		{"failed after bounced", EventTypeEmailFailed, condition(metav1.ConditionFalse), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetEmailCondition(tt.event).Supersedes(tt.existing); got != tt.want {
				t.Fatalf("Supersedes = %v, want %v", got, tt.want)
			}
		})
	}
}
