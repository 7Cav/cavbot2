package utils

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
)

type sampleResponse struct {
	Name string `json:"name"`
}

func TestMakeAPIRequest_StatusMatrix(t *testing.T) {
	tests := []struct {
		name           string
		status         int
		body           string
		wantNilResult  bool
		wantErrSubstr  string // empty means error must be nil
		wantResultName string // expected sampleResponse.Name when result is non-nil
	}{
		{
			name:           "200 OK with valid JSON returns parsed result",
			status:         http.StatusOK,
			body:           `{"name":"alice"}`,
			wantNilResult:  false,
			wantResultName: "alice",
		},
		{
			name:          "200 OK with empty body returns non-nil zero-value result",
			status:        http.StatusOK,
			body:          "",
			wantNilResult: false,
		},
		{
			name:          "204 No Content is treated as 2xx",
			status:        http.StatusNoContent,
			body:          "",
			wantNilResult: false,
		},
		{
			name:          "404 returns nil result and friendly 'no <X> found' error",
			status:        http.StatusNotFound,
			body:          "",
			wantNilResult: true,
			wantErrSubstr: "no milpacs found",
		},
		{
			name:          "401 returns nil result and API-returned-401 error",
			status:        http.StatusUnauthorized,
			body:          `{"error":"unauthorized"}`,
			wantNilResult: true,
			wantErrSubstr: "milpacs API returned 401 Unauthorized",
		},
		{
			name:          "500 returns nil result and API-returned-500 error",
			status:        http.StatusInternalServerError,
			body:          "boom",
			wantNilResult: true,
			wantErrSubstr: "milpacs API returned 500 Internal Server Error",
		},
		{
			name:          "418 (arbitrary 4xx) is treated as non-2xx",
			status:        http.StatusTeapot,
			body:          "",
			wantNilResult: true,
			wantErrSubstr: "milpacs API returned 418",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.body != "" {
					w.Header().Set("Content-Type", "application/json")
				}
				w.WriteHeader(tc.status)
				if tc.body != "" {
					_, _ = io.WriteString(w, tc.body)
				}
			}))
			t.Cleanup(srv.Close)
			withTestAPIServer(t, srv)

			got, err := makeAPIRequest[sampleResponse](context.Background(), "milpacs/profile/username/alice", "alice")

			if tc.wantErrSubstr == "" {
				if err != nil {
					t.Fatalf("expected nil err, got: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Fatalf("expected error containing %q, got: %v", tc.wantErrSubstr, err)
				}
			}

			if tc.wantNilResult {
				if got != nil {
					t.Fatalf("expected nil result, got: %+v", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected non-nil result, got nil")
			}
			if tc.wantResultName != "" && got.Name != tc.wantResultName {
				t.Fatalf("expected name=%q, got %q", tc.wantResultName, got.Name)
			}
		})
	}
}

func TestMakeAPIRequest_SendsBearerFromEnv(t *testing.T) {
	t.Setenv("BEARER", "test-token-123")

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	withTestAPIServer(t, srv)

	if _, err := makeAPIRequest[sampleResponse](context.Background(), "milpacs/x", "x"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "Bearer test-token-123"; gotAuth != want {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, want)
	}
}

func TestMakeAPIRequest_BuildsRequestPathFromBaseURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	withTestAPIServer(t, srv)

	if _, err := makeAPIRequest[sampleResponse](context.Background(), "milpacs/profile/username/alice", "alice"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "/milpacs/profile/username/alice"; gotPath != want {
		t.Fatalf("request path = %q, want %q", gotPath, want)
	}
}

func TestMakeAPIRequest_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	withTestAPIServer(t, srv)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := makeAPIRequest[sampleResponse](ctx, "milpacs/profile/username/alice", "alice")
	if got != nil {
		t.Fatalf("expected nil result on context cancel, got %+v", *got)
	}
	if err == nil {
		t.Fatalf("expected non-nil error on context cancel")
	}
	if !strings.Contains(err.Error(), "failed to fetch milpacs") {
		t.Fatalf("expected wrapped 'failed to fetch milpacs' error, got: %v", err)
	}
}

