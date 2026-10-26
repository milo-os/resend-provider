package logredact

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
)

const address = "jane.doe+test@example.co.uk"

type recipient struct {
	UserRef      string
	EmailAddress string
}

func capture() (logr.Logger, *strings.Builder) {
	var b strings.Builder
	log := funcr.New(func(prefix, args string) {
		b.WriteString(prefix + " " + args + "\n")
	}, funcr.Options{})
	return log, &b
}

func TestWrapRedactsAddresses(t *testing.T) {
	sentinel := errors.New("resend says no")
	cases := map[string]func(logr.Logger){
		"message":        func(l logr.Logger) { l.Info("sending to " + address) },
		"string value":   func(l logr.Logger) { l.Info("sending", "to", address) },
		"display name":   func(l logr.Logger) { l.Info("sending", "from", "Jane <"+address+">") },
		"struct value":   func(l logr.Logger) { l.Info("sending", "recipient", recipient{EmailAddress: address}) },
		"error":          func(l logr.Logger) { l.Error(errors.New("invalid `to` field: "+address), "send failed") },
		"error value":    func(l logr.Logger) { l.Info("send failed", "err", errors.New("bad "+address)) },
		"with values":    func(l logr.Logger) { l.WithValues("to", address).Info("sending") },
		"with name":      func(l logr.Logger) { l.WithName("x").Info("sending", "to", address) },
		"wrapped error":  func(l logr.Logger) { l.Error(errors.Join(sentinel, errors.New(address)), "send failed") },
		"verbose info":   func(l logr.Logger) { l.V(0).Info("sending", "to", address) },
		"slice of value": func(l logr.Logger) { l.Info("sending", "to", []string{address}) },
	}
	for name, logIt := range cases {
		t.Run(name, func(t *testing.T) {
			raw, out := capture()
			logIt(Wrap(raw))
			if strings.Contains(out.String(), address) || strings.Contains(out.String(), "example.co.uk") {
				t.Fatalf("address was logged: %s", out.String())
			}
			if !strings.Contains(out.String(), Placeholder) {
				t.Fatalf("expected %s in output: %s", Placeholder, out.String())
			}
		})
	}
}

func TestWrapKeepsOtherValues(t *testing.T) {
	raw, out := capture()
	Wrap(raw).Info("reconciled", "email", "milo-system/suspicious-login-1", "attempts", 3)
	got := out.String()
	if !strings.Contains(got, `"email"="milo-system/suspicious-login-1"`) || !strings.Contains(got, `"attempts"=3`) {
		t.Fatalf("ordinary values changed: %s", got)
	}
}

func TestRedactedErrorUnwraps(t *testing.T) {
	sentinel := errors.New("not found")
	err := redactError(errors.Join(sentinel, errors.New(address)))
	if !errors.Is(err, sentinel) {
		t.Fatal("redacted error no longer unwraps to the original")
	}
	if strings.Contains(err.Error(), address) {
		t.Fatalf("address in redacted error: %s", err)
	}
}
