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

	controlplanev1 "github.com/mcules/llm-router/gen/controlplane/v1"
	"github.com/mcules/llm-router/internal/llama"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	nodeID := mustEnv("NODE_ID")
	serverAddr := mustEnv("SERVER_GRPC_ADDR")

	// Internal URL for agent->llama (same docker network as llama container)
	llamaBase := mustEnv("LLAMA_BASE_URL")

	// External URL for server->llama (must be reachable from server)
	dataPlane := envOr("DATA_PLANE_URL", llamaBase)

	meminfoPath := envOr("HOST_MEMINFO_PATH", "/host/proc/meminfo")

	heartbeatSec := envOrInt("HEARTBEAT_SECONDS", 1)
	pollModelsBaseSec := envOrInt("POLL_MODELS_SECONDS", 5)
	pollSlotsSec := envOrInt("POLL_SLOTS_SECONDS", 1)

	ll := llama.New(llamaBase)

	conn, err := grpc.NewClient(serverAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("grpc dial: %v", err)
	}
	defer conn.Close()

	client := controlplanev1.NewNodeControlClient(conn)

	for {
		if err := runOnce(client, ll, nodeID, meminfoPath, dataPlane, heartbeatSec, pollModelsBaseSec, pollSlotsSec); err != nil {
			log.Printf("stream ended: %v", err)
		}
		time.Sleep(2 * time.Second)
	}
}

func runOnce(
	client controlplanev1.NodeControlClient,
	ll *llama.Client,
	nodeID, meminfoPath, dataPlaneURL string,
	heartbeatSec, pollModelsBaseSec, pollSlotsSec int,
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
				DataPlaneUrl: dataPlaneURL,
			},
		},
	}); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	// Receive loop (commands and pings) in background.
	cmdErr := make(chan error, 1)
	// We use a channel to trigger immediate status updates on Ping
	pingTrigger := make(chan struct{}, 1)

	go func() {
		for {
			in, err := stream.Recv()
			if err != nil {
				cmdErr <- err
				return
			}
			switch msg := in.Msg.(type) {
			case *controlplanev1.ServerMessage_UnloadModel:
				reqID := msg.UnloadModel.RequestId
				modelID := msg.UnloadModel.ModelId

				err := ll.UnloadModel(context.Background(), modelID)
				ack := &controlplanev1.CommandAck{
					RequestId: reqID,
					Ok:        err == nil,
				}
				if err != nil {
					ack.Error = err.Error()
				}

				_ = stream.Send(&controlplanev1.NodeMessage{
					Msg: &controlplanev1.NodeMessage_Ack{Ack: ack},
				})
			case *controlplanev1.ServerMessage_Ping:
				// Trigger immediate status send
				select {
				case pingTrigger <- struct{}{}:
				default:
				}
			default:
				// Ignore.
			}
		}
	}()

	var (
		lastModels *llama.ModelsResponse
		inflight   uint32
	)

	// Prime initial reads quickly.
	_ = refreshModels(ctx, ll, &lastModels)
	_ = refreshSlots(ctx, ll, &inflight)

	tHeartbeat := time.NewTicker(time.Duration(heartbeatSec) * time.Second)
	defer tHeartbeat.Stop()

	// Models polling: dynamic (fast while any model is loading)
	modelsTicker := time.NewTicker(time.Duration(pollModelsBaseSec) * time.Second)
	defer modelsTicker.Stop()

	tSlots := time.NewTicker(time.Duration(pollSlotsSec) * time.Second)
	defer tSlots.Stop()

	for {
		// Helper function to send status
		sendStatus := func() error {
			ramTotal, ramAvail, err := readMeminfo(meminfoPath)
			if err != nil {
				log.Printf("meminfo: %v", err)
				return nil // continue loop
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
			return nil
		}

		select {
		case err := <-cmdErr:
			return fmt.Errorf("recv loop: %w", err)

		case <-pingTrigger:
			if err := sendStatus(); err != nil {
				return err
			}

		case <-tSlots.C:
			_ = refreshSlots(ctx, ll, &inflight)

		case <-modelsTicker.C:
			_ = refreshModels(ctx, ll, &lastModels)

			// If any model is loading, temporarily poll faster (1s).
			if anyLoading(lastModels) && pollModelsBaseSec > 1 {
				modelsTicker.Reset(1 * time.Second)
			} else {
				modelsTicker.Reset(time.Duration(pollModelsBaseSec) * time.Second)
			}

		case <-tHeartbeat.C:
			if err := sendStatus(); err != nil {
				return err
			}
		}
	}
}

func refreshModels(ctx context.Context, ll *llama.Client, last **llama.ModelsResponse) error {
	m, err := ll.GetModels(ctx)
	if err != nil {
		return err
	}
	*last = m
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

func anyLoading(m *llama.ModelsResponse) bool {
	if m == nil {
		return false
	}
	for _, x := range m.Data {
		if strings.EqualFold(x.Status.Value, "loading") {
			return true
		}
	}
	return false
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
			LoadedSinceUnixMs: now, // best effort for now
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
	// Try the provided path (likely /proc/meminfo)
	f, err := os.Open(path)
	if err == nil {
		defer f.Close()
		var totalKB, availKB uint64
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "MemTotal:") {
				totalKB = parseMeminfoKB(line)
			} else if strings.HasPrefix(line, "MemAvailable:") {
				availKB = parseMeminfoKB(line)
			} else if strings.HasPrefix(line, "MemFree:") && availKB == 0 {
				// Fallback to MemFree if MemAvailable is not present (older kernels)
				availKB = parseMeminfoKB(line)
			}
		}
		if sc.Err() == nil && totalKB > 0 {
			return totalKB * 1024, availKB * 1024, nil
		}
	}

	// Fallback for development (Windows/Darwin or missing /proc/meminfo)
	// Return some static values so the agent can still run locally.
	return 16 * 1024 * 1024 * 1024, 8 * 1024 * 1024 * 1024, nil
}

func parseMeminfoKB(line string) uint64 {
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
