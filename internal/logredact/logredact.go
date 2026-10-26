// Package logredact keeps email addresses out of logs.
//
// Our own log calls name Emails and Contacts by namespace and name, never by
// address. Errors from Resend and from the API server are not ours to shape,
// though, and can quote an address back. Wrap replaces any address in a log
// message, value or error with a placeholder before it is written.
package logredact

import (
	"fmt"
	"regexp"

	"github.com/go-logr/logr"
)

// Placeholder replaces every redacted address.
const Placeholder = "<redacted-email>"

var addressPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(\.[A-Za-z0-9\-]+)+`)

// String returns s with every email address replaced by Placeholder.
func String(s string) string {
	return addressPattern.ReplaceAllString(s, Placeholder)
}

// Wrap returns a logger that redacts email addresses before log writes them.
func Wrap(log logr.Logger) logr.Logger {
	sink := log.GetSink()
	if sink == nil {
		return log
	}
	return log.WithSink(&redactingSink{sink: sink})
}

type redactingSink struct {
	sink logr.LogSink
}

func (r *redactingSink) Init(info logr.RuntimeInfo) {
	// One extra frame, so the caller reported is the code that logged, not us.
	info.CallDepth++
	r.sink.Init(info)
}

func (r *redactingSink) Enabled(level int) bool { return r.sink.Enabled(level) }

func (r *redactingSink) Info(level int, msg string, keysAndValues ...any) {
	r.sink.Info(level, String(msg), redactValues(keysAndValues)...)
}

func (r *redactingSink) Error(err error, msg string, keysAndValues ...any) {
	r.sink.Error(redactError(err), String(msg), redactValues(keysAndValues)...)
}

func (r *redactingSink) WithValues(keysAndValues ...any) logr.LogSink {
	return &redactingSink{sink: r.sink.WithValues(redactValues(keysAndValues)...)}
}

func (r *redactingSink) WithName(name string) logr.LogSink {
	return &redactingSink{sink: r.sink.WithName(name)}
}

// redactValues redacts every value that contains an address. A value is only
// replaced, by its redacted text, when it contains one, so ordinary values keep
// their type and structured encoding.
func redactValues(keysAndValues []any) []any {
	out := make([]any, len(keysAndValues))
	for i, v := range keysAndValues {
		out[i] = redactValue(v)
	}
	return out
}

func redactValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		return String(t)
	case error:
		return redactError(t)
	default:
		s := fmt.Sprintf("%+v", t)
		if addressPattern.MatchString(s) {
			return String(s)
		}
		return v
	}
}

func redactError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if !addressPattern.MatchString(msg) {
		return err
	}
	return redactedError{msg: String(msg), err: err}
}

// redactedError reports a redacted message but still unwraps to the original,
// so errors.Is and errors.As keep working for anything that inspects it.
type redactedError struct {
	msg string
	err error
}

func (e redactedError) Error() string { return e.msg }
func (e redactedError) Unwrap() error { return e.err }
