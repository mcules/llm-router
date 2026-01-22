package ui

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mcules/llm-router/internal/activity"
	"github.com/mcules/llm-router/internal/auth"
	"github.com/mcules/llm-router/internal/metrics"
	"github.com/mcules/llm-router/internal/policy"
	"github.com/mcules/llm-router/internal/state"
)

type CommandSender interface {
	SendUnload(nodeID, requestID, modelID string) error
}

type Handler struct {
	Cluster        *state.ClusterState
	Commands       CommandSender
	PolicyStore    *policy.Store
	Auth           *auth.Authenticator
	Activity       *activity.Log
	Latency        *metrics.LatencyTracker
	templateDir    string
	templates      map[string]*template.Template
	NodeOfflineTTL time.Duration
}

type viewModel struct {
	Title     string
	Now       time.Time
	Nodes     []*state.NodeSnapshot
	Models    []modelGroup
	Policies  []PolicyViewRow
	NodeViews []nodeView
	Activity  []activityRow
	User      *policy.UserRecord
	Data      any
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

	EWMAms  float64
	ErrRate float64
}

type modelGroup struct {
	ModelID string
	Nodes   []modelNodeInfo
}

type modelNodeInfo struct {
	NodeID      string
	State       string
	LastSeen    time.Time
	LoadedSince time.Time
}

func NewHandler(cluster *state.ClusterState, commands CommandSender, store *policy.Store, act *activity.Log, lat *metrics.LatencyTracker, templateDir string) (*Handler, error) {
	h := &Handler{
		Cluster:        cluster,
		Commands:       commands,
		PolicyStore:    store,
		Auth:           auth.NewAuthenticator(store),
		Activity:       act,
		Latency:        lat,
		templateDir:    templateDir,
		templates:      make(map[string]*template.Template),
		NodeOfflineTTL: 5 * time.Second,
	}

	funcMap := template.FuncMap{
		"formatRAM": func(b uint64) string {
			if b == 0 {
				return "0 GB"
			}
			return fmt.Sprintf("%.2f GB", float64(b)/(1024*1024*1024))
		},
		"formatTime": func(t time.Time) string {
			if t.IsZero() {
				return "n/a"
			}
			return t.Format("02.01.2006 15:04:05")
		},
		"upper": strings.ToUpper,
	}

	pages := []string{"dashboard.html", "nodes.html", "models.html", "policies.html", "activity.html", "keys.html", "login.html", "users.html"}
	for _, page := range pages {
		tpl := template.New(page).Funcs(funcMap)
		tpl, err := tpl.ParseFiles(
			filepath.Join(templateDir, "layout.html"),
			filepath.Join(templateDir, page),
		)
		if err != nil {
			return nil, fmt.Errorf("parse template %s: %w", page, err)
		}
		h.templates[page] = tpl
	}

	return h, nil
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/ui/login", h.login)
	mux.HandleFunc("/ui/logout", h.logout)

	// UI root
	mux.HandleFunc("/ui/", h.authMiddleware(h.dashboard))

	mux.HandleFunc("/ui/nodes", h.authMiddleware(h.nodes))
	mux.HandleFunc("/ui/models", h.authMiddleware(h.models))
	mux.HandleFunc("/ui/models/unload", h.authMiddleware(h.unloadModel))
	mux.HandleFunc("/ui/events", h.events) // SSE normally doesn't need auth if pages are protected

	mux.HandleFunc("/ui/policies", h.authMiddleware(h.policies))
	mux.HandleFunc("/ui/policies/save", h.authMiddleware(h.savePolicy))
	mux.HandleFunc("/ui/policies/delete", h.authMiddleware(h.deletePolicy))
	mux.HandleFunc("/ui/policies/upsert", h.authMiddleware(h.upsertPolicy))

	mux.HandleFunc("/ui/keys", h.authMiddleware(h.keys))
	mux.HandleFunc("/ui/keys/create", h.authMiddleware(h.createKey))
	mux.HandleFunc("/ui/keys/delete", h.authMiddleware(h.deleteKey))

	mux.HandleFunc("/ui/users", h.authMiddleware(h.users))
	mux.HandleFunc("/ui/users/create", h.authMiddleware(h.createUser))
	mux.HandleFunc("/ui/users/update", h.authMiddleware(h.updateUser))
	mux.HandleFunc("/ui/users/delete", h.authMiddleware(h.deleteUser))
	mux.HandleFunc("/ui/users/password", h.authMiddleware(h.changePassword))

	mux.HandleFunc("/ui/activity", h.authMiddleware(h.activity))

	// Simple health endpoint for the server itself
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
}

