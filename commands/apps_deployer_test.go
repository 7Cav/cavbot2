package commands

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// testGithubPEM generates an RSA private key and returns it base64-encoded the
// way GITHUB_APP_KEY ships in the environment (PEM bytes, base64-wrapped).
// GithubAuth parses the decoded PEM with jwt.ParseRSAPrivateKeyFromPEM, so the
// key must be a real, parseable RSA key.
func testGithubPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return base64.StdEncoding.EncodeToString(pemBytes)
}

// setGithubEnv wires the env vars runAppsBetaComponentInteraction reads, with a
// valid PEM, restoring prior values on cleanup.
func setGithubEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_APP_KEY", testGithubPEM(t))
	t.Setenv("GITHUB_APP_CLIENT_ID", "Iv1.testclientid")
}

// githubCapture records the dispatch request the fake server saw, so a test can
// assert the command path sent the expected payload without a live GitHub call.
type githubCapture struct {
	dispatchBody []byte
	dispatchPath string
}

// githubMux is a configurable fake GitHub Apps API. Each phase can be made to
// fail by status code; the happy path returns the canonical success codes.
type githubMux struct {
	installStatus  int // GET /app/installations           (want 200)
	tokenStatus    int // POST .../access_tokens            (want 201)
	branchStatus   int // GET .../branches/<branch>         (want 200)
	dispatchStatus int // POST .../dispatches               (want 204)
	capture        *githubCapture
}

func (g githubMux) server(t *testing.T) *httptest.Server {
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
				_, _ = w.Write([]byte(`[{"id": 42}]`))
			}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/access_tokens"):
			st := or(g.tokenStatus, http.StatusCreated)
			w.WriteHeader(st)
			if st == http.StatusCreated {
				_, _ = w.Write([]byte(`{"token": "ghs_testtoken"}`))
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
	t.Cleanup(utils.SetGithubBaseURLForTest(srv.URL))
	return srv
}

// findRespond returns the first recorded InteractionRespond call, or fails.
func findRespond(t *testing.T, calls []recordedCall) recordedCall {
	t.Helper()
	for _, c := range calls {
		if c.Method == "Respond" {
			return c
		}
	}
	t.Fatalf("no Respond call recorded; got %d calls", len(calls))
	return recordedCall{}
}

func TestAppsBetaDeploy_Definition(t *testing.T) {
	cmd := AppsBetaDeploy()
	if cmd.Definition == nil {
		t.Fatal("AppsBetaDeploy must return a Definition")
	}
	if cmd.Definition.Name != "apps_beta_deploy" {
		t.Fatalf("command name = %q, want apps_beta_deploy", cmd.Definition.Name)
	}
	if len(cmd.Definition.Options) != 1 || cmd.Definition.Options[0].Name != "branch" {
		t.Fatalf("expected a required 'branch' option, got %+v", cmd.Definition.Options)
	}
	if !cmd.Definition.Options[0].Required {
		t.Fatal("branch option must be required")
	}
	if cmd.Handler == nil {
		t.Fatal("AppsBetaDeploy must wire a Handler")
	}
}

func TestRunAppsBetaDeploy_SuccessfulDispatch(t *testing.T) {
	setGithubEnv(t)
	githubMux{}.server(t)

	r := &fakeResponder{}
	i := fakeMessageComponentInteraction("apps_beta_deploy::confirm::feature-x")
	runAppsBetaDeploy(r, i)

	calls := r.Calls()
	// First call acknowledges via InteractionResponseUpdateMessage.
	ack := findRespond(t, calls)
	if ack.Response == nil || !strings.Contains(ack.Response.Data.Content, "Deployment initiated") {
		t.Fatalf("expected acknowledge content, got %+v", ack.Response)
	}
	// Final success is delivered as a followup.
	var followup *recordedCall
	for idx := range calls {
		if calls[idx].Method == "Followup" {
			followup = &calls[idx]
		}
	}
	if followup == nil {
		t.Fatalf("expected a Followup call on success; calls=%+v", calls)
	}
	if !strings.Contains(followup.Params.Content, "deployment started for branch `feature-x`") {
		t.Fatalf("unexpected followup content: %q", followup.Params.Content)
	}
}

