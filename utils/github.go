package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-resty/resty/v2"
	"github.com/golang-jwt/jwt/v5"
	"strconv"
	"time"
)

// ghAppsBaseURL is the GitHub REST API root. It's a package-level var (not a
// const) so tests can redirect GithubAuth/TriggerGithubDeployment/
// CheckGithubBranchExists at an httptest.Server via SetGithubBaseURLForTest.
var ghAppsBaseURL = "https://api.github.com"

func GithubAuth(clientID string, privateKey []byte) (string, error) {
	now := time.Now()
	jwtToken := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iat": now.Unix(),
		"exp": now.Add(time.Minute * 5).Unix(),
		"iss": clientID,
	})
	jwtToken.Header["alg"] = "RS256"

	key, err := jwt.ParseRSAPrivateKeyFromPEM(privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to parse private key: %w", err)
	}

	signedToken, err := jwtToken.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("failed to sign jwtToken: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client := resty.New().
		SetRetryCount(3).
		SetRetryWaitTime(1 * time.Second)

	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Accept", "application/vnd.github+json").
		SetHeader("Authorization", "Bearer "+signedToken).
		Get(ghAppsBaseURL + "/app/installations")

	if err != nil {
		return "", fmt.Errorf("failed to get installations: %w", err)
	}

	if resp.StatusCode() != 200 {
		return "", fmt.Errorf("github API returned non-200 status code: %d %s", resp.StatusCode(), resp.Body())
	}

	var installations []struct {
		ID int `json:"id"`
	}

	err = json.Unmarshal(resp.Body(), &installations)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal installations: %w", err)
	}
	if len(installations) == 0 {
		return "", fmt.Errorf("no installations found")
	}

	installationID := strconv.Itoa(installations[0].ID)
	tokenResp, err := client.R().
		SetContext(ctx).
		SetHeader("Accept", "application/vnd.github+json").
		SetHeader("Authorization", "Bearer "+signedToken).
		Post(fmt.Sprintf("%s/app/installations/%s/access_tokens", ghAppsBaseURL, installationID))

	if err != nil {
		return "", fmt.Errorf("failed to get installation jwtToken: %w", err)
	}

	if tokenResp.StatusCode() != 201 {
		return "", fmt.Errorf("github API returned non-201 status code: %d %s", tokenResp.StatusCode(), tokenResp.Body())
	}

	var tokenData struct {
		Token string `json:"token"`
	}

	err = json.Unmarshal(tokenResp.Body(), &tokenData)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal jwtToken: %w", err)
	}

	return tokenData.Token, nil
}

func TriggerGithubDeployment(branch string, token string, owner string, repo string, workflow string, ref string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client := resty.New().
		SetRetryCount(3).
		SetRetryWaitTime(1 * time.Second)

	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Accept", "application/vnd.github+json").
		SetHeader("Authorization", "Bearer "+token).
		SetBody(map[string]interface{}{
			"ref": ref,
			"inputs": map[string]interface{}{
				"branch": branch,
			},
		}).
		Post(fmt.Sprintf("%s/repos/%s/%s/actions/workflows/%s/dispatches", ghAppsBaseURL, owner, repo, workflow))

	if err != nil {
		return fmt.Errorf("failed to trigger github workflow: %w", err)
	}

	if resp.StatusCode() != 204 {
		return fmt.Errorf("github API returned non-204 status code: %d %s", resp.StatusCode(), resp.Body())
	}
	return nil
}

func CheckGithubBranchExists(branch, token, owner, repo string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client := resty.New().
		SetRetryCount(3).
		SetRetryWaitTime(1 * time.Second)

	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Accept", "application/vnd.github+json").
		SetHeader("Authorization", "Bearer "+token).
		Get(fmt.Sprintf("%s/repos/%s/%s/branches/%s", ghAppsBaseURL, owner, repo, branch))

	if err != nil {
		return fmt.Errorf("failed to check branch: %w", err)
	}

	if resp.StatusCode() == 404 {
		return fmt.Errorf("branch '%s' does not exist in repository %s/%s", branch, owner, repo)
	}

	if resp.StatusCode() != 200 {
		return fmt.Errorf("github API returned unexpected status code: %d %s", resp.StatusCode(), resp.Body())
	}

	return nil
}
