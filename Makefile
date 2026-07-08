.PHONY: test build ebpf integration

test:
	go test ./...

build:
	go build -o bin/ltm ./cmd/ltm

ebpf:
	cd internal/ebpf && clang -O2 -g -target bpf -D__TARGET_ARCH_x86 \
		-mllvm -bpf-stack-size=1024 \
		-c collector.bpf.c -o collector_bpfel.o -I./headers

integration:
	bash tests/integration.sh
