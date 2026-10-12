package controller

import (
	stderrors "errors"
	"time"

	kerrors "k8s.io/apimachinery/pkg/util/errors"
)

// dependentsRequeueAfter is how long a finalizer waits before checking again
// whether its dependent objects are gone.
const dependentsRequeueAfter = 5 * time.Second

// errDependentsRemaining means a finalizer is waiting for dependent objects to
// finish deleting. The finalizer helper treats any error as "not done yet",
// which is the only way to keep the finalizer in place, but this one is not a
// failure: Reconcile requeues instead of reporting it.
var errDependentsRemaining = stderrors.New("waiting for dependent objects to be deleted")

// onlyWaitingForDependents reports whether err, as returned by
// finalizer.Finalizers.Finalize, holds nothing but errDependentsRemaining.
func onlyWaitingForDependents(err error) bool {
	if err == nil {
		return false
	}
	var agg kerrors.Aggregate
	if !stderrors.As(err, &agg) {
		return stderrors.Is(err, errDependentsRemaining)
	}
	for _, e := range agg.Errors() {
		if !stderrors.Is(e, errDependentsRemaining) {
			return false
		}
	}
	return true
}
