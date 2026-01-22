package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/mcules/llm-router/internal/policy"
	"golang.org/x/crypto/bcrypt"
)

type Authenticator struct {
	Store *policy.Store
}

func NewAuthenticator(store *policy.Store) *Authenticator {
	// Sicherstellen, dass der Standard-Admin-User existiert
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, exists, _ := store.GetUser(ctx, "admin")
	if !exists {
		hash, _ := bcrypt.GenerateFromPassword([]byte("admin"), bcrypt.DefaultCost)
		_ = store.CreateUser(ctx, policy.UserRecord{
			Username:      "admin",
			PasswordHash:  string(hash),
			AllowedNodes:  "*",
			AllowedModels: "*",
		})
	}

	return &Authenticator{Store: store}
}

// GenerateKey erzeugt einen neuen API-Key (Plaintext) und den zugehörigen Record.
func (a *Authenticator) GenerateKey(ctx context.Context, name string, allowedNodes, allowedModels string) (string, policy.APIKeyRecord, error) {
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
		ID:            id,
		Name:          name,
		Prefix:        prefix,
		HashedKey:     hashedKey,
		CreatedAt:     time.Now(),
		AllowedNodes:  allowedNodes,
		AllowedModels: allowedModels,
	}

	if err := a.Store.CreateAPIKey(ctx, record); err != nil {
		return "", policy.APIKeyRecord{}, err
	}

	return key, record, nil
}

func (a *Authenticator) AuthenticateUser(ctx context.Context, username, password string) (policy.UserRecord, error) {
	u, exists, err := a.Store.GetUser(ctx, username)
	if err != nil {
		return policy.UserRecord{}, err
	}
	if !exists {
		return policy.UserRecord{}, errors.New("user not found")
	}

	err = bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password))
	if err != nil {
		return policy.UserRecord{}, errors.New("invalid password")
	}

	return u, nil
}

func (a *Authenticator) CreateUser(ctx context.Context, username, password, allowedNodes, allowedModels string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	return a.Store.CreateUser(ctx, policy.UserRecord{
		Username:      username,
		PasswordHash:  string(hash),
		AllowedNodes:  allowedNodes,
		AllowedModels: allowedModels,
	})
}

func (a *Authenticator) UpdateUser(ctx context.Context, username, allowedNodes, allowedModels string) error {
	return a.Store.UpdateUser(ctx, policy.UserRecord{
		Username:      username,
		AllowedNodes:  allowedNodes,
		AllowedModels: allowedModels,
	})
}

func (a *Authenticator) ChangePassword(ctx context.Context, username, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return a.Store.UpdateUserPassword(ctx, username, string(hash))
}

// CheckACL prüft, ob ein Modell und eine Node für einen ACL-String erlaubt sind.
func CheckACL(allowedStr, actualValue string) bool {
	if allowedStr == "*" || allowedStr == "" {
		return true
	}
	parts := strings.Split(allowedStr, ",")
	for _, p := range parts {
		if strings.TrimSpace(p) == actualValue {
			return true
		}
	}
	return false
}

type ctxKeyAuthRecord struct{}

func GetAuthRecord(r *http.Request) *policy.APIKeyRecord {
	if v := r.Context().Value(ctxKeyAuthRecord{}); v != nil {
		return v.(*policy.APIKeyRecord)
	}
	return nil
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

		// Record in context speichern für ACL Checks im Proxy
		ctx := context.WithValue(r.Context(), ctxKeyAuthRecord{}, found)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
