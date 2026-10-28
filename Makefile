.PHONY: test package release size
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# The speaker has a few tens of MB free on /data, so the binary is kept small:
# no symbols or DWARF (-s -w), no local paths (-trimpath), no VCS stamp, and
# net/http without HTTP/2 (providers and uniproxy all speak HTTP/1.1).
GOFLAGS_SIZE = -trimpath -buildvcs=false -tags nethttpomithttp2 -ldflags="-s -w"
# make size fails when the binary grows past this budget.
SIZE_LIMIT ?= 13000000
test:
	go test ./...
package:
	rm -rf dist/aibox-pkg && mkdir -p dist/aibox-pkg/bin
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build $(GOFLAGS_SIZE) -o dist/aibox-pkg/bin/aibox ./cmd/aibox
	cp packaging/yam/* dist/aibox-pkg/
	chmod +x dist/aibox-pkg/*.sh
	sed -i.bak 's/^version = .*/version = "$(patsubst v%,%,$(VERSION))"/' dist/aibox-pkg/plugin.toml && rm dist/aibox-pkg/plugin.toml.bak
size: package
	@size=$$(wc -c < dist/aibox-pkg/bin/aibox); \
	echo "aibox linux/arm64: $$size bytes (limit $(SIZE_LIMIT))"; \
	[ $$size -le $(SIZE_LIMIT) ] || { echo "binary is over the size budget"; exit 1; }
# Release assets: the archive for install.sh, a .yap for the web panel of
# Luna or legacy YAM (plugin.toml at its root), the bare binary and checksums.
release: package
	cd dist && tar czf aibox-$(VERSION)-linux-arm64.tar.gz aibox-pkg
	cd dist/aibox-pkg && tar czf ../aibox-$(VERSION)-linux-arm64.yap *
	cp dist/aibox-pkg/bin/aibox dist/aibox-$(VERSION)-linux-arm64
	cd dist && sha256sum aibox-$(VERSION)-linux-arm64.tar.gz aibox-$(VERSION)-linux-arm64.yap aibox-$(VERSION)-linux-arm64 > SHA256SUMS
