package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Purpose is what a login intent is for. The callback acts on the intent's purpose,
// never on anything in its URL (auth-y-tenancy §5.1.3). Only login exists: the step-up of the
// design (auth-y-tenancy §5.2) cannot be built on WorkOS (auth-y-tenancy §13).
type Purpose string

// PurposeLogin starts a new session.
const PurposeLogin Purpose = "login"

// Intent limits (auth-y-tenancy §5.1.2).
const (
	IntentTTL       = 5 * time.Minute
	MaxLiveIntents  = 3
	intentClockSkew = 30 * time.Second
	intentVersion   = "v1"
)

// Intent is a login in progress, kept by the browser between the redirect to the
// provider and the callback.
type Intent struct {
	State     string
	Verifier  string
	ReturnTo  string
	Purpose   Purpose
	StartedAt time.Time
}

// IntentCodec seals intents so the browser can hold them but not forge or alter them:
// the value is the JSON intent and an HMAC-SHA256 over the state and that JSON.
type IntentCodec struct {
	key []byte
}

// NewIntentCodec builds a codec from a key of at least 32 bytes.
func NewIntentCodec(key []byte) (IntentCodec, error) {
	if len(key) < randomBytes {
		return IntentCodec{}, fmt.Errorf("intent key has %d bytes, need at least %d", len(key), randomBytes)
	}
	return IntentCodec{key: slices.Clone(key)}, nil
}

type sealedIntent struct {
	Verifier  string  `json:"v"`
	ReturnTo  string  `json:"r"`
	Purpose   Purpose `json:"p"`
	StartedAt int64   `json:"t"`
}

// Seal returns the value to store for intent under its state.
func (c IntentCodec) Seal(intent Intent) (string, error) {
	payload, err := json.Marshal(sealedIntent{
		Verifier: intent.Verifier, ReturnTo: intent.ReturnTo, Purpose: intent.Purpose,
		StartedAt: intent.StartedAt.Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("encode intent: %w", err)
	}
	enc := base64.RawURLEncoding.EncodeToString(payload)
	return enc + "." + base64.RawURLEncoding.EncodeToString(c.mac(intent.State, enc)), nil
}

// Open verifies value as the sealed intent of state and returns it if it is still live.
func (c IntentCodec) Open(state, value string, now time.Time) (Intent, error) {
	if !WellFormed(state) {
		return Intent{}, ErrIntentInvalid()
	}
	enc, sig, ok := strings.Cut(value, ".")
	if !ok {
		return Intent{}, ErrIntentInvalid()
	}
	gotMAC, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(gotMAC, c.mac(state, enc)) {
		return Intent{}, ErrIntentInvalid()
	}
	payload, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return Intent{}, ErrIntentInvalid()
	}
	var s sealedIntent
	if err := json.Unmarshal(payload, &s); err != nil || s.Purpose != PurposeLogin || s.Verifier == "" {
		return Intent{}, ErrIntentInvalid()
	}
	started := time.Unix(s.StartedAt, 0)
	if now.Sub(started) > IntentTTL || started.After(now.Add(intentClockSkew)) {
		return Intent{}, ErrIntentInvalid()
	}
	return Intent{State: state, Verifier: s.Verifier, ReturnTo: s.ReturnTo, Purpose: s.Purpose, StartedAt: started}, nil
}

func (c IntentCodec) mac(state, enc string) []byte {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(intentVersion + "|" + state + "|" + enc))
	return m.Sum(nil)
}

// StoredIntent is an intent value the browser sent back, under its state.
type StoredIntent struct {
	State string
	Value string
}

// IntentsToDiscard returns the states to delete before storing one more intent: every
// invalid or expired one, and the oldest live ones so that, with the new one, at most
// MaxLiveIntents remain.
func (c IntentCodec) IntentsToDiscard(stored []StoredIntent, now time.Time) []string {
	var discard []string
	var live []Intent
	for _, s := range stored {
		intent, err := c.Open(s.State, s.Value, now)
		if err != nil {
			discard = append(discard, s.State)
			continue
		}
		live = append(live, intent)
	}
	slices.SortFunc(live, func(a, b Intent) int { return a.StartedAt.Compare(b.StartedAt) })
	for len(live) > MaxLiveIntents-1 {
		discard = append(discard, live[0].State)
		live = live[1:]
	}
	return discard
}
