package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	controlplanev1 "your.module/gen/controlplane/v1"
	"your.module/internal/llama"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Comments in this file are intentionally in English.

func main() {
	nodeID := mustEnv("NODE_ID")
	serverAddr := mustEnv("SERVER_GRPC_ADDR")
	llamaBase := mustEnv("LLAMA_BASE_URL")
	meminfoPath := envOr("HOST_MEMINFO_PATH", "/host/proc/meminfo")

	heartbeatSec := envOrInt("HEARTBEAT_SECONDS", 1)
	pollModelsSec := envOrInt("POLL_MODELS_SECONDS", 5)
	pollSlotsSec := envOrInt("POLL_SLOTS_SECONDS", 1)

	ll := llama.New(llamaBase)

	conn, err := grpc.NewClient(serverAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("grpc dial: %v", err)
	}
	defer conn.Close()

	client := controlplanev1.NewNodeControlClient(conn)

	for {
		if err := runOnce(client, ll, nodeID, meminfoPath, heartbeatSec, pollModelsSec, pollSlotsSec); err != nil {
			log.Printf("stream ended: %v", err)
		}
		time.Sleep(2 * time.Second)
	}
}

func runOnce(
	client controlplanev1.NodeControlClient,
	ll *llama.Client,
	nodeID, meminfoPath string,
	heartbeatSec, pollModelsSec, pollSlotsSec int,
) error {
	ctx := context.Background()
	stream, err := client.Stream(ctx)
	if err != nil {
		return fmt.Errorf("stream open: %w", err)
	}

	// Send hello.
	if err := stream.Send(&controlplanev1.NodeMessage{
		Msg: &controlplanev1.NodeMessage_Hello{
			Hello: &controlplanev1.NodeHello{
				NodeId:       nodeID,
				Version:      "dev",
				LlamaBaseUrl: ll.BaseURL,
			},
		},
	}); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	// Receive loop (commands) in background.
	cmdErr := make(chan error, 1)
	go func() {
		for {
			in, err := stream.Recv()
			if err != nil {
				cmdErr <- err
				return
			}
			switch msg := in.Msg.(type) {
			case *controlplanev1.ServerMessage_UnloadModel:
				// Phase 1: command handling placeholder (we will implement unload in Phase 3).
				_ = msg
				_ = stream.Send(&controlplanev1.NodeMessage{
					Msg: &controlplanev1.NodeMessage_Ack{
						Ack: &controlplanev1.CommandAck{
							RequestId: msg.UnloadModel.RequestId,
							Ok:        false,
							Error:     "unload not implemented in phase 1",
						},
					},
				})
			default:
				// Ignore.
			}
		}
	}()

	var (
		lastModels   *llama.ModelsResponse
		lastModelsAt time.Time
		inflight     uint32
	)

	tHeartbeat := time.NewTicker(time.Duration(heartbeatSec) * time.Second)
	tModels := time.NewTicker(time.Duration(pollModelsSec) * time.Second)
	tSlots := time.NewTicker(time.Duration(pollSlotsSec) * time.Second)
	defer tHeartbeat.Stop()
	defer tModels.Stop()
	defer tSlots.Stop()

	// Prime initial reads quickly.
	_ = refreshModels(ctx, ll, &lastModels, &lastModelsAt)
	_ = refreshSlots(ctx, ll, &inflight)

	for {
		select {
		case err := <-cmdErr:
			return fmt.Errorf("recv loop: %w", err)

		case <-tSlots.C:
			_ = refreshSlots(ctx, ll, &inflight)

		case <-tModels.C:
			_ = refreshModels(ctx, ll, &lastModels, &lastModelsAt)

		case <-tHeartbeat.C:
			ramTotal, ramAvail, err := readMeminfo(meminfoPath)
			if err != nil {
				log.Printf("meminfo: %v", err)
				continue
			}

			status := &controlplanev1.NodeStatus{
				TsUnixMs:          time.Now().UnixMilli(),
				RamTotalBytes:     ramTotal,
				RamAvailableBytes: ramAvail,
				InflightRequests:  inflight,
				Models:            convertModels(lastModels),
			}

			if err := stream.Send(&controlplanev1.NodeMessage{
				Msg: &controlplanev1.NodeMessage_Status{Status: status},
			}); err != nil {
				return fmt.Errorf("send status: %w", err)
			}
		}
	}
}

func refreshModels(ctx context.Context, ll *llama.Client, last **llama.ModelsResponse, lastAt *time.Time) error {
	m, err := ll.GetModels(ctx)
	if err != nil {
		return err
	}
	*last = m
	*lastAt = time.Now()
	return nil
}

func refreshSlots(ctx context.Context, ll *llama.Client, inflight *uint32) error {
	n, err := ll.GetSlotsInflight(ctx)
	if err != nil {
		return err
	}
	*inflight = n
	return nil
}

func convertModels(m *llama.ModelsResponse) []*controlplanev1.ModelResidency {
	if m == nil {
		return nil
	}
	out := make([]*controlplanev1.ModelResidency, 0, len(m.Data))
	now := time.Now().UnixMilli()

	for _, x := range m.Data {
		out = append(out, &controlplanev1.ModelResidency{
			ModelId:           x.ID,
			State:             mapLlamaStatus(x.Status.Value, x.Status.Failed),
			LoadedSinceUnixMs: now, // best effort for phase 1
		})
	}
	return out
}

func mapLlamaStatus(value string, failed bool) controlplanev1.ModelState {
	if failed {
		return controlplanev1.ModelState_MODEL_STATE_ERROR
	}
	switch strings.ToLower(value) {
	case "loaded":
		return controlplanev1.ModelState_MODEL_STATE_READY
	case "loading":
		return controlplanev1.ModelState_MODEL_STATE_LOADING
	case "unloaded":
		return controlplanev1.ModelState_MODEL_STATE_UNLOADED
	default:
		return controlplanev1.ModelState_MODEL_STATE_UNLOADED
	}
}

func readMeminfo(path string) (totalBytes uint64, availBytes uint64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	var totalKB, availKB uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			totalKB = parseMeminfoKB(line)
		} else if strings.HasPrefix(line, "MemAvailable:") {
			availKB = parseMeminfoKB(line)
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	return totalKB * 1024, availKB * 1024, nil
}

func parseMeminfoKB(line string) uint64 {
	// Example: "MemAvailable:   123456 kB"
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	v, _ := strconv.ParseUint(fields[1], 10, 64)
	return v
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("missing env: %s", k)
	}
	return v
}

func envOr(k, def string) string {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	return v
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
