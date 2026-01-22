package ui

import (
	"github.com/mcules/llm-router/internal/policy"
	"net/http"
)

func (h *Handler) keys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.PolicyStore.ListAPIKeys(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	vm := h.newViewModel("API Keys")
	vm.Data = struct {
		Keys   []policy.APIKeyRecord
		NewKey string
	}{
		Keys:   keys,
		NewKey: r.URL.Query().Get("new_key"),
	}

	h.render(w, "keys.html", vm)
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := r.FormValue("name")
	if name == "" {
		name = "Unnamed Key"
	}

	key, _, err := h.Auth.GenerateKey(r.Context(), name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/ui/keys?new_key="+key, http.StatusSeeOther)
}

func (h *Handler) deleteKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := r.FormValue("id")
	if id == "" {
		http.Error(w, "Missing key ID", http.StatusBadRequest)
		return
	}

	if err := h.PolicyStore.DeleteAPIKey(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/ui/keys", http.StatusSeeOther)
}
