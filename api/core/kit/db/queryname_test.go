package db_test

import (
	"testing"

	"chaskicode.dev/saas/platform/kit/db"
)

func TestQueryName(t *testing.T) {
	t.Parallel()

	for sql, want := range map[string]string{
		"-- name: ListMembers :many\nSELECT m.id FROM memberships m WHERE id > $1": "ListMembers",
		"SELECT set_config('app.user_id', $1::uuid::text, true)":                   db.RLSVarsQueryName,
		"begin":                    "BEGIN",
		"  -- a comment\nUPDATE x": "UPDATE",
		"":                         "UNKNOWN",
	} {
		if got := db.QueryName(sql); got != want {
			t.Errorf("QueryName(%q) = %q, want %q", sql, got, want)
		}
	}
}
