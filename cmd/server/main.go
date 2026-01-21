package main

import (
	"log"
	"net"
	"net/http"
	"time"

	controlplanev1 "your.module/gen/controlplane/v1"
	"your.module/internal/control"
	"your.module/internal/state"
	"your.module/internal/ui"

	"google.golang.org/grpc"
)

// Comments in this file are intentionally in English.

func main() {
	cluster := state.NewClusterState()

	// gRPC server
	grpcLis, err := net.Listen("tcp", ":9090")
	if err != nil {
		log.Fatalf("grpc listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	controlplanev1.RegisterNodeControlServer(grpcServer, control.NewNodeControlService(cluster))

	go func() {
		log.Printf("gRPC listening on :9090")
		if err := grpcServer.Serve(grpcLis); err != nil {
			log.Fatalf("grpc serve: %v", err)
		}
	}()

	// HTTP server (UI now; API later)
	mux := http.NewServeMux()

	// Root redirect to UI.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})

	uiHandler, err := ui.NewHandler(cluster, "internal/ui/templates")
	if err != nil {
		log.Fatalf("ui templates: %v", err)
	}
	uiHandler.Register(mux)

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("HTTP listening on :8080")
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("http serve: %v", err)
	}
}
