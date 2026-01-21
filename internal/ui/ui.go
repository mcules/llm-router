package ui

import (
	"html/template"
	"net/http"
	"path/filepath"
	"time"

	"your.module/internal/state"
)

// Comments in this file are intentionally in English.

type Handler struct {
	Cluster   *state.ClusterState
	templates *template.Template
}

func NewHandler(cluster *state.ClusterState, templateDir string) (*Handler, error) {
	tpl, err := template.ParseFiles(
		filepath.Join(templateDir, "layout.html"),
		filepath.Join(templateDir, "dashboard.html"),
		filepath.Join(templateDir, "nodes.html"),
		filepath.Join(templateDir, "models.html"),
	)
	if err != nil {
		return nil, err
	}

	return &Handler{Cluster: cluster, templates: tpl}, nil
}

func (h *Handler) Register(mux *http.ServeMux) {
	// UI root
	mux.HandleFunc("/ui/", h.dashboard)

	mux.HandleFunc("/ui/nodes", h.nodes)
	mux.HandleFunc("/ui/models", h.models)

	// Simple health endpoint for the server itself
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
}

type viewModel struct {
	Now   time.Time
	Nodes []*state.NodeSnapshot
	// Models view is computed in handler.
	Models []modelRow
}

type modelRow struct {
	ModelID     string
	NodeID      string
	State       string
	LastSeen    time.Time
	LoadedSince time.Time
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/ui/" && r.URL.Path != "/ui" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/ui" {
		http.Redirect(w, r, "/ui/", http.StatusFound)
		return
	}
	vm := viewModel{Now: time.Now(), Nodes: h.Cluster.Snapshot()}
	h.render(w, "dashboard.html", vm)
}

func (h *Handler) nodes(w http.ResponseWriter, r *http.Request) {
	vm := viewModel{Now: time.Now(), Nodes: h.Cluster.Snapshot()}
	h.render(w, "nodes.html", vm)
}

func (h *Handler) models(w http.ResponseWriter, r *http.Request) {
	nodes := h.Cluster.Snapshot()
	rows := make([]modelRow, 0, 256)

	for _, n := range nodes {
		for _, m := range n.Models {
			rows = append(rows, modelRow{
				ModelID:     m.ModelID,
				NodeID:      n.NodeID,
				State:       string(m.State),
				LastSeen:    m.LastSeen,
				LoadedSince: m.LoadedSince,
			})
		}
	}

	vm := viewModel{Now: time.Now(), Nodes: nodes, Models: rows}
	h.render(w, "models.html", vm)
}

func (h *Handler) render(w http.ResponseWriter, name string, vm viewModel) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = h.templates.ExecuteTemplate(w, "layout.html", map[string]any{
		"Page": name,
		"VM":   vm,
	})
}
