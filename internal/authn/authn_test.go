package authn

import (
	"testing"

	"github.com/zeoril1/video_viewer/internal/db"
)

func TestCanEditSegments(t *testing.T) {
	for _, role := range []string{"", db.RoleUser, db.RoleModerator, db.RoleAdmin, "ADMIN", "owner"} {
		want := role == db.RoleModerator || role == db.RoleAdmin
		if got := CanEditSegments(db.User{Role: role}); got != want {
			t.Errorf("role %q allowed=%v, want %v", role, got, want)
		}
	}
}
