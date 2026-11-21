package db

import (
	"strings"
	"unicode"
)

// RLSVarsQueryName names, in traces, the statement of every transaction that sets its
// row-level security variables (WithTenantTx, WithUserTx, WithAnonTx…). Only this
// package knows that statement; the rest of the code sees this name.
const RLSVarsQueryName = "rls.set_vars"

// QueryName names a query for traces and metrics: its sqlc name ("-- name: ListMembers
// :many"), RLSVarsQueryName, or its first keyword (SELECT, BEGIN…). It always comes
// from the code, never from a value.
func QueryName(sql string) string {
	trimmed := strings.TrimSpace(sql)
	if rest, ok := strings.CutPrefix(trimmed, "-- name: "); ok {
		if name, _, ok := strings.Cut(rest, " "); ok && name != "" {
			return name
		}
	}
	if strings.Contains(sql, "set_config(") {
		return RLSVarsQueryName
	}
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		word, _, _ := strings.Cut(line, " ")
		return strings.ToUpper(strings.TrimRightFunc(word, func(r rune) bool { return !unicode.IsLetter(r) }))
	}
	return "UNKNOWN"
}
