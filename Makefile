.PHONY: build web-build test check clean

build: web-build
	mkdir -p bin
	CGO_ENABLED=0 go build -tags stdjson,gjson -trimpath -o bin/proxy-server ./cmd/proxy-server
	CGO_ENABLED=0 go build -tags stdjson,gjson -trimpath -o bin/proxy-manage-server ./cmd/proxy-manage-server

web-build:
	npm --prefix web run build
	mkdir -p internal/webui/dist
	find internal/webui/dist -mindepth 1 -maxdepth 1 -exec rm -rf {} +
	cp -R web/dist/. internal/webui/dist/

test:
	go test -tags stdjson,gjson ./...

check:
	go vet -tags stdjson,gjson ./...
	bash -n scripts/install-proxy-server.sh scripts/install-proxy-manage-server.sh

clean:
	rm -rf bin web/dist
