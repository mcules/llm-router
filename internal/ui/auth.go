package ui

import (
	"context"
	"net/http"
	"sort"

	"github.com/mcules/llm-router/internal/policy"
)

type ctxKeyUser struct{}

func (h *Handler) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session")
		if err != nil {
			http.Redirect(w, r, "/ui/login", http.StatusFound)
			return
		}

		// Einfaches Session-Handling: Cookie-Wert ist der Username
		// In einer echten App würde man hier ein sicheres Session-Token verwenden.
		username := cookie.Value
		u, exists, err := h.PolicyStore.GetUser(r.Context(), username)
		if err != nil || !exists {
			http.Redirect(w, r, "/ui/login", http.StatusFound)
			return
		}

		ctx := context.WithValue(r.Context(), ctxKeyUser{}, &u)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.render(w, "login.html", h.newViewModel("Login"))
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	u, err := h.Auth.AuthenticateUser(r.Context(), username, password)
	if err != nil {
		vm := h.newViewModel("Login")
		vm.Data = "Ungültiger Benutzername oder Passwort"
		h.render(w, "login.html", vm)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    u.Username,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   86400,
	})

	http.Redirect(w, r, "/ui/", http.StatusFound)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/ui/login", http.StatusFound)
}

func (h *Handler) users(w http.ResponseWriter, r *http.Request) {
	users, err := h.PolicyStore.ListUsers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Aggregate all known models and nodes for the dropdowns
	nodes := h.Cluster.Snapshot()
	allNodes := make(map[string]struct{})
	allModels := make(map[string]struct{})
	for _, n := range nodes {
		allNodes[n.NodeID] = struct{}{}
		for modelID := range n.Models {
			allModels[modelID] = struct{}{}
		}
	}

	vm := h.newViewModel("Users")
	vm.Data = struct {
		Users     []policy.UserRecord
		AllNodes  []string
		AllModels []string
	}{
		Users:     users,
		AllNodes:  mapToSortedSlice(allNodes),
		AllModels: mapToSortedSlice(allModels),
	}
	h.render(w, "users.html", vm)
}

func mapToSortedSlice(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (h *Handler) updateUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	username := r.FormValue("username")
	nodes := r.FormValue("allowed_nodes")
	models := r.FormValue("allowed_models")

	if username == "" {
		http.Error(w, "Username required", http.StatusBadRequest)
		return
	}

	if err := h.Auth.UpdateUser(r.Context(), username, nodes, models); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/ui/users", http.StatusSeeOther)
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Password can be changed for self, or by admin for others
	currentUser := h.getUser(r)
	targetUser := r.FormValue("username")
	newPassword := r.FormValue("password")

	if targetUser == "" {
		targetUser = currentUser.Username
	}

	if currentUser.Username != "admin" && currentUser.Username != targetUser {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if newPassword == "" {
		http.Error(w, "Password required", http.StatusBadRequest)
		return
	}

	if err := h.Auth.ChangePassword(r.Context(), targetUser, newPassword); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// If changing own password, maybe redirect to login?
	// For now, just back to users or dashboard
	if currentUser.Username == "admin" && targetUser != "admin" {
		http.Redirect(w, r, "/ui/users", http.StatusSeeOther)
	} else {
		http.Redirect(w, r, "/ui/", http.StatusSeeOther)
	}
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")
	nodes := r.FormValue("allowed_nodes")
	models := r.FormValue("allowed_models")

	if username == "" || password == "" {
		http.Error(w, "Username and password required", http.StatusBadRequest)
		return
	}

	err := h.Auth.CreateUser(r.Context(), username, password, nodes, models)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/ui/users", http.StatusSeeOther)
}

func (h *Handler) deleteUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	username := r.FormValue("username")
	if username == "admin" {
		http.Error(w, "Cannot delete admin user", http.StatusForbidden)
		return
	}

	if err := h.PolicyStore.DeleteUser(r.Context(), username); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/ui/users", http.StatusSeeOther)
}

func (h *Handler) getUser(r *http.Request) *policy.UserRecord {
	if v := r.Context().Value(ctxKeyUser{}); v != nil {
		return v.(*policy.UserRecord)
	}
	return nil
}
