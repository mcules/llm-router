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

	vm := h.newViewModel("API Keys")
	vm.User = h.getUser(r)
	vm.Data = struct {
		Keys      []policy.APIKeyRecord
		NewKey    string
		AllNodes  []string
		AllModels []string
	}{
		Keys:      keys,
		NewKey:    r.URL.Query().Get("new_key"),
		AllNodes:  mapToSortedSlice(allNodes),
		AllModels: mapToSortedSlice(allModels),
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

	nodes := r.FormValue("allowed_nodes")
	models := r.FormValue("allowed_models")

	key, _, err := h.Auth.GenerateKey(r.Context(), name, nodes, models)
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
