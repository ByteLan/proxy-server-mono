.PHONY: build web-build release test check clean

build: web-build
	mkdir -p bin
	CGO_ENABLED=0 go build -tags stdjson,gjson -trimpath -o bin/proxy-server ./cmd/proxy-server
	CGO_ENABLED=0 go build -tags stdjson,gjson -trimpath -o bin/proxy-manage-server ./cmd/proxy-manage-server

web-build:
	npm --prefix web run build
	mkdir -p internal/webui/dist
	find internal/webui/dist -mindepth 1 -maxdepth 1 -exec rm -rf {} +
	cp -R web/dist/. internal/webui/dist/

release:
	bash scripts/build-release.sh

test:
	go test -tags stdjson,gjson ./...
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts -p 'test_*.py'

check:
	go vet -tags stdjson,gjson ./...
	bash -n scripts/*.sh

clean:
	rm -rf bin web/dist