func TestMakeAPIRequest_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	srv.Close() // close immediately so connect attempts fail
	withTestAPIServer(t, srv)

	got, err := makeAPIRequest[sampleResponse](context.Background(), "milpacs/profile/username/alice", "alice")
	if got != nil {
		t.Fatalf("expected nil result on transport error, got %+v", *got)
	}
	if err == nil {
		t.Fatalf("expected non-nil error on transport error")
	}
	if !strings.Contains(err.Error(), "failed to fetch milpacs") {
		t.Fatalf("expected wrapped 'failed to fetch milpacs' error, got: %v", err)
	}
}

// TestTypedWrappers_RequestPaths asserts the EXACT request path each typed
// wrapper issues. These are golden-path assertions pinned to current
// production behaviour — note the deliberate `milpac/` vs `milpacs/` prefix
// inconsistency between wrappers. The intent is to catch a future typo (wrong
// prefix → 404 → "no X found"), not to assert which prefix is "correct".
func TestTypedWrappers_RequestPaths(t *testing.T) {
	tests := []struct {
		name     string
		arg      string
		wantPath string
		call     func(ctx context.Context, arg string) error
	}{
		{
			name:     "GetMilpacByUsername",
			arg:      "alice",
			wantPath: "/milpacs/profile/username/alice",
			call: func(ctx context.Context, arg string) error {
				_, err := GetMilpacByUsername(ctx, arg)
				return err
			},
		},
		{
			name:     "GetMilpacByDiscordID",
			arg:      "123456789",
			wantPath: "/milpac/discord/123456789",
			call: func(ctx context.Context, arg string) error {
				_, err := GetMilpacByDiscordID(ctx, arg)
				return err
			},
		},
		{
			name:     "GetRosterByFuzzyPositionSearch",
			arg:      "medic",
			wantPath: "/milpacs/position/search/medic",
			call: func(ctx context.Context, arg string) error {
				_, err := GetRosterByFuzzyPositionSearch(ctx, arg)
				return err
			},
		},
		{
			name:     "GetUserByGamertag",
			arg:      "tag42",
			wantPath: "/milpac/gamertag/tag42",
			call: func(ctx context.Context, arg string) error {
				_, err := GetUserByGamertag(ctx, arg)
				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{}`)
			}))
			t.Cleanup(srv.Close)
			withTestAPIServer(t, srv)

			if err := tc.call(context.Background(), tc.arg); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotPath != tc.wantPath {
				t.Fatalf("request path = %q, want %q", gotPath, tc.wantPath)
			}
		})
	}
}

func TestGetRosterStatus(t *testing.T) {
	tests := []struct {
		name   string
		roster string
		want   string
	}{
		{
			name:   "known status maps to friendly label",
			roster: "ROSTER_TYPE_COMBAT",
			want:   "Active Duty",
		},
		{
			name:   "another known status maps to friendly label",
			roster: "ROSTER_TYPE_WALL_OF_HONOR",
			want:   "Wall of Honor",
		},
		{
			name:   "unknown status passes through raw",
			roster: "ROSTER_TYPE_SOMETHING_NEW",
			want:   "ROSTER_TYPE_SOMETHING_NEW",
		},
		{
			name:   "empty status passes through raw",
			roster: "",
			want:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &ProfileResponse{Roster: tc.roster}
			if got := r.GetRosterStatus(); got != tc.want {
				t.Fatalf("GetRosterStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGetMilpacByDiscordID_MapsConsoleGamertag pins the wire contract: the
// canonical 7Cav API serializes the in-game tag under the key "consoleGamertag"
// (not "gamertag"), so ProfileResponse.Gamertag must decode from that key. This
// feeds a RAW JSON body (the literal key the API sends) — a marshaled-struct
// fixture would round-trip through the same tag and hide a mismatch.
func TestGetMilpacByDiscordID_MapsConsoleGamertag(t *testing.T) {
	const body = `{"user":{"username":"Miller.L"},"consoleGamertag":"DrakenActual","roster":"ROSTER_TYPE_COMBAT"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	withTestAPIServer(t, srv)

	got, err := GetMilpacByDiscordID(context.Background(), "111")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Gamertag != "DrakenActual" {
		t.Fatalf("Gamertag = %q, want %q (decoded from consoleGamertag key)", got.Gamertag, "DrakenActual")
	}
}

// apiRequest is one request a fake 7Cav API received.
type apiRequest struct {
	path string
	auth string
}

// recordingAPI is a fake 7Cav API that answers 200 {} to everything and
// records each request. A bare handler, so a doubled slash reaches it as
// sent rather than as a redirect.
type recordingAPI struct {
	*httptest.Server
	mu       sync.Mutex
	received []apiRequest
}

func newRecordingAPI(t *testing.T) *recordingAPI {
	t.Helper()
	api := &recordingAPI{}
	api.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		api.received = append(api.received, apiRequest{path: r.URL.Path, auth: r.Header.Get("Authorization")})
		api.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(api.Close)
	return api
}

func (a *recordingAPI) requests() []apiRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]apiRequest(nil), a.received...)
}

// initAPIBaseURL runs the startup read of CAV_API_BASE_URL and puts the
// lookups back where they were when the test ends.
func initAPIBaseURL(t *testing.T) {
	t.Helper()
	prev := apiBaseURL
	t.Cleanup(func() { apiBaseURL = prev })
	InitAPIBaseURL()
}

func TestInitAPIBaseURL_EveryLookupGoesToTheConfiguredOrigin(t *testing.T) {
	t.Setenv("BEARER", "token-652")
	lookups := []struct {
		name     string
		wantPath string
		call     func(ctx context.Context) error
	}{
		{"GetMilpacByUsername", "/api/v1/milpacs/profile/username/alice", func(ctx context.Context) error {
			_, err := GetMilpacByUsername(ctx, "alice")
			return err
		}},
		{"GetMilpacByDiscordID", "/api/v1/milpac/discord/123456789", func(ctx context.Context) error {
			_, err := GetMilpacByDiscordID(ctx, "123456789")
			return err
		}},
		{"GetUserByGamertag", "/api/v1/milpac/gamertag/tag42", func(ctx context.Context) error {
			_, err := GetUserByGamertag(ctx, "tag42")
			return err
		}},
		{"GetRosterByFuzzyPositionSearch", "/api/v1/milpacs/position/search/a/acd", func(ctx context.Context) error {
			_, err := GetRosterByFuzzyPositionSearch(ctx, "a/acd")
			return err
		}},
		{"GetRanks", "/api/v1/milpacs/ranks", func(ctx context.Context) error {
			_, err := GetRanks(ctx)
			return err
		}},
	}
	for _, tc := range lookups {
		t.Run(tc.name, func(t *testing.T) {
			api := newRecordingAPI(t)
			t.Setenv("CAV_API_BASE_URL", api.URL)
			initAPIBaseURL(t)

			if err := tc.call(context.Background()); err != nil {
				t.Fatalf("lookup failed: %v", err)
			}
			want := []apiRequest{{path: tc.wantPath, auth: "Bearer token-652"}}
			if got := api.requests(); !slices.Equal(got, want) {
				t.Fatalf("the configured API received %+v, want %+v", got, want)
			}
		})
	}
}

func TestInitAPIBaseURL_TrailingSlashLeavesTheRequestUnchanged(t *testing.T) {
	api := newRecordingAPI(t)
	t.Setenv("CAV_API_BASE_URL", api.URL+"/")
	initAPIBaseURL(t)

	if _, err := GetRanks(context.Background()); err != nil {
		t.Fatalf("lookup failed: %v", err)
	}
	got := api.requests()
	if len(got) != 1 || got[0].path != "/api/v1/milpacs/ranks" {
		t.Fatalf("the configured API received %+v, want one request for /api/v1/milpacs/ranks", got)
	}
}

// recordingTransport stands in for the network under every lookup. It
// answers each request 200 {} and records the URL it was sent to.
type recordingTransport struct {
	mu   sync.Mutex
	sent []string
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.sent = append(rt.sent, req.URL.String())
	rt.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Request:    req,
	}, nil
}

