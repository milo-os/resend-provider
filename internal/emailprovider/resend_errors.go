package emailprovider

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// resendNotFoundError is a 404 answer from the Resend API.
type resendNotFoundError struct {
	Message string
}

func (e *resendNotFoundError) Error() string {
	if e.Message == "" {
		return "resend: not found"
	}
	return "resend: not found: " + e.Message
}

// notFoundTransport turns Resend 404 responses into a resendNotFoundError.
//
// The Resend SDK reports every non-2xx response, other than 429, as a plain
// error carrying only the API's message, so the status code is lost by the
// time it reaches the caller. Failing the round trip on a 404 keeps it: the
// SDK returns transport errors unchanged, wrapped in a *url.Error.
type notFoundTransport struct {
	base http.RoundTripper
}

func (t notFoundTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusNotFound {
		return resp, err
	}
	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Message string `json:"message"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 8<<10)).Decode(&body)
	return nil, &resendNotFoundError{Message: body.Message}
}

// TranslateResendError converts a Resend 404 into a Kubernetes NotFound error,
// so callers can rely on apierrors.IsNotFound. Any other error is returned
// unchanged, even if its message mentions "not found".
//
// The caller must specify which GroupResource and name were being requested
// so the generated error contains correct metadata.
func TranslateResendError(err error, gr schema.GroupResource, name string) error {
	var notFound *resendNotFoundError
	if errors.As(err, &notFound) {
		return apierrors.NewNotFound(gr, name)
	}
	return err
}
