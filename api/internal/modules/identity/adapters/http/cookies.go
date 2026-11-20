package http

import (
	"net/http"
	"strings"
	"time"

	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/domain"
)

// Cookie names (design, 5.1.2 and section 6). The __Host- prefix makes the browser
// require Secure, Path=/ and no Domain, so no subdomain can set or overwrite them.
const (
	SessionCookie = "__Host-session"
	IntentPrefix  = "__Host-oauth_"
)

func sessionCookie(token string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	}
}

func clearSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name: SessionCookie, Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	}
}

func intentCookie(state, value string) *http.Cookie {
	return &http.Cookie{
		Name: IntentPrefix + state, Value: value, Path: "/", MaxAge: int(domain.IntentTTL / time.Second),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	}
}

func clearIntentCookie(state string) *http.Cookie {
	return &http.Cookie{
		Name: IntentPrefix + state, Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	}
}

// storedIntents returns every intent cookie the browser sent.
func storedIntents(r *http.Request) []domain.StoredIntent {
	var out []domain.StoredIntent
	for _, c := range r.Cookies() {
		if state, ok := strings.CutPrefix(c.Name, IntentPrefix); ok {
			out = append(out, domain.StoredIntent{State: state, Value: c.Value})
		}
	}
	return out
}

func cookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}
