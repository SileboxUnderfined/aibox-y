.PHONY: test package release
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
test:
	go test ./...
package:
	rm -rf dist/aibox-pkg && mkdir -p dist/aibox-pkg/bin
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/aibox-pkg/bin/aibox ./cmd/aibox
	cp packaging/yam/* dist/aibox-pkg/
	chmod +x dist/aibox-pkg/*.sh
	sed -i.bak 's/^version = .*/version = "$(patsubst v%,%,$(VERSION))"/' dist/aibox-pkg/plugin.toml && rm dist/aibox-pkg/plugin.toml.bak
# Release assets: the plugin archive, the bare binary and their checksums.
release: package
	cd dist && tar czf aibox-$(VERSION)-linux-arm64.tar.gz aibox-pkg
	cp dist/aibox-pkg/bin/aibox dist/aibox-$(VERSION)-linux-arm64
	cd dist && sha256sum aibox-$(VERSION)-linux-arm64.tar.gz aibox-$(VERSION)-linux-arm64 > SHA256SUMS
