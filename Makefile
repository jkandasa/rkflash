VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 2.0.0)
LDFLAGS := -s -w -X main.version=$(VERSION)
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