func (rt *recordingTransport) urls() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]string(nil), rt.sent...)
}

// ranksURL returns the URL a GetRanks lookup is sent to, caught before it
// reaches the network.
func ranksURL(t *testing.T) []string {
	t.Helper()
	rt := &recordingTransport{}
	withAPITransport(t, rt)
	if _, err := GetRanks(context.Background()); err != nil {
		t.Fatalf("lookup failed: %v", err)
	}
	return rt.urls()
}

const publicRanksURL = "https://api.7cav.us/api/v1/milpacs/ranks"

func TestInitAPIBaseURL_UnsetOrEmptyUsesThePublicAPIWithoutWarning(t *testing.T) {
	for _, tc := range []struct {
		name  string
		unset bool
	}{
		{name: "unset", unset: true},
		{name: "empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CAV_API_BASE_URL", "")
			if tc.unset {
				_ = os.Unsetenv("CAV_API_BASE_URL")
			}
			logs := recordLogs(t)
			events := initSentryWithTransport(t)
			initAPIBaseURL(t)

			if n := logs.count(slog.LevelWarn, anyRecord); n != 0 {
				t.Errorf("logged %d warnings, want none", n)
			}
			if n := len(events.Events()); n != 0 {
				t.Errorf("sent %d Sentry events, want none", n)
			}
			if got, want := ranksURL(t), []string{publicRanksURL}; !slices.Equal(got, want) {
				t.Fatalf("the lookup went to %q, want %q", got, want)
			}
		})
	}
}

