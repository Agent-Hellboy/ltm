.PHONY: test build generate ebpf integration

test:
	go test ./...

build:
	go build -o bin/ltm ./cmd/ltm

# generate compiles abi.yaml into Go + the kernel_event.gen.h header. Pure Go,
# runs anywhere.
generate:
	go generate ./internal/abi/

# ebpf regenerates the header first, then compiles the BPF object and its Go
# bindings with bpf2go. Requires clang with a bpf target (Linux/CI, not macOS).
ebpf: generate
	go generate ./internal/ebpf/

integration:
	bash tests/integration.sh
