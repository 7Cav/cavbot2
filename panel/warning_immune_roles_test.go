package panel

import (
	"context"
	"net/url"
	"strconv"
	"testing"

	"github.com/7cav/cavbot2/store"
)

// warningImmuneRolesForm is the Warnings section's form as a page loaded now
// posts it.
func warningImmuneRolesForm(t *testing.T, st store.Store, roleIDs ...string) url.Values {
	t.Helper()
	return url.Values{fieldWarningImmuneRoles: roleIDs, fieldVersion: {strconv.FormatInt(storedWarningImmuneRoles(t, st).Version, 10)}}
}

// storedWarningImmuneRoles reads the warning immune roles back through the
// store.
func storedWarningImmuneRoles(t *testing.T, st store.Store) store.WarningImmuneRoles {
	t.Helper()
	roles, err := st.GetWarningImmuneRoles(context.Background(), testGuildID)
	if err != nil {
		t.Fatalf("GetWarningImmuneRoles: %v", err)
	}
	return roles
}
