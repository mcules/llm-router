package control

import (
	"io"
	"log"
	"sync"
	"time"

	controlplanev1 "github.com/mcules/llm-router/gen/controlplane/v1"
	"github.com/mcules/llm-router/internal/state"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type ModelStateNotifier interface {
	NotifyModelState(nodeID, modelID string, st state.ModelState)
}

type NodeControlService struct {
	controlplanev1.UnimplementedNodeControlServer
	Cluster  *state.ClusterState
	Notifier ModelStateNotifier

	mu      sync.RWMutex
	streams map[string]*nodeStream
}

type nodeStream struct {
	sendMu sync.Mutex
	stream controlplanev1.NodeControl_StreamServer
}

func NewNodeControlService(cluster *state.ClusterState, notifier ModelStateNotifier) *NodeControlService {
	return &NodeControlService{
		Cluster:  cluster,
		Notifier: notifier,
		streams:  map[string]*nodeStream{},
	}
}

func (s *NodeControlService) SendUnload(nodeID, requestID, modelID string) error {
	s.mu.RLock()
	ns := s.streams[nodeID]
	s.mu.RUnlock()
	if ns == nil {
		return status.Errorf(codes.Unavailable, "node stream not available: %s", nodeID)
	}

	msg := &controlplanev1.ServerMessage{
		Msg: &controlplanev1.ServerMessage_UnloadModel{
			UnloadModel: &controlplanev1.UnloadModel{
				RequestId: requestID,
				ModelId:   modelID,
			},
		},
	}

	ns.sendMu.Lock()
	defer ns.sendMu.Unlock()

	if err := ns.stream.Send(msg); err != nil {
		return status.Errorf(codes.Unavailable, "send unload: %v", err)
	}
	return nil
}

func (s *NodeControlService) BroadcastPing() {
	s.mu.RLock()
	// Copy stream pointers to minimize lock hold time
	streams := make([]*nodeStream, 0, len(s.streams))
	for _, ns := range s.streams {
		streams = append(streams, ns)
	}
	s.mu.RUnlock()

	msg := &controlplanev1.ServerMessage{
		Msg: &controlplanev1.ServerMessage_Ping{
			Ping: &controlplanev1.Ping{TsUnixMs: time.Now().UnixMilli()},
		},
	}

	for _, ns := range streams {
		go func(n *nodeStream) {
			n.sendMu.Lock()
			defer n.sendMu.Unlock()
			_ = n.stream.Send(msg)
		}(ns)
	}
}

func (s *NodeControlService) Stream(stream controlplanev1.NodeControl_StreamServer) error {
	_ = stream.Send(&controlplanev1.ServerMessage{
		Msg: &controlplanev1.ServerMessage_Hello{
			Hello: &controlplanev1.ServerHello{ServerVersion: "dev"},
		},
	})

	var nodeID string

	for {
		in, err := stream.Recv()
		if err == io.EOF {
			s.detach(nodeID, stream)
			return nil
		}
		if err != nil {
			s.detach(nodeID, stream)
			return status.Errorf(codes.Unavailable, "stream recv: %v", err)
		}

		switch msg := in.Msg.(type) {
		case *controlplanev1.NodeMessage_Hello:
			nodeID = msg.Hello.NodeId

			s.Cluster.UpsertNodeHello(
				nodeID,
				msg.Hello.Version,
				msg.Hello.LlamaBaseUrl,
				msg.Hello.DataPlaneUrl,
			)

			s.attach(nodeID, stream)
			remoteAddr := "unknown"
			if p, ok := peer.FromContext(stream.Context()); ok {
				remoteAddr = p.Addr.String()
			}
			log.Printf("node hello: id=%s version=%s llama=%s data=%s remote=%s",
				msg.Hello.NodeId, msg.Hello.Version, msg.Hello.LlamaBaseUrl, msg.Hello.DataPlaneUrl, remoteAddr)

		case *controlplanev1.NodeMessage_Status:
			if nodeID == "" {
				remoteAddr := "unknown"
				if p, ok := peer.FromContext(stream.Context()); ok {
					remoteAddr = p.Addr.String()
				}
				log.Printf("WARNING: Received status from stream with no nodeID (remote: %s). Closing stream.", remoteAddr)
				return status.Errorf(codes.FailedPrecondition, "nodeID not established via hello")
			}

			models := map[string]state.ModelResidency{}
			now := time.Now()

			for _, m := range msg.Status.Models {
				st := mapModelState(m.State)

				models[m.ModelId] = state.ModelResidency{
					ModelID:     m.ModelId,
					State:       st,
					LoadedSince: unixMsToTime(m.LoadedSinceUnixMs),
					LastSeen:    now,
				}

				// Notify router gates (READY signals unblock waiting requests).
				if s.Notifier != nil {
					s.Notifier.NotifyModelState(nodeID, m.ModelId, st)
				}
			}

			remoteAddr := "unknown"
			if p, ok := peer.FromContext(stream.Context()); ok {
				remoteAddr = p.Addr.String()
			}
			log.Printf("node status: id=%s remote=%s ram_avail=%d inflight=%d models=%d", nodeID, remoteAddr, msg.Status.RamAvailableBytes, msg.Status.InflightRequests, len(msg.Status.Models))
			s.Cluster.UpdateNodeStatus(nodeID, msg.Status.RamTotalBytes, msg.Status.RamAvailableBytes, msg.Status.InflightRequests, models)

			// Verify if this stream is still the authoritative one for this nodeID.
			s.mu.RLock()
			currentStream, ok := s.streams[nodeID]
			s.mu.RUnlock()
			if ok && currentStream.stream != stream {
				remoteAddr := "unknown"
				if p, ok := peer.FromContext(stream.Context()); ok {
					remoteAddr = p.Addr.String()
				}
				log.Printf("WARNING: Received status from non-authoritative stream for node %s (remote: %s). Possible NODE_ID collision!", nodeID, remoteAddr)
			}

		case *controlplanev1.NodeMessage_Ack:
			log.Printf("node ack: req=%s ok=%v err=%s", msg.Ack.RequestId, msg.Ack.Ok, msg.Ack.Error)

		default:
			// Ignore unknown messages for forward compatibility.
		}
	}
}

func (s *NodeControlService) attach(nodeID string, stream controlplanev1.NodeControl_StreamServer) {
	if nodeID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	remoteAddr := "unknown"
	if p, ok := peer.FromContext(stream.Context()); ok {
		remoteAddr = p.Addr.String()
	}

	if old, ok := s.streams[nodeID]; ok {
		oldAddr := "unknown"
		if p, ok := peer.FromContext(old.stream.Context()); ok {
			oldAddr = p.Addr.String()
		}
		log.Printf("WARNING: node %s re-attached from %s (previous was %s). If these are different nodes, ensure unique NODE_IDs!", nodeID, remoteAddr, oldAddr)
	}

	s.streams[nodeID] = &nodeStream{stream: stream}
}

func (s *NodeControlService) detach(nodeID string, stream controlplanev1.NodeControl_StreamServer) {
	if nodeID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur := s.streams[nodeID]; cur != nil && cur.stream == stream {
		delete(s.streams, nodeID)
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
