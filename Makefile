.PHONY: build test docker-image docker-test clean

build:
	./build.sh

test:
	go test ./...

docker-image:
	docker build -t headscale-console:latest .

docker-test:
	./tests/docker-smoke.sh dist/headscale-console

clean:
	rm -rf dist .cache
	find internal/assets/runtime -mindepth 1 ! -name .gitkeep -delete
