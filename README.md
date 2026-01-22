# LLM-Router

An intelligent router and load balancer for `llama.cpp` instances. This project allows managing multiple LLM nodes (agents), dynamically loading/unloading models, and distributing requests based on availability and performance.

## Features
- **Central Server:** Manages cluster state, policies, provides a UI, and an OpenAI-compatible API.
- **Node Agent:** Runs alongside each `llama.cpp` instance, monitors resources (RAM, slots), and reports status to the server.
- **Dynamic Placement:** Automatically loads models on available nodes.
- **Web UI:** Dashboard for monitoring nodes, models, and activities.

## Known Issues (Bugs) 🐛
- **Node Routing:** Currently, the router only works with the **last started node**. Requests are not correctly distributed across all active nodes.

## Preparation

### Create Network
Before starting the containers, the Docker network must exist:
```bash
docker network create llmnet
```

### Prerequisites (Protobuf)
The Protobuf compiler is required for development and generating gRPC interfaces.

#### Debian/Ubuntu
`apt install protobuf-compiler`

#### macOS
`brew install protobuf`

#### Windows
`winget install protobuf`

## Installation & Development

### Proto Generation
To generate Go files from proto definitions:
```bash
protoc -I proto --go_out=gen --go_opt=paths=source_relative --go-grpc_out=gen --go-grpc_opt=paths=source_relative proto/controlplane/v1/controlplane.proto
```

### Running with Docker
The project includes example compose files for the server and several nodes:

1. **Start Server:**
   ```bash
   docker compose -f compose.server.yml up -d
   ```
2. **Start Node:**
   ```bash
   docker compose -f compose.node.yml up -d
   ```

## Testing

The server is accessible by default on port `8080` (API/UI) and port `9090` (gRPC).

### API Test (OpenAI compatible)
```bash
curl -s http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"<MODEL_ID>",
    "messages":[{"role":"user","content":"hi"}],
    "stream":false
  }'
```

### Web Interface
The dashboard is accessible at `http://localhost:8080/ui/`.