// Package sentry stands in for github.com/getsentry/sentry-go, with the calls
// the testdata's capture makes.
package sentry

type Context = map[string]any

type Scope struct{}

func (s *Scope) SetTag(key, value string) {}

func (s *Scope) SetContext(key string, value Context) {}

type EventID string

func WithScope(f func(scope *Scope)) { f(&Scope{}) }

func CaptureException(exception error) *EventID { return nil }
