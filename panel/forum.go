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
	// checkPassed: the forum knows the token and said who the user is. Any
	// forum user passes; their groups decide which pages they open.
	checkPassed checkOutcome = iota
	// checkExpired: the forum refused the token (401). The session ends with
	// cause expired.
	checkExpired
	// checkRefused: the forum knows the token but would not say who the user
	// is (403). The session ends with cause refused.
	checkRefused
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
	case checkRefused:
		return causeRefused
	}
	return causeNone
}

// groupCheck is one GET to the userinfo URL with the access token. It returns
// the user on checkPassed and the outcome in every case; err carries the
// transport or read failure behind checkUnavailable for the log line. What
// the user opens is p.accessOf(user), decided by the caller.
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
		return forumUser{}, checkRefused, nil
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
	return envelope.Me, checkPassed, nil
}

// access is what one group check lets a session open. A user can be both a
// panel admin and a Foxhole manager.
type access struct {
	// panelAdmin is the primary group or any secondary group being one of
	// the panel's admin groups. A panel admin opens every page.
	panelAdmin bool
	// foxholeManager is the primary group or any secondary group being the
	// Foxhole group.
	foxholeManager bool
}

// foxholePage reports whether the session opens the Foxhole page: panel
// admins and Foxhole managers do.
func (a access) foxholePage() bool { return a.panelAdmin || a.foxholeManager }

// landing is the address of the page the session lands on at sign-in: the
// hub page for a panel admin, else the Foxhole page for a Foxhole manager.
// Empty when the session opens no page, and the no-access page shows.
func (a access) landing() string {
	switch {
	case a.panelAdmin:
		return "/"
	case a.foxholePage():
		return foxholePath
	}
	return ""
}

// accessOf is the group check's rule: the user's primary group or any
// secondary group decides what the session opens.
func (p *Panel) accessOf(u forumUser) access {
	return access{
		panelAdmin:     inGroups(u, p.cfg.AdminGroupIDs...),
		foxholeManager: inGroups(u, p.cfg.FoxholeGroupID),
	}
}

// inGroups reports whether the user's primary group or any secondary group
// is one of ids.
func inGroups(u forumUser, ids ...int) bool {
	if slices.Contains(ids, u.UserGroupID) {
		return true
	}
	for _, id := range u.SecondaryGroupIDs {
		if slices.Contains(ids, id) {
			return true
		}
	}
	return false
}
