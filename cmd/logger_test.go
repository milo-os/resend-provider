package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

// Both commands install newLogger, so an address quoted back by Resend or the
// API server never reaches their output.
func TestNewLoggerRedactsAddresses(t *testing.T) {
	const address = "jane.doe@example.com"
	var out bytes.Buffer
	log := newLogger(zap.WriteTo(&out), zap.JSONEncoder())

	log.Error(errors.New("invalid `to` field: "+address), "Reconciler error", "to", address)

	if strings.Contains(out.String(), address) {
		t.Fatalf("address was logged: %s", out.String())
	}
	if !strings.Contains(out.String(), "Reconciler error") {
		t.Fatalf("log line missing: %s", out.String())
	}
}
