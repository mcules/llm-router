package control

import (
	"io"
	"log"
	"time"

	controlplanev1 "your.module/gen/controlplane/v1"
	"your.module/internal/state"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Comments in this file are intentionally in English.

type NodeControlService struct {
	controlplanev1.UnimplementedNodeControlServer
	Cluster *state.ClusterState
}

func NewNodeControlService(cluster *state.ClusterState) *NodeControlService {
	return &NodeControlService{Cluster: cluster}
}

func (s *NodeControlService) Stream(stream controlplanev1.NodeControl_StreamServer) error {
	// Send hello once.
	_ = stream.Send(&controlplanev1.ServerMessage{
		Msg: &controlplanev1.ServerMessage_Hello{
			Hello: &controlplanev1.ServerHello{ServerVersion: "dev"},
		},
	})

	var nodeID string

	for {
		in, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return status.Errorf(codes.Unavailable, "stream recv: %v", err)
		}

		switch msg := in.Msg.(type) {
		case *controlplanev1.NodeMessage_Hello:
			nodeID = msg.Hello.NodeId
			s.Cluster.UpsertNodeHello(nodeID, msg.Hello.Version, msg.Hello.LlamaBaseUrl)
			log.Printf("node hello: id=%s version=%s llama=%s", msg.Hello.NodeId, msg.Hello.Version, msg.Hello.LlamaBaseUrl)

		case *controlplanev1.NodeMessage_Status:
			if nodeID == "" {
				// Accept status even if hello was not received yet (best effort).
				nodeID = "unknown"
			}
			models := map[string]state.ModelResidency{}
			now := time.Now()

			for _, m := range msg.Status.Models {
				models[m.ModelId] = state.ModelResidency{
					ModelID:     m.ModelId,
					State:       mapModelState(m.State),
					LoadedSince: unixMsToTime(m.LoadedSinceUnixMs),
					LastSeen:    now,
				}
			}
			s.Cluster.UpdateNodeStatus(nodeID, msg.Status.RamTotalBytes, msg.Status.RamAvailableBytes, msg.Status.InflightRequests, models)

		case *controlplanev1.NodeMessage_Ack:
			// Phase 1: just log acks.
			log.Printf("node ack: ok=%v err=%s", msg.Ack.Ok, msg.Ack.Error)

		default:
			// Ignore unknown messages for forward compatibility.
		}
	}
}

func mapModelState(st controlplanev1.ModelState) state.ModelState {
	switch st {
	case controlplanev1.ModelState_MODEL_STATE_LOADING:
		return state.ModelLoading
	case controlplanev1.ModelState_MODEL_STATE_READY:
		return state.ModelReady
	case controlplanev1.ModelState_MODEL_STATE_UNLOADED:
		return state.ModelUnloaded
	case controlplanev1.ModelState_MODEL_STATE_ERROR:
		return state.ModelError
	default:
		return state.ModelUnloaded
	}
}

func unixMsToTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.Unix(0, ms*int64(time.Millisecond))
}
