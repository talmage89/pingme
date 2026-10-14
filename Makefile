.PHONY: build test release
build:
	go build -trimpath -o push-notifs .
test:
	go test -race ./...
	go vet ./...
release:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/push-notifs-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/push-notifs-linux-arm64 .
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -o dist/push-notifs-darwin-amd64 .
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -o dist/push-notifs-darwin-arm64 .
