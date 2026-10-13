package utils

import (
	"net/http"

	"github.com/go-resty/resty/v2"
)

// SetAPIBaseURLForTest replaces the 7Cav API base URL and returns a function
// that restores the previous value. Tests call it to redirect makeAPIRequest
// at an httptest.Server.
//
// Production code MUST NOT call this. The ForTest suffix is a hard signal in
// code review; we accept the small surface-area cost over the build-tag
// alternative.
func SetAPIBaseURLForTest(url string) (restore func()) {
	prev := apiBaseURL
	apiBaseURL = url
	return func() { apiBaseURL = prev }
}

// SetAPITransportForTest sends every 7Cav API call through rt in place of the
// network, and returns a function that restores the previous client. Tests
// call it to see the URL a lookup requests when that URL is one no test can
// serve, such as the public address.
//
// Production code MUST NOT call this, as with SetAPIBaseURLForTest.
func SetAPITransportForTest(rt http.RoundTripper) (restore func()) {
	prev := newAPIClient
	newAPIClient = func() *resty.Client { return resty.New().SetTransport(rt) }
	return func() { newAPIClient = prev }
}