func TestInitAPIBaseURL_MalformedValueKeepsThePublicAPIAndWarnsOnce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"path", "http://7cav-api:11000/api/v1"},
		{"scheme", "ftp://7cav-api:11000"},
		{"no host", "http://:11000"},
		{"does not parse", "http://7cav-api:port"},
		{"query", "http://7cav-api:11000?x=1"},
		{"empty query", "http://7cav-api:11000?"},
		{"fragment", "http://7cav-api:11000#x"},
		{"user info", "http://ops@7cav-api:11000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CAV_API_BASE_URL", tc.value)
			logs := recordLogs(t)
			events := initSentryWithTransport(t)
			initAPIBaseURL(t) // returning at all is the bot running on

			namesValue := func(rec slog.Record) bool {
				return carries(rec, func(a slog.Attr) bool { return a.Value.String() == tc.value })
			}
			if n := logs.count(slog.LevelWarn, namesValue); n != 1 {
				t.Errorf("logged %d warnings naming %q, want 1", n, tc.value)
			}
			if n := len(events.Events()); n != 1 {
				t.Errorf("sent %d Sentry events, want 1", n)
			}
			if got, want := ranksURL(t), []string{publicRanksURL}; !slices.Equal(got, want) {
				t.Fatalf("the lookup went to %q, want %q", got, want)
			}
		})
	}
}

func TestInitAPIBaseURL_RejectedValueKeepsItsPasswordOutOfTheLogAndSentry(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"parses", "http://ops:hunter2@7cav-api:11000"},
		{"does not parse", "http://ops:hunter2@7cav-api:bad"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CAV_API_BASE_URL", tc.value)
			logs := recordLogs(t)
			events := initSentryWithTransport(t)
			initAPIBaseURL(t)

			carriesPassword := func(a slog.Attr) bool { return strings.Contains(a.Value.String(), "hunter2") }
			for _, rec := range logs.all() {
				if strings.Contains(rec.Message, "hunter2") || carries(rec, carriesPassword) {
					t.Errorf("log record %q carries the password", rec.Message)
				}
			}
			for _, ev := range events.Events() {
				data, err := json.Marshal(ev)
				if err != nil {
					t.Fatalf("marshal Sentry event: %v", err)
				}
				if strings.Contains(string(data), "hunter2") {
					t.Errorf("Sentry event carries the password: %s", data)
				}
			}
			namesHost := func(rec slog.Record) bool {
				return carries(rec, func(a slog.Attr) bool { return strings.Contains(a.Value.String(), "7cav-api") })
			}
			if n := logs.count(slog.LevelWarn, namesHost); n != 1 {
				t.Errorf("logged %d warnings naming the value's host, want 1", n)
			}
		})
	}
}

func TestInitAPIBaseURL_LogsTheAddressInUseOnce(t *testing.T) {
	for _, tc := range []struct {
		name       string
		value      string
		wantOrigin string
	}{
		{"unset", "", "https://api.7cav.us"},
		{"set", "http://7cav-api:11000", "http://7cav-api:11000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CAV_API_BASE_URL", tc.value)
			if tc.value == "" {
				_ = os.Unsetenv("CAV_API_BASE_URL")
			}
			logs := recordLogs(t)
			initAPIBaseURL(t)

			namesAddress := func(a slog.Attr) bool {
				u, err := url.Parse(a.Value.String())
				return err == nil && u.Scheme+"://"+u.Host == tc.wantOrigin
			}
			if n := logs.infoLinesWhere(namesAddress); n != 1 {
				t.Errorf("logged %d INFO lines naming %s, want 1", n, tc.wantOrigin)
			}
		})
	}
}
