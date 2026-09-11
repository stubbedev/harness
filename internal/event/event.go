// Package event is the seam where usage events used to be reported to a
// remote analytics service. This fork sends nothing: every reporting entry
// point below is inert, and no network client is constructed or configured.
//
// The API is kept so the call sites throughout the app — which mark session,
// prompt, and tool activity — stay in place and continue to document what the
// app considers notable. If reporting is ever wanted again, it belongs behind
// send, Error, and Alias, and behind an explicit opt-in.
//
// GetID survives because it is not telemetry: it identifies the machine to
// providers that rate-limit per device (see the x-harness-id request header).
package event

import (
	"os"
)

const (
	nonInteractiveAttrName       = "NonInteractive"
	nonInteractiveNestedAttrName = "NonInteractiveNested"
	continueSessionByIDAttrName  = "ContinueSessionByID"
	continueLastSessionAttrName  = "ContinueLastSession"
)

// baseProps records how the app was invoked. Nothing transmits it; the
// setters below are kept so callers do not have to special-case a build
// without reporting.
var baseProps = properties{
	nonInteractiveAttrName:       false,
	nonInteractiveNestedAttrName: false,
}

// properties is the shape event data took when it was reported.
type properties map[string]any

func (p properties) Set(key string, value any) properties {
	p[key] = value
	return p
}

func SetNonInteractive(nonInteractive bool) {
	baseProps = baseProps.
		Set(nonInteractiveAttrName, nonInteractive).
		Set(nonInteractiveNestedAttrName, nonInteractive && os.Getenv("HARNESS") == "1")
}

func SetContinueBySessionID(continueBySessionID bool) {
	baseProps = baseProps.Set(continueSessionByIDAttrName, continueBySessionID)
}

func SetContinueLastSession(continueLastSession bool) {
	baseProps = baseProps.Set(continueLastSessionAttrName, continueLastSession)
}

// Init resolves the machine identifier. It opens no connections.
func Init() {
	distinctId = getDistinctId()
}

// GetID returns the machine identifier, resolving it on first use so callers
// that never call Init still get a stable value.
func GetID() string {
	if distinctId == "" {
		distinctId = getDistinctId()
	}
	return distinctId
}

// Alias is inert: there is no analytics identity to link an account to.
func Alias(string) {}

// send is inert. Callers in all.go describe app activity for future use.
func send(string, ...any) {}

// Error is inert. Errors are surfaced through the logs instead.
func Error(any, ...any) {}

// Flush is inert: nothing is buffered.
func Flush() {}
