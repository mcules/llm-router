package proxy

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mcules/llm-router/internal/state"
)

type ModelsHandler struct {
	Cluster *state.ClusterState
}

func NewModelsHandler(cluster *state.ClusterState) *ModelsHandler {
	return &ModelsHandler{Cluster: cluster}
}

type openAIModelsResponse struct {
	Object string        `json:"object"`
	Data   []openAIModel `json:"data"`
}

type openAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
	Created int64  `json:"created"`
}

func (h *ModelsHandler) HandleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}

	// Aggregate model IDs across all nodes.
	snap := h.Cluster.Snapshot()
	set := map[string]struct{}{}

	for _, n := range snap {
		for modelID := range n.Models {
			set[modelID] = struct{}{}
		}
	}

	modelIDs := make([]string, 0, len(set))
	for id := range set {
		modelIDs = append(modelIDs, id)
	}
	sort.Slice(modelIDs, func(i, j int) bool {
		return strings.ToLower(modelIDs[i]) < strings.ToLower(modelIDs[j])
	})

	now := time.Now().Unix()
	out := openAIModelsResponse{
		Object: "list",
		Data:   make([]openAIModel, 0, len(modelIDs)),
	}

	for _, id := range modelIDs {
		out.Data = append(out.Data, openAIModel{
			ID:      id,
			Object:  "model",
			OwnedBy: "llm-router",
			Created: now,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
