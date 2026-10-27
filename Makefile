.PHONY: test package
test:
	go test ./...
package:
	rm -rf dist/aibox-pkg && mkdir -p dist/aibox-pkg/bin
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/aibox-pkg/bin/aibox ./cmd/aibox
	cp packaging/yam/* dist/aibox-pkg/
	chmod +x dist/aibox-pkg/*.sh