// TestRunAppsBetaDeploy_DispatchPayloadAndAttribution drives the full confirm
// path and asserts (a) the dispatch the command sent targets dev_deploy.yml on
// 7cav/adr with ref=main and inputs.branch=<branch>, and (b) the success
// followup attributes the deploy to the invoking user and links the status URL.
func TestRunAppsBetaDeploy_DispatchPayloadAndAttribution(t *testing.T) {
	setGithubEnv(t)
	cap := &githubCapture{}
	githubMux{capture: cap}.server(t)

	r := &fakeResponder{}
	i := fakeMessageComponentInteraction("apps_beta_deploy::confirm::feature-x")
	runAppsBetaDeploy(r, i)

	// Dispatch payload: targets the expected workflow with the right ref/branch.
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
	if payload.Ref != "main" || payload.Inputs.Branch != "feature-x" {
		t.Fatalf("unexpected dispatch payload: ref=%q branch=%q", payload.Ref, payload.Inputs.Branch)
	}

	// Followup attributes the invoking user (ID 999, see fake interaction) and
	// includes the workflow status URL.
	var followup *recordedCall
	for idx := range r.Calls() {
		if r.Calls()[idx].Method == "Followup" {
			followup = &r.Calls()[idx]
		}
	}
	if followup == nil {
		t.Fatalf("expected a Followup call on success; calls=%+v", r.Calls())
	}
	if !strings.Contains(followup.Params.Content, "<@999>") {
		t.Fatalf("followup must attribute invoking user <@999>: %q", followup.Params.Content)
	}
	if !strings.Contains(followup.Params.Content, "https://github.com/7cav/adr/actions/workflows/dev_deploy.yml") {
		t.Fatalf("followup must include status URL: %q", followup.Params.Content)
	}
}

func TestRunAppsBetaDeploy_MissingAppKey(t *testing.T) {
	// Client ID present, app key cleared → specific config error, no dispatch.
	t.Setenv("GITHUB_APP_CLIENT_ID", "Iv1.testclientid")
	t.Setenv("GITHUB_APP_KEY", "")
	cap := &githubCapture{}
	githubMux{capture: cap}.server(t)

	r := &fakeResponder{}
	i := fakeMessageComponentInteraction("apps_beta_deploy::confirm::feature-x")
	runAppsBetaDeploy(r, i)

	if !errorContent(r.Calls(), "GitHub App key not configured") {
		t.Fatalf("expected missing-app-key error; calls=%+v", r.Calls())
	}
	if cap.dispatchBody != nil {
		t.Fatalf("must not dispatch when app key is missing; body=%q", cap.dispatchBody)
	}
	if hasFollowup(r.Calls()) {
		t.Fatalf("config error must not send a success followup")
	}
}

func TestRunAppsBetaDeploy_BadBase64Key(t *testing.T) {
	// App key set but not valid base64 → decode error, no dispatch.
	t.Setenv("GITHUB_APP_CLIENT_ID", "Iv1.testclientid")
	t.Setenv("GITHUB_APP_KEY", "not!valid!base64!")
	cap := &githubCapture{}
	githubMux{capture: cap}.server(t)

	r := &fakeResponder{}
	i := fakeMessageComponentInteraction("apps_beta_deploy::confirm::feature-x")
	runAppsBetaDeploy(r, i)

	if !errorContent(r.Calls(), "Failed to decode private key") {
		t.Fatalf("expected base64-decode error; calls=%+v", r.Calls())
	}
	if cap.dispatchBody != nil {
		t.Fatalf("must not dispatch on bad base64 key; body=%q", cap.dispatchBody)
	}
	if hasFollowup(r.Calls()) {
		t.Fatalf("config error must not send a success followup")
	}
}

func TestRunAppsBetaDeploy_MissingClientID(t *testing.T) {
	// Valid app key, client ID cleared → specific config error, no dispatch.
	t.Setenv("GITHUB_APP_KEY", testGithubPEM(t))
	t.Setenv("GITHUB_APP_CLIENT_ID", "")
	cap := &githubCapture{}
	githubMux{capture: cap}.server(t)

	r := &fakeResponder{}
	i := fakeMessageComponentInteraction("apps_beta_deploy::confirm::feature-x")
	runAppsBetaDeploy(r, i)

	if !errorContent(r.Calls(), "GitHub App client ID not configured") {
		t.Fatalf("expected missing-client-id error; calls=%+v", r.Calls())
	}
	if cap.dispatchBody != nil {
		t.Fatalf("must not dispatch when client ID is missing; body=%q", cap.dispatchBody)
	}
	if hasFollowup(r.Calls()) {
		t.Fatalf("config error must not send a success followup")
	}
}

