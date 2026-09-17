.PHONY: test build docs up down logs

test:
	docker build --progress=plain -f Dockerfile.test -t muun-wrapper-test .

build:
	docker compose build

docs:
	python3 -m json.tool docs/openapi.json >/dev/null
	bash -n scripts/up.sh scripts/down.sh
	docker run --rm -v "$(CURDIR):/work" -w /work redocly/cli:1.25.6 lint docs/openapi.json

up:
	./scripts/up.sh

down:
	./scripts/down.sh

logs:
	docker compose logs -f
