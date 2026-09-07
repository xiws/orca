

.PHONY: run
run:
	go run ./cmd/cli -f ./docs/test.md "原样输出文档内容，不要包括任何内容"

.PHONY:build
build:
	go build -o orca ./cmd/cli
