package utils

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
