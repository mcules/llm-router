package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/mcules/llm-router/internal/policy"
)

type Authenticator struct {
	Store *policy.Store
}

func NewAuthenticator(store *policy.Store) *Authenticator {
	return &Authenticator{Store: store}
}

// GenerateKey erzeugt einen neuen API-Key (Plaintext) und den zugehörigen Record.
func (a *Authenticator) GenerateKey(ctx context.Context, name string) (string, policy.APIKeyRecord, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", policy.APIKeyRecord{}, err
	}
	key := "sk-" + hex.EncodeToString(raw)

	id := hex.EncodeToString(raw[:8])
	prefix := key[:7] // sk-xxxx

	hash := sha256.Sum256([]byte(key))
	hashedKey := hex.EncodeToString(hash[:])

	record := policy.APIKeyRecord{
		ID:        id,
		Name:      name,
		Prefix:    prefix,
		HashedKey: hashedKey,
		CreatedAt: time.Now(),
	}

	if err := a.Store.CreateAPIKey(ctx, record); err != nil {
		return "", policy.APIKeyRecord{}, err
	}

	return key, record, nil
}

// Middleware prüft den Authorization Header.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Missing Authorization header", http.StatusUnauthorized)
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			http.Error(w, "Invalid Authorization header format", http.StatusUnauthorized)
			return
		}

		key := parts[1]
		hash := sha256.Sum256([]byte(key))
		hashedKey := hex.EncodeToString(hash[:])

		keys, err := a.Store.ListAPIKeys(r.Context())
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		var found *policy.APIKeyRecord
		for _, k := range keys {
			if k.HashedKey == hashedKey {
				found = &k
				break
			}
		}

		if found == nil {
			http.Error(w, "Invalid API key", http.StatusUnauthorized)
			return
		}

		// Update last used (asynchron)
		go func() {
			_ = a.Store.UpdateAPIKeyLastUsed(context.Background(), found.ID)
		}()

		next.ServeHTTP(w, r)
	})
}