func (h *Handler) render(w http.ResponseWriter, page string, vm viewModel) {
	tpl, ok := h.templates[page]
	if !ok {
		http.Error(w, fmt.Sprintf("template %s not found", page), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Cache-Control to prevent potential hanging on slow clients or proxies
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")

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
	vm := h.newViewModel("Dashboard")
	nodes := h.Cluster.Snapshot()
	sort.Slice(nodes, func(i, j int) bool {
		return strings.ToLower(nodes[i].NodeID) < strings.ToLower(nodes[j].NodeID)
	})
	vm.Nodes = nodes
	vm.User = h.getUser(r)
	h.render(w, "dashboard.html", vm)
}

func (h *Handler) nodes(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	nodes := h.Cluster.Snapshot()
	user := h.getUser(r)

	ttl := h.NodeOfflineTTL
	views := make([]nodeView, 0, len(nodes))

	for _, n := range nodes {
		if user != nil && !auth.CheckACL(user.AllowedNodes, n.NodeID) {
			continue
		}
		online := n.IsOnline(now, ttl)
		log.Printf("DEBUG: UI nodes: node %s online=%v (last: %v, ttl: %v, now: %v)", n.NodeID, online, n.LastHeartbeat.Format("15:04:05.000"), ttl, now.Format("15:04:05.000"))

		age := "n/a"
		if !n.LastHeartbeat.IsZero() {
			age = now.Sub(n.LastHeartbeat).Truncate(100 * time.Millisecond).String()
		}

		var ewma float64
		var errRate float64
		if h.Latency != nil {
			if l, ok := h.Latency.Get(n.NodeID); ok {
				ewma = l.EWMAms
				total := l.OK + l.Error
				if total > 0 {
					errRate = (float64(l.Error) / float64(total)) * 100.0
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

	vm := h.newViewModel("Nodes")
	sort.Slice(views, func(i, j int) bool {
		return strings.ToLower(views[i].NodeID) < strings.ToLower(views[j].NodeID)
	})
	vm.NodeViews = views
	vm.User = user
	h.render(w, "nodes.html", vm)
}

func (h *Handler) models(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	ttl := h.NodeOfflineTTL
	nodes := h.Cluster.Snapshot()
	user := h.getUser(r)

	groupsMap := make(map[string]*modelGroup)

	for _, n := range nodes {
		if user != nil && !auth.CheckACL(user.AllowedNodes, n.NodeID) {
			continue
		}
		online := n.IsOnline(now, ttl)
		log.Printf("DEBUG: UI models: node %s online=%v (last: %v, ttl: %v, now: %v)", n.NodeID, online, n.LastHeartbeat.Format("15:04:05.000"), ttl, now.Format("15:04:05.000"))
		if !online {
			continue
		}

		for _, m := range n.Models {
			if user != nil && !auth.CheckACL(user.AllowedModels, m.ModelID) {
				continue
			}

			group, ok := groupsMap[m.ModelID]
			if !ok {
				group = &modelGroup{ModelID: m.ModelID}
				groupsMap[m.ModelID] = group
			}

			group.Nodes = append(group.Nodes, modelNodeInfo{
				NodeID:      n.NodeID,
				State:       string(m.State),
				LastSeen:    m.LastSeen,
				LoadedSince: m.LoadedSince,
			})
		}
	}

	groups := make([]modelGroup, 0, len(groupsMap))
	for _, g := range groupsMap {
		sort.Slice(g.Nodes, func(i, j int) bool {
			return g.Nodes[i].NodeID < g.Nodes[j].NodeID
		})
		groups = append(groups, *g)
	}

	sort.Slice(groups, func(i, j int) bool {
		return strings.ToLower(groups[i].ModelID) < strings.ToLower(groups[j].ModelID)
	})

	vm := h.newViewModel("Models")
	vm.Models = groups
	vm.User = user
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
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Send initial pulse
	_, _ = fmt.Fprintf(w, ": ok\n\n")
	flusher.Flush()

	t := time.NewTicker(2 * time.Second)
	defer t.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
			snap := h.Cluster.Snapshot()
			payload, _ := json.Marshal(map[string]any{
				"ts":    time.Now().UnixMilli(),
				"nodes": snap,
			})

			_, err := fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", payload)
			if err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (h *Handler) newViewModel(title string) viewModel {
	return viewModel{
		Title: title,
		Now:   time.Now(),
		Nodes: h.Cluster.Snapshot(),
	}
}
