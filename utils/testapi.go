package utils

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
