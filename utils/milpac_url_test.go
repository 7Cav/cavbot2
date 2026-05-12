package utils

import "testing"

func TestExtractMilpacIDFromUniformURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantID  string
		wantErr bool
	}{
		{"happy path", "https://example.com/cdn/123/456.jpg", "456", false},
		{"prod-shaped URL", "https://7cav.us/data/avatars/o/123/45678.jpg", "45678", false},
		{"empty string", "", "", true},
		{"missing .jpg suffix", "https://example.com/cdn/123/456.png", "", true},
		{"no digit segments", "https://example.com/avatar.jpg", "", true},
		{"single digit segment", "https://example.com/456.jpg", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExtractMilpacIDFromUniformURL(tc.url)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got id=%q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantID {
				t.Fatalf("id = %q, want %q", got, tc.wantID)
			}
		})
	}
}
