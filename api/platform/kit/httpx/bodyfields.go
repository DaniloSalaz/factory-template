package httpx

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"

	"chaskicode.dev/saas/platform/apperr"
)

// KnownBodyFields rejects, with 400 unknown_field, a JSON object body with a member that
// its operation does not declare. The generated decoder ignores unknown members, so a
// field the contract does not have (a slug in PATCH /tenant) would be dropped silently:
// the caller would believe it took effect. table maps ServeMux patterns to the
// properties their body accepts, from the operations whose body schema says
// additionalProperties: false (gen/routes). Only the top level is checked.
//
// Anything else passes through untouched: routes outside table, bodies that are not a
// JSON object (the generated decoder answers them) and a body over BodyLimit, which is
// answered here with the same 413 the decoder would give.
func KnownBodyFields(mux *http.ServeMux, table map[string][]string, logger *slog.Logger) Middleware {
	known := make(map[string]map[string]struct{}, len(table))
	for pattern, fields := range table {
		set := make(map[string]struct{}, len(fields))
		for _, f := range fields {
			set[f] = struct{}{}
		}
		known[pattern] = set
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body == nil || r.Body == http.NoBody {
				next.ServeHTTP(w, r)
				return
			}
			_, pattern := mux.Handler(r)
			fields, ok := known[pattern]
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				WriteProblem(w, r, apperr.Wrap(err, apperr.KindBadRequest, "invalid_request",
					"The request body could not be read."), logger)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))

			var members map[string]json.RawMessage
			if json.Unmarshal(raw, &members) != nil {
				next.ServeHTTP(w, r)
				return
			}
			var unknown []apperr.FieldError
			for _, name := range slices.Sorted(maps.Keys(members)) {
				if _, ok := fields[name]; !ok {
					unknown = append(unknown, apperr.FieldError{Field: name, Code: "unknown", Message: "not a field of this request"})
				}
			}
			if len(unknown) > 0 {
				WriteProblem(w, r, unknownFields(unknown), logger)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// unknownFields is a 400, not a 422: the body does not match the contract at all, as a
// malformed one, rather than carrying a value the rules refuse.
func unknownFields(fields []apperr.FieldError) error {
	return &apperr.Error{
		Kind: apperr.KindBadRequest, Code: "unknown_field",
		Message: "The request has fields this operation does not accept.", Fields: fields,
	}
}
