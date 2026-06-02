package utils

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// testRSAKey generates a throwaway RSA private key and returns it both as a
// parseable PEM (the form GithubAuth consumes) and as the *rsa.PrivateKey, so a
// test can verify the RS256 signature GithubAuth produces against the public
// half.
func testRSAKey(t *testing.T) ([]byte, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return pemBytes, key
}

// testRSAPEM generates a throwaway RSA private key and returns it PEM-encoded.
// GithubAuth parses it with jwt.ParseRSAPrivateKeyFromPEM, so the bytes must be
// a real, parseable RSA key.
func testRSAPEM(t *testing.T) []byte {
	t.Helper()
	pemBytes, _ := testRSAKey(t)
	return pemBytes
}

// ghCapture records request details the fake server saw, so a test can assert
// the Authorization header (the minted JWT) and the dispatch payload without a
// live GitHub call.
type ghCapture struct {
	installAuth  string // Authorization header on GET /app/installations
	dispatchBody []byte // raw body of POST .../dispatches
	dispatchPath string // request path of POST .../dispatches
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
	capture        *ghCapture
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
			if g.capture != nil {
				g.capture.installAuth = r.Header.Get("Authorization")
			}
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
			if g.capture != nil {
				g.capture.dispatchBody, _ = io.ReadAll(r.Body)
				g.capture.dispatchPath = r.URL.Path
			}
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

// TestGithubAuth_JWTClaimsAndSignature captures the Bearer JWT GithubAuth sends
// to the installations endpoint and verifies its issuer, ~5-minute expiry
// window, and RS256 signature against the test key's public half. This pins the
// GitHub App authentication contract — a regression in signing alg or claims
// would otherwise pass silently behind the status-only fake.
func TestGithubAuth_JWTClaimsAndSignature(t *testing.T) {
	pemBytes, key := testRSAKey(t)
	cap := &ghCapture{}
	ghMux{capture: cap}.server(t)

	before := time.Now()
	if _, err := GithubAuth("Iv1.testclientid", pemBytes); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	after := time.Now()

	const prefix = "Bearer "
	if !strings.HasPrefix(cap.installAuth, prefix) {
		t.Fatalf("expected Bearer auth header, got %q", cap.installAuth)
	}
	raw := strings.TrimPrefix(cap.installAuth, prefix)

	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(raw, claims, func(tok *jwt.Token) (interface{}, error) {
		if _, ok := tok.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return &key.PublicKey, nil
	})
	if err != nil {
		t.Fatalf("JWT failed to verify against test public key: %v", err)
	}
	if !parsed.Valid {
		t.Fatal("parsed JWT reported invalid")
	}
	if alg, _ := parsed.Header["alg"].(string); alg != "RS256" {
		t.Fatalf("expected RS256 alg header, got %q", alg)
	}
	if iss, _ := claims["iss"].(string); iss != "Iv1.testclientid" {
		t.Fatalf("expected iss==clientID, got %q", iss)
	}

	// exp must sit ~5 minutes ahead of iat; allow slack for the wall-clock
	// window the call spanned.
	iat, ok1 := claims["iat"].(float64)
	exp, ok2 := claims["exp"].(float64)
	if !ok1 || !ok2 {
		t.Fatalf("missing iat/exp claims: %v", claims)
	}
	if d := exp - iat; d < 290 || d > 301 {
		t.Fatalf("expected ~300s expiry window, got %.0fs", d)
	}
	if exp < float64(before.Add(4*time.Minute).Unix()) || exp > float64(after.Add(6*time.Minute).Unix()) {
		t.Fatalf("exp %.0f outside expected ~5-min-from-now window [%d,%d]", exp, before.Unix(), after.Unix())
	}
}

func TestGithubAuth_MalformedInstallationsJSON(t *testing.T) {
	// 200 with a body that isn't the expected JSON array → unmarshal error.
	ghMux{installBody: `{not valid json`}.server(t)

	_, err := GithubAuth("Iv1.testclientid", testRSAPEM(t))
	if err == nil {
		t.Fatal("expected error on malformed installations JSON, got nil")
	}
	if !strings.Contains(err.Error(), "failed to unmarshal installations") {
		t.Fatalf("expected unmarshal-installations error, got %v", err)
	}
}

func TestGithubAuth_MalformedTokenJSON(t *testing.T) {
	// Installations succeed; the access-token 201 body is unparseable.
	ghMux{tokenBody: `{not valid json`}.server(t)

	_, err := GithubAuth("Iv1.testclientid", testRSAPEM(t))
	if err == nil {
		t.Fatal("expected error on malformed token JSON, got nil")
	}
	if !strings.Contains(err.Error(), "failed to unmarshal jwtToken") {
		t.Fatalf("expected unmarshal-token error, got %v", err)
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

// TestTriggerGithubDeployment_Payload captures the workflow-dispatch request and
// asserts it carries the expected ref + inputs.branch and targets the right
// workflow path. Without this, the status-only fake would accept a dispatch
// pointed at the wrong branch or workflow.
func TestTriggerGithubDeployment_Payload(t *testing.T) {
	cap := &ghCapture{}
	ghMux{capture: cap}.server(t)

	if err := TriggerGithubDeployment("feature-x", "tok", "7cav", "adr", "dev_deploy.yml", "main"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if want := "/repos/7cav/adr/actions/workflows/dev_deploy.yml/dispatches"; cap.dispatchPath != want {
		t.Fatalf("dispatch path = %q, want %q", cap.dispatchPath, want)
	}

	var payload struct {
		Ref    string `json:"ref"`
		Inputs struct {
			Branch string `json:"branch"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(cap.dispatchBody, &payload); err != nil {
		t.Fatalf("dispatch body not JSON: %v (raw=%q)", err, cap.dispatchBody)
	}
	if payload.Ref != "main" {
		t.Fatalf("ref = %q, want main", payload.Ref)
	}
	if payload.Inputs.Branch != "feature-x" {
		t.Fatalf("inputs.branch = %q, want feature-x", payload.Inputs.Branch)
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
