run:
	go run ./cmd/api
build:
	go build ./...
tests:
	GOCACHE=$${GOCACHE:-/tmp/go-build-cache} GOTMPDIR=$${GOTMPDIR:-/tmp/go-tmp} go test ./...
	GOCACHE=$${GOCACHE:-/tmp/go-build-cache} GOTMPDIR=$${GOTMPDIR:-/tmp/go-tmp} go test ./tests -coverpkg=./internal/application -covermode=atomic -coverprofile=coverage.out
	go tool cover -func=coverage.out | grep total:
lint:
	golangci-lint run
fmt:
	gofmt -w cmd