func TestRunAppsBetaDeploy_AuthFailure(t *testing.T) {
	setGithubEnv(t)
	// Installations lookup returns 500 -> GithubAuth fails.
	githubMux{installStatus: http.StatusInternalServerError}.server(t)

	r := &fakeResponder{}
	i := fakeMessageComponentInteraction("apps_beta_deploy::confirm::feature-x")
	runAppsBetaDeploy(r, i)

	if !errorContent(r.Calls(), "Failed to authenticate with GitHub") {
		t.Fatalf("expected auth-failure error; calls=%+v", r.Calls())
	}
	if hasFollowup(r.Calls()) {
		t.Fatalf("auth failure must not send a success followup")
	}
}

func TestRunAppsBetaDeploy_DispatchHTTPError(t *testing.T) {
	setGithubEnv(t)
	// Auth + branch-check succeed; dispatch returns a non-204.
	githubMux{dispatchStatus: http.StatusUnprocessableEntity}.server(t)

	r := &fakeResponder{}
	i := fakeMessageComponentInteraction("apps_beta_deploy::confirm::feature-x")
	runAppsBetaDeploy(r, i)

	if !errorContent(r.Calls(), "Failed to trigger Apps Beta deployment") {
		t.Fatalf("expected dispatch-failure error; calls=%+v", r.Calls())
	}
	if hasFollowup(r.Calls()) {
		t.Fatalf("dispatch failure must not send a success followup")
	}
}

func TestRunAppsBetaDeploy_CancelAcknowledge(t *testing.T) {
	// Cancel path must acknowledge the component interaction and never touch
	// GitHub. No env / server configured proves no GitHub call is made.
	r := &fakeResponder{}
	i := fakeMessageComponentInteraction("apps_beta_deploy::cancel::feature-x")
	runAppsBetaDeploy(r, i)

	calls := r.Calls()
	ack := findRespond(t, calls)
	if ack.Response.Type != discordgo.InteractionResponseUpdateMessage {
		t.Fatalf("cancel must use InteractionResponseUpdateMessage, got %v", ack.Response.Type)
	}
	if !strings.Contains(ack.Response.Data.Content, "Deployment cancelled for branch `feature-x`") {
		t.Fatalf("unexpected cancel content: %q", ack.Response.Data.Content)
	}
	if hasFollowup(calls) {
		t.Fatalf("cancel must not send a followup")
	}
}

func TestRunAppsBetaDeploy_BranchDoesNotExist(t *testing.T) {
	setGithubEnv(t)
	githubMux{branchStatus: http.StatusNotFound}.server(t)

	r := &fakeResponder{}
	i := fakeMessageComponentInteraction("apps_beta_deploy::confirm::ghost")
	runAppsBetaDeploy(r, i)

	if !errorContent(r.Calls(), "Branch does not exist") {
		t.Fatalf("expected branch-missing error; calls=%+v", r.Calls())
	}
}

func TestRunAppsBetaInitialCommand_InvalidBranch(t *testing.T) {
	r := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("branch", "../evil"))
	runAppsBetaDeploy(r, i)

	if !errorContent(r.Calls(), "Invalid branch name") {
		t.Fatalf("expected invalid-branch error; calls=%+v", r.Calls())
	}
}

func TestRunAppsBetaInitialCommand_ConfirmPrompt(t *testing.T) {
	r := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("branch", "feature-x"))
	runAppsBetaDeploy(r, i)

	resp := findRespond(t, r.Calls())
	if !strings.Contains(resp.Response.Data.Content, "deploy branch `feature-x`") {
		t.Fatalf("unexpected confirm content: %q", resp.Response.Data.Content)
	}
	if len(resp.Response.Data.Components) == 0 {
		t.Fatalf("confirm prompt must include action-row buttons")
	}
}

func TestRunAppsBetaComponent_MalformedCustomID(t *testing.T) {
	r := &fakeResponder{}
	i := fakeMessageComponentInteraction("apps_beta_deploy::confirm") // only 2 parts
	runAppsBetaDeploy(r, i)

	if !errorContent(r.Calls(), "Invalid custom ID format") {
		t.Fatalf("expected malformed-id error; calls=%+v", r.Calls())
	}
}

// errorContent reports whether any recorded call carries the given substring,
// covering both the Respond (HandleError) and Edit retry paths.
func errorContent(calls []recordedCall, want string) bool {
	for _, c := range calls {
		if c.Response != nil && c.Response.Data != nil && strings.Contains(c.Response.Data.Content, want) {
			return true
		}
		if c.Edit != nil && c.Edit.Content != nil && strings.Contains(*c.Edit.Content, want) {
			return true
		}
	}
	return false
}

func hasFollowup(calls []recordedCall) bool {
	for _, c := range calls {
		if c.Method == "Followup" {
			return true
		}
	}
	return false
}
