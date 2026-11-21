package db

// The rules of the design for SECURITY DEFINER functions (section 7.4), outside the
// _test files so that schema_test.go (package db_test) can hold the platform's and the
// app's functions to them.

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

type functionInfo struct {
	Name           string
	Owner          string
	Config         []string
	ArgNames       []string
	PublicExecute  bool
	ExecuteGranted []string // roles other than the owner and PUBLIC with EXECUTE
}

const pinnedSearchPath = "search_path=pg_catalog, extensions, pg_temp"

func loadSecurityDefiners(ctx context.Context, q querier) ([]functionInfo, error) {
	// A NULL proacl means default privileges, which include EXECUTE for PUBLIC.
	rows, err := q.Query(ctx, `
		SELECT p.proname,
		       pg_get_userbyid(p.proowner),
		       coalesce(p.proconfig, '{}'),
		       coalesce(p.proargnames, '{}'),
		       p.proacl IS NULL OR EXISTS (SELECT 1 FROM aclexplode(p.proacl) a
		                                   WHERE a.grantee = 0 AND a.privilege_type = 'EXECUTE'),
		       ARRAY(SELECT pg_get_userbyid(a.grantee)
		             FROM aclexplode(coalesce(p.proacl, '{}')) a
		             WHERE a.privilege_type = 'EXECUTE' AND a.grantee NOT IN (0, p.proowner)
		             ORDER BY 1)
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE p.prosecdef AND n.nspname = 'public'
		ORDER BY p.proname`)
	if err != nil {
		return nil, fmt.Errorf("query security definer functions: %w", err)
	}
	fns, err := pgx.CollectRows(rows, pgx.RowToStructByPos[functionInfo])
	if err != nil {
		return nil, fmt.Errorf("collect security definer functions: %w", err)
	}
	return fns, nil
}

func functionViolations(f functionInfo) []string {
	userIDParam := regexp.MustCompile(`(^|_)user_id$`)
	var out []string
	add := func(format string, args ...any) {
		out = append(out, fmt.Sprintf("[security-definer] function %s: ", f.Name)+fmt.Sprintf(format, args...))
	}
	if f.Owner != "app_security" {
		add("owned by %s: ALTER FUNCTION ... OWNER TO app_security (after granting EXECUTE)", f.Owner)
	}
	if !slices.Contains(f.Config, pinnedSearchPath) {
		add("search_path is not pinned: add SET %s", strings.Replace(pinnedSearchPath, "=", " = ", 1))
	}
	if f.PublicExecute {
		add("EXECUTE is granted to PUBLIC: REVOKE EXECUTE ON FUNCTION ... FROM PUBLIC")
	}
	if !slices.Equal(f.ExecuteGranted, []string{"app_runtime"}) {
		add("EXECUTE must be granted to app_runtime only, got %v", f.ExecuteGranted)
	}
	for _, arg := range f.ArgNames {
		if userIDParam.MatchString(arg) {
			add("parameter %q: the acting user comes from current_setting('app.user_id'), never from a parameter", arg)
		}
	}
	return out
}

// SecurityDefinerViolations applies the rules of section 7.4 to every SECURITY DEFINER
// function of the public schema that q can see. It returns their names (empty means the
// discovery query is broken) and the violations, each naming the rule and the fix.
func SecurityDefinerViolations(ctx context.Context, q querier) (names, violations []string, err error) {
	fns, err := loadSecurityDefiners(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range fns {
		names = append(names, f.Name)
		violations = append(violations, functionViolations(f)...)
	}
	return names, violations, nil
}
