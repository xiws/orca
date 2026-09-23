
BINARY=orca

.PHONY: run
run:
	go run ./cmd/cli --help

.PHONY:build
build:
	go build -o orca ./cmd/cli

.PHONY: build-tui
build-tui:
	go build -o orca-tui ./cmd/tui

.PHONY: run-tui
run-tui:
	go run ./cmd/tui

.PHONY: test test-race vet bench test-live
test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

bench:
	go test -run '^$$' -bench . -benchmem ./internal/store ./internal/app

test-live:
	go test -tags live -run '^TestRequester' ./internal/llm

.PHONY: publish
publish: build
	@echo "📦 安装 $(BINARY) 到 GOPATH/bin..."
	@cp ./$(BINARY) $(GOPATH)/bin/$(BINARY)
	@echo "✅ 安装完成: $(GOPATH)/bin/$(BINARY)"