VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(DATE)
PREFIX  ?= /usr/local

.PHONY: build test vet install clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o rkflash ./cmd/rkflash

test:
	go test ./...

vet:
	gofmt -l . | (! grep .) && go vet ./...

install: build
	install -Dm755 rkflash $(DESTDIR)$(PREFIX)/bin/rkflash

clean:
	rm -f rkflash
