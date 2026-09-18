package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
)

// forumUser is the part of the forum's /api/me answer the panel reads. The
// two group fields come from the forum's user:groups scope.
type forumUser struct {
	UserID            int    `json:"user_id"`
	Username          string `json:"username"`
	UserGroupID       int    `json:"user_group_id"`
	SecondaryGroupIDs []int  `json:"secondary_group_ids"`
}

// checkOutcome is what the group check decided. It is the whole contract
// between the forum call and the handlers: each outcome maps to one response.
type checkOutcome int

const (
	// checkPassed: the forum knows the token and the user holds an
	// allowlisted group.
	checkPassed checkOutcome = iota
	// checkExpired: the forum refused the token (401). The session ends with
	// cause expired.
	checkExpired
	// checkNoGroup: the forum answered but the user may not open the panel,
	// either a 403 or a 200 with no allowlisted group. The session ends with
	// cause no-group.
	checkNoGroup
	// checkUnavailable: the forum did not answer, or answered with a status
	// the panel cannot read as a decision. The session is kept and the user
	// sees the error page.
	checkUnavailable
)

// cause is the sign-in page's word for an outcome that ends the session.
// checkPassed and checkUnavailable end nothing and map to none.
func (o checkOutcome) cause() cause {
	switch o {
	case checkExpired:
		return causeExpired
	case checkNoGroup:
		return causeNoGroup
	}
	return causeNone
}

// groupCheck is one GET to the userinfo URL with the access token. It returns
// the user on checkPassed and the outcome in every case; err carries the
// transport or read failure behind checkUnavailable for the log line.
func (p *Panel) groupCheck(ctx context.Context, accessToken string) (forumUser, checkOutcome, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.UserinfoURL, nil)
	if err != nil {
		return forumUser{}, checkUnavailable, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	res, err := p.client.Do(req)
	if err != nil {
		return forumUser{}, checkUnavailable, err
	}
	defer func() { _ = res.Body.Close() }()

	switch {
	case res.StatusCode == http.StatusUnauthorized:
		return forumUser{}, checkExpired, nil
	case res.StatusCode == http.StatusForbidden:
		return forumUser{}, checkNoGroup, nil
	case res.StatusCode != http.StatusOK:
		return forumUser{}, checkUnavailable, fmt.Errorf("userinfo answered %d", res.StatusCode)
	}

	var envelope struct {
		Me forumUser `json:"me"`
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return forumUser{}, checkUnavailable, err
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return forumUser{}, checkUnavailable, fmt.Errorf("userinfo body: %w", err)
	}
	if !p.allowed(envelope.Me) {
		return envelope.Me, checkNoGroup, nil
	}
	return envelope.Me, checkPassed, nil
}

// allowed is the group check's rule: the primary group or any secondary group
// is in the allowlist.
func (p *Panel) allowed(u forumUser) bool {
	if slices.Contains(p.cfg.GroupIDs, u.UserGroupID) {
		return true
	}
	for _, id := range u.SecondaryGroupIDs {
		if slices.Contains(p.cfg.GroupIDs, id) {
			return true
		}
	}
	return false
}
