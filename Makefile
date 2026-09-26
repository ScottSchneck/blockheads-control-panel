VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet check fakeconsole docker clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o blockheads ./cmd/blockheads

fakeconsole:
	go build -o fakeconsole ./tools/fakeconsole

test:
	go test -race ./...

vet:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed on:"; gofmt -l .; exit 1)

check: vet test

docker:
	docker build --build-arg VERSION=$(VERSION) -t blockheads-control-panel:$(VERSION) .

clean:
	rm -f blockheads fakeconsole
