# ci-shell — сборка под все платформы.
#
#   make build            — бинарь под текущую ОС/арх → ./ci
#   make macos            — darwin arm64 + amd64 (docker CLI)
#   make macos-container  — darwin arm64 + amd64 с Apple `container` вместо docker
#   make linux            — linux amd64 + arm64
#   make windows          — windows amd64
#   make all              — все платформы разом
#   make deps             — пересобрать go.sum (go mod tidy, ходит в сеть)
#   make clean            — убрать dist/ и ./ci
#
# go.sum создаётся автоматически перед первой сборкой — отдельно звать deps не надо.

BINARY      := ci
PKG         := ./cmd/ci
DIST        := dist
MODULE      := github.com/f3rym/ci-shell

# -s -w — без таблицы символов и DWARF: бинарь заметно меньше.
LDFLAGS       := -s -w
# Вариант с Apple `container` (https://github.com/apple/container) вместо docker:
# подменяем имя CLI, зашитое в internal/runner.
CONTAINER_LDFLAGS := $(LDFLAGS) -X $(MODULE)/internal/runner.containerCLI=container

.PHONY: build all deps macos macos-container linux windows clean

# go.sum появляется из go.mod одним вызовом go mod tidy — он ходит в сеть за
# модулями и дописывает косвенные зависимости. Цель-файл, а не .PHONY: пока
# go.sum на месте и не старше go.mod, сеть не дёргается.
go.sum: go.mod
	go mod tidy

# deps — ручной вызов того же самого, когда go.sum надо пересобрать намеренно.
deps:
	go mod tidy

build: go.sum
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

all: macos macos-container linux windows

macos: go.sum
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-arm64 $(PKG)
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-amd64 $(PKG)

# macOS-бинарь, который зовёт Apple `container` вместо docker.
# CLI обязан быть docker-совместимым по подкомандам: version / image inspect /
# pull / create / start / exec / rm.
macos-container: go.sum
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(CONTAINER_LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-arm64-container $(PKG)
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(CONTAINER_LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-amd64-container $(PKG)

linux: go.sum
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-amd64 $(PKG)
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-arm64 $(PKG)

windows: go.sum
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-windows-amd64.exe $(PKG)

clean:
	rm -rf $(DIST) $(BINARY)
