package utils

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testRSAPEM generates a throwaway RSA private key and returns it PEM-encoded.
// GithubAuth parses it with jwt.ParseRSAPrivateKeyFromPEM, so the bytes must be
// a real, parseable RSA key.
func testRSAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

// ghMux is a configurable fake GitHub Apps API. Each phase defaults to its
// canonical success code; a non-zero override forces a failure for that phase.
type ghMux struct {
	installStatus  int    // GET /app/installations    (want 200)
	installBody    string // override the 200 body (for malformed-JSON / empty cases)
	tokenStatus    int    // POST .../access_tokens     (want 201)
	tokenBody      string // override the 201 body
	branchStatus   int    // GET .../branches/<branch>  (want 200)
	dispatchStatus int    // POST .../dispatches        (want 204)
}

func (g ghMux) server(t *testing.T) *httptest.Server {
	t.Helper()
	or := func(v, def int) int {
		if v == 0 {
			return def
		}
		return v
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/app/installations":
			st := or(g.installStatus, http.StatusOK)
			w.WriteHeader(st)
			if st == http.StatusOK {
				body := g.installBody
				if body == "" {
					body = `[{"id": 42}]`
				}
				_, _ = w.Write([]byte(body))
			}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/access_tokens"):
			st := or(g.tokenStatus, http.StatusCreated)
			w.WriteHeader(st)
			if st == http.StatusCreated {
				body := g.tokenBody
				if body == "" {
					body = `{"token": "ghs_testtoken"}`
				}
				_, _ = w.Write([]byte(body))
			}
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/branches/"):
			w.WriteHeader(or(g.branchStatus, http.StatusOK))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/dispatches"):
			w.WriteHeader(or(g.dispatchStatus, http.StatusNoContent))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(SetGithubBaseURLForTest(srv.URL))
	return srv
}

func TestGithubAuth_Success(t *testing.T) {
	ghMux{}.server(t)

	token, err := GithubAuth("Iv1.testclientid", testRSAPEM(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "ghs_testtoken" {
		t.Fatalf("expected minted token, got %q", token)
	}
}

func TestGithubAuth_MalformedPEM(t *testing.T) {
	// No server needed: parsing fails before any HTTP call.
	_, err := GithubAuth("Iv1.testclientid", []byte("not-a-pem"))
	if err == nil {
		t.Fatal("expected error for malformed PEM, got nil")
	}
	if !strings.Contains(err.Error(), "failed to parse private key") {
		t.Fatalf("expected parse-key error, got %v", err)
	}
}

func TestGithubAuth_InstallationsHTTPError(t *testing.T) {
	ghMux{installStatus: http.StatusInternalServerError}.server(t)

	_, err := GithubAuth("Iv1.testclientid", testRSAPEM(t))
	if err == nil {
		t.Fatal("expected error on installations 500, got nil")
	}
	if !strings.Contains(err.Error(), "non-200") {
		t.Fatalf("expected non-200 error, got %v", err)
	}
}

func TestGithubAuth_NoInstallations(t *testing.T) {
	ghMux{installBody: `[]`}.server(t)

	_, err := GithubAuth("Iv1.testclientid", testRSAPEM(t))
	if err == nil {
		t.Fatal("expected error when no installations, got nil")
	}
	if !strings.Contains(err.Error(), "no installations found") {
		t.Fatalf("expected no-installations error, got %v", err)
	}
}

func TestGithubAuth_TokenMintHTTPError(t *testing.T) {
	// Installations succeed; the access-token POST returns a 4xx.
	ghMux{tokenStatus: http.StatusUnauthorized}.server(t)

	_, err := GithubAuth("Iv1.testclientid", testRSAPEM(t))
	if err == nil {
		t.Fatal("expected error on token-mint failure, got nil")
	}
	if !strings.Contains(err.Error(), "non-201") {
		t.Fatalf("expected non-201 error, got %v", err)
	}
}

func TestTriggerGithubDeployment_Success(t *testing.T) {
	ghMux{}.server(t)

	err := TriggerGithubDeployment("feature-x", "tok", "7cav", "adr", "dev_deploy.yml", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTriggerGithubDeployment_HTTPError(t *testing.T) {
	ghMux{dispatchStatus: http.StatusUnprocessableEntity}.server(t)

	err := TriggerGithubDeployment("feature-x", "tok", "7cav", "adr", "dev_deploy.yml", "main")
	if err == nil {
		t.Fatal("expected error on dispatch 422, got nil")
	}
	if !strings.Contains(err.Error(), "non-204") {
		t.Fatalf("expected non-204 error, got %v", err)
	}
}

func TestCheckGithubBranchExists_Found(t *testing.T) {
	ghMux{}.server(t)

	if err := CheckGithubBranchExists("main", "tok", "7cav", "adr"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckGithubBranchExists_NotFound(t *testing.T) {
	ghMux{branchStatus: http.StatusNotFound}.server(t)

	err := CheckGithubBranchExists("ghost", "tok", "7cav", "adr")
	if err == nil {
		t.Fatal("expected error for missing branch, got nil")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected branch-missing error, got %v", err)
	}
}

func TestCheckGithubBranchExists_UnexpectedStatus(t *testing.T) {
	// A missing repo (or auth problem) surfaces as a non-200, non-404 status.
	ghMux{branchStatus: http.StatusInternalServerError}.server(t)

	err := CheckGithubBranchExists("main", "tok", "7cav", "missing-repo")
	if err == nil {
		t.Fatal("expected error on unexpected status, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected status code") {
		t.Fatalf("expected unexpected-status error, got %v", err)
	}
}
