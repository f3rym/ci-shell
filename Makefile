# ci-shell — сборка под все платформы.
#
#   make build            — бинарь под текущую ОС/арх → ./ci
#   make macos            — darwin arm64 + amd64 (docker CLI)
#   make macos-container  — darwin arm64 + amd64 с Apple `container` вместо docker
#   make linux            — linux amd64 + arm64
#   make windows          — windows amd64
#   make all              — все платформы разом
#   make clean            — убрать dist/ и ./ci

BINARY      := ci
PKG         := ./cmd/ci
DIST        := dist
MODULE      := github.com/f3rym/ci-shell

# -s -w — без таблицы символов и DWARF: бинарь заметно меньше.
LDFLAGS       := -s -w
# Вариант с Apple `container` (https://github.com/apple/container) вместо docker:
# подменяем имя CLI, зашитое в internal/runner.
CONTAINER_LDFLAGS := $(LDFLAGS) -X $(MODULE)/internal/runner.containerCLI=container

.PHONY: build all macos macos-container linux windows clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

all: macos macos-container linux windows

macos:
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-arm64 $(PKG)
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-amd64 $(PKG)

# macOS-бинарь, который зовёт Apple `container` вместо docker.
# CLI обязан быть docker-совместимым по подкомандам: version / image inspect /
# pull / create / start / exec / rm.
macos-container:
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(CONTAINER_LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-arm64-container $(PKG)
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(CONTAINER_LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-amd64-container $(PKG)

linux:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-amd64 $(PKG)
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-arm64 $(PKG)

windows:
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-windows-amd64.exe $(PKG)

clean:
	rm -rf $(DIST) $(BINARY)
