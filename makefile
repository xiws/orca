
BINARY=orca

.PHONY: run
run:
	go run ./cmd/cli  "生成一个单元测试文件,为./internal/llm/openai.go文件"

.PHONY:build
build:
	go build -o orca ./cmd/cli

.PHONY: publish
publish: build
	@echo "📦 安装 $(BINARY) 到 GOPATH/bin..."
	@cp ./$(BINARY) $(GOPATH)/bin/$(BINARY)
	@echo "✅ 安装完成: $(GOPATH)/bin/$(BINARY)"

.PHONY: run
run:
	go run ./cmd/cli -p otter -m  gpt "这个项目做了什么"