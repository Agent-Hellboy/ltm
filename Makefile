.PHONY: test build ebpf integration

test:
	go test ./...

build:
	go build -o bin/ltm ./cmd/ltm

ebpf:
	cd ebpf && clang -O2 -g -target bpf -D__TARGET_ARCH_x86 \
		-c collector.bpf.c -o collector_bpfel.o -I./headers

integration:
	bash tests/integration.sh
