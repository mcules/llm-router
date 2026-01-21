package ui

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
	"time"

	"github.com/mcules/llm-router/internal/activity"
	"github.com/mcules/llm-router/internal/metrics"
	"github.com/mcules/llm-router/internal/policy"
	"github.com/mcules/llm-router/internal/state"
)

type CommandSender interface {
	SendUnload(nodeID, requestID, modelID string) error
}

type Handler struct {
	Cluster     *state.ClusterState
	Commands    CommandSender
	PolicyStore *policy.Store
	Activity    *activity.Log
	Latency     *metrics.LatencyTracker
	templateDir string
}

type viewModel struct {
	Now       time.Time
	Nodes     []*state.NodeSnapshot
	Models    []modelRow
	Policies  []PolicyViewRow
	NodeViews []nodeView
	Activity  []activityRow
}

type nodeView struct {
	NodeID        string
	Online        bool
	LastHeartbeat time.Time
	Age           string
	RAMAvail      uint64
	RAMTotal      uint64
	Inflight      uint32
	DataPlaneURL  string

	EWMAms  string
	ErrRate string
}

type modelRow struct {
	ModelID     string
	NodeID      string
	State       string
	LastSeen    time.Time
	LoadedSince time.Time
}

func NewHandler(cluster *state.ClusterState, commands CommandSender, store *policy.Store, act *activity.Log, lat *metrics.LatencyTracker, templateDir string) (*Handler, error) {
	return &Handler{
		Cluster:     cluster,
		Commands:    commands,
		PolicyStore: store,
		Activity:    act,
		Latency:     lat,
		templateDir: templateDir,
	}, nil
}

func (h *Handler) Register(mux *http.ServeMux) {
	// UI root
	mux.HandleFunc("/ui/", h.dashboard)

	mux.HandleFunc("/ui/nodes", h.nodes)
	mux.HandleFunc("/ui/models", h.models)
	mux.HandleFunc("/ui/models/unload", h.unloadModel)
	mux.HandleFunc("/ui/events", h.events)

	mux.HandleFunc("/ui/policies", h.policies)
	mux.HandleFunc("/ui/policies/save", h.savePolicy)

	mux.HandleFunc("/ui/activity", h.activity)

	// Simple health endpoint for the server itself
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
}

func (h *Handler) render(w http.ResponseWriter, page string, vm viewModel) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	tpl, err := template.ParseFiles(
		filepath.Join(h.templateDir, "layout.html"),
		filepath.Join(h.templateDir, page),
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := tpl.ExecuteTemplate(w, page, vm); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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
	now := time.Now()
	nodes := h.Cluster.Snapshot()

	ttl := 5 * time.Second
	views := make([]nodeView, 0, len(nodes))

	for _, n := range nodes {
		online := n.IsOnline(now, ttl)

		age := "n/a"
		if !n.LastHeartbeat.IsZero() {
			age = now.Sub(n.LastHeartbeat).Truncate(100 * time.Millisecond).String()
		}

		ewma := "n/a"
		errRate := "n/a"
		if h.Latency != nil {
			if l, ok := h.Latency.Get(n.NodeID); ok {
				if l.EWMAms > 0 {
					ewma = fmt.Sprintf("%.0fms", l.EWMAms)
				}
				total := l.OK + l.Error
				if total > 0 {
					er := (float64(l.Error) / float64(total)) * 100.0
					errRate = fmt.Sprintf("%.1f%%", er)
				} else {
					errRate = "0.0%"
				}
			}
		}

		views = append(views, nodeView{
			NodeID:        n.NodeID,
			Online:        online,
			LastHeartbeat: n.LastHeartbeat,
			Age:           age,
			RAMAvail:      n.RAMAvailBytes,
			RAMTotal:      n.RAMTotalBytes,
			Inflight:      n.InflightRequests,
			DataPlaneURL:  n.DataPlaneURL,
			EWMAms:        ewma,
			ErrRate:       errRate,
		})
	}

	vm := viewModel{
		Now:       now,
		Nodes:     nodes,
		NodeViews: views,
	}
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

func (h *Handler) unloadModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}

	nodeID := r.FormValue("node_id")
	modelID := r.FormValue("model_id")
	if nodeID == "" || modelID == "" {
		http.Error(w, "missing node_id or model_id", http.StatusBadRequest)
		return
	}

	reqID := fmt.Sprintf("unload-%d", time.Now().UnixNano())
	if err := h.Commands.SendUnload(nodeID, reqID, modelID); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// Log activity event (optional).
	if h.Activity != nil {
		h.Activity.Add(activity.Event{
			At:     time.Now(),
			Type:   activity.EventManualUnload,
			NodeID: nodeID,
			Model:  modelID,
			Note:   "ui",
		})
	}

	http.Redirect(w, r, "/ui/models", http.StatusFound)
}

func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	t := time.NewTicker(1 * time.Second)
	defer t.Stop()

	enc := json.NewEncoder(w)

	for {
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
			snap := h.Cluster.Snapshot()
			payload := map[string]any{
				"ts":    time.Now().UnixMilli(),
				"nodes": snap,
			}

			_, _ = w.Write([]byte("event: snapshot\n"))
			_, _ = w.Write([]byte("data: "))
			_ = enc.Encode(payload)
			_, _ = w.Write([]byte("\n"))

			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
}
