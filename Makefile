.PHONY: test build generate ebpf integration verify-generated

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

# verify-generated checks each abi.yaml-derived file's embedded content hash
# against its current bytes, catching a hand edit that bypassed the generator
# without needing abi.yaml or a Go/BPF toolchain. Also runs as a unit test
# (see internal/abi/gen/main_test.go), so `make test` covers it too.
verify-generated:
	cd internal/abi && go run ./gen -verify

integration:
	bash tests/integration.sh
