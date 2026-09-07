
.PHONY: run
run:
	go run ./cmd/cli -f ./docs/test.md "输出文档内容"

.PHONY:build
build:
	go build -o orca ./cmd/cli

