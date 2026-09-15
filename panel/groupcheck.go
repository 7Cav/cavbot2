package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"
)

// Session end causes. The sign-in page carries the value in a data-cause
// attribute and renders a sentence for each; the attribute is a test
// contract.
const (
	// causeExpired: the forum refused the token (401), or the session passed
	// its lifetime.
	causeExpired = "expired"
	// causeNoGroup: the user holds no allowlisted group, or the forum refused
	// the user (403).
	causeNoGroup = "no-group"
)

// groupOutcome is what one group check decided.
type groupOutcome int

const (
	// groupPass: the user holds an allowlisted group.
	groupPass groupOutcome = iota
	// groupExpired: the forum answered 401. The session ends with causeExpired.
	groupExpired
	// groupNoGroup: 403, or 200 with no allowlisted group. The session ends
	// with causeNoGroup.
	groupNoGroup
	// groupUnavailable: transport error or 5xx. The session is kept and the
	// request gets an error page.
	groupUnavailable
)

// forumUser is what the userinfo endpoint says about the token's user.
type forumUser struct {
	UserID            int    `json:"user_id"`
	Username          string `json:"username"`
	UserGroupID       int    `json:"user_group_id"`
	SecondaryGroupIDs []int  `json:"secondary_group_ids"`
}

// userinfoTimeout bounds one group check. The forum is on the same host;
// anything slower is an outage the visitor should see as an error page, not
// a hung request.
const userinfoTimeout = 10 * time.Second

// groupCheck fetches the token's user from the forum and reports whether they
// may use the panel. One request, every time: access follows the forum with
// no restart and no cache.
func (s *Server) groupCheck(ctx context.Context, accessToken string) (forumUser, groupOutcome) {
	ctx, cancel := context.WithTimeout(ctx, userinfoTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.UserinfoURL, nil)
	if err != nil {
		return forumUser{}, groupUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return forumUser{}, groupUnavailable
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return forumUser{}, groupExpired
	case resp.StatusCode == http.StatusForbidden:
		return forumUser{}, groupNoGroup
	case resp.StatusCode >= 500:
		return forumUser{}, groupUnavailable
	case resp.StatusCode != http.StatusOK:
		// Any other status (a 429, a redirect) is unexpected from this
		// endpoint. Treat it as an outage so the session survives and the next
		// request retries; a revoked token answers 401 on that retry.
		return forumUser{}, groupUnavailable
	}

	var body struct {
		Me forumUser `json:"me"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return forumUser{}, groupUnavailable
	}
	if !s.allowlisted(body.Me) {
		return body.Me, groupNoGroup
	}
	return body.Me, groupPass
}

// allowlisted reports whether the user's primary or any secondary group is in
// the allowlist.
func (s *Server) allowlisted(u forumUser) bool {
	if slices.Contains(s.cfg.GroupIDs, u.UserGroupID) {
		return true
	}
	for _, id := range u.SecondaryGroupIDs {
		if slices.Contains(s.cfg.GroupIDs, id) {
			return true
		}
	}
	return false
}

// cause is the sign-in page's cause value for an outcome that ends the
// session, empty for the others.
func (o groupOutcome) cause() string {
	switch o {
	case groupExpired:
		return causeExpired
	case groupNoGroup:
		return causeNoGroup
	default:
		return ""
	}
}

// signInURL returns the sign-in path, carrying the cause when one applies.
func signInURL(cause string) string {
	if cause == "" {
		return "/signin"
	}
	return fmt.Sprintf("/signin?cause=%s", cause)
}
