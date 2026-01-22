package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"google.golang.org/grpc"

	controlplanev1 "github.com/mcules/llm-router/gen/controlplane/v1"
	"github.com/mcules/llm-router/internal/activity"
	"github.com/mcules/llm-router/internal/auth"
	"github.com/mcules/llm-router/internal/control"
	"github.com/mcules/llm-router/internal/httpx"
	"github.com/mcules/llm-router/internal/metrics"
	"github.com/mcules/llm-router/internal/planner"
	"github.com/mcules/llm-router/internal/policy"
	"github.com/mcules/llm-router/internal/proxy"
	"github.com/mcules/llm-router/internal/state"
	"github.com/mcules/llm-router/internal/ui"
)

// Comments in this file are intentionally in English.

func main() {
	// Cluster state shared across gRPC control plane, planner and HTTP API.
	cluster := state.NewClusterState()

	// In-memory store/logs.
	dbPath := os.Getenv("POLICIES_DB_PATH")
	if dbPath == "" {
		dbPath = "policies.db"
	}
	policyStore, err := policy.Open(dbPath)
	if err != nil {
		log.Fatalf("failed to open policy store: %v", err)
	}
	defer policyStore.Close()

	activityLog := activity.New(300)
	authenticator := auth.NewAuthenticator(policyStore)

	// Proxy router (API hot path).
	apiRouter := proxy.NewRouter(cluster, policyStore)
	apiRouter.NodeOfflineTTL = time.Duration(envOrInt("NODE_OFFLINE_SECONDS", 5)) * time.Second
	apiRouter.Latency = metrics.NewLatencyTracker(0.2)

	// gRPC server (control plane).
	grpcLis, err := net.Listen("tcp", ":9090")
	if err != nil {
		log.Fatalf("grpc listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	controlSvc := control.NewNodeControlService(cluster, apiRouter)
	controlplanev1.RegisterNodeControlServer(grpcServer, controlSvc)

	go func() {
		log.Printf("gRPC listening on :9090")
		if err := grpcServer.Serve(grpcLis); err != nil {
			log.Fatalf("grpc serve: %v", err)
		}
	}()

	// Periodic status polling (Server-side heartbeats/pings)
	go func() {
		interval := time.Duration(envOrInt("STATUS_POLL_INTERVAL_SECONDS", 10)) * time.Second
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				controlSvc.BroadcastPing()
			}
		}
	}()

	// Planner (unload/pressure/ttl automation).
	pl := &planner.Planner{
		Cluster:      cluster,
		Policies:     policyStore,
		Commands:     controlSvc,
		Activity:     activityLog,
		MinFreeBytes: uint64(envOrInt("MIN_FREE_RAM_MB", 2048)) * 1024 * 1024,
		Interval:     time.Duration(envOrInt("PLANNER_INTERVAL_SECONDS", 2)) * time.Second,
	}
	go pl.Run(context.Background())

	// HTTP server (UI + API on same port).
	mux := http.NewServeMux()

	// Root redirect to UI.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})

	// UI.
	uiHandler, err := ui.NewHandler(cluster, controlSvc, policyStore, activityLog, apiRouter.Latency, "internal/ui/templates")
	if err != nil {
		log.Fatalf("ui init: %v", err)
	}
	uiHandler.NodeOfflineTTL = apiRouter.NodeOfflineTTL
	uiHandler.Register(mux)

	// API endpoints.
	modelsHandler := proxy.NewModelsHandler(cluster)

	// Create a sub-mux or just wrap the handlers for API.
	// For simplicity, we wrap the individual handlers if they need auth.
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/v1/models", modelsHandler.HandleModels)
	apiMux.HandleFunc("/v1/chat/completions", apiRouter.HandleChatCompletions)
	apiMux.HandleFunc("/v1/embeddings", apiRouter.HandleEmbeddings)
	apiMux.HandleFunc("/v1/completions", apiRouter.HandleCompletions)

	// Register the API mux into the main mux, wrapped with Auth middleware.
	mux.Handle("/v1/", authenticator.Middleware(apiMux))

	// Wrap mux with CORS (optional but recommended).
	handler := httpx.CORS{AllowOrigin: "*"}.Wrap(mux)

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		// Important: do not set WriteTimeout for streaming responses.
		IdleTimeout: 120 * time.Second,
	}

	log.Printf("HTTP listening on :8080")
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("http serve: %v", err)
	}
}

func envOrInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
