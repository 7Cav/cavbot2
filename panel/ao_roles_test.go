package panel

import (
	"context"
	"net/url"
	"strconv"
	"testing"

	"github.com/7cav/cavbot2/store"
)

// aoRolesForm is the AO roles section's form as a page loaded now posts it.
func aoRolesForm(t *testing.T, st store.Store, roleIDs ...string) url.Values {
	t.Helper()
	return url.Values{fieldAORoles: roleIDs, fieldVersion: {strconv.FormatInt(storedAORoles(t, st).Version, 10)}}
}

// storedAORoles reads the AO roles back through the store.
func storedAORoles(t *testing.T, st store.Store) store.AORoles {
	t.Helper()
	roles, err := st.GetAORoles(context.Background(), testGuildID)
	if err != nil {
		t.Fatalf("GetAORoles: %v", err)
	}
	return roles
}
