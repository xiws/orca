

.PHONY: run
run:
	go run ./cmd/cli  "总结cmd/cli/args.go文件的逻辑到./docs/test.md"

.PHONY:build
build:
	go build -o orca ./cmd/cli
