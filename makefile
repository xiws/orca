
BINARY=orca

.PHONY: run
run:
	go run ./cmd/cli --help

.PHONY:build
build:
	go build -o orca ./cmd/cli

.PHONY: test test-race vet bench
test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

bench:
	go test -run '^$$' -bench . -benchmem ./internal/app

.PHONY: publish
publish: build
	@echo "📦 安装 $(BINARY) 到 GOPATH/bin..."
	@cp ./$(BINARY) $(GOPATH)/bin/$(BINARY)
	@echo "✅ 安装完成: $(GOPATH)/bin/$(BINARY)"
