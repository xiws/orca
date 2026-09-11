

.PHONY: run
run:
	go run ./cmd/cli  "生成一个单元测试文件,为./internal/llm/openai.go文件"

.PHONY:build
build:
	go build -o orca ./cmd/cli

.PHONY: publish
publish: build
	sudo cp ./orca /usr/local/bin/