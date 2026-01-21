## Vorbereitung
`docker network create llmnet`

## Proto install

### Debian/Ubuntu
`apt install protobuf-compiler`

### macOS
`brew install protobuf`

### windows
`winget install protobuf`

## Proto gen
`protoc -I proto --go_out=gen --go_opt=paths=source_relative --go-grpc_out=gen --go-grpc_opt=paths=source_relative proto/controlplane/v1/controlplane.proto`

## Test
```
curl -s http://localhost:8090/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"<MODEL_ID>","messages":[{"role":"user","content":"hi"}],"stream":false}'
```