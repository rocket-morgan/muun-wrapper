.PHONY: apollo test build docs up down logs

# Prints the Apollo base image tag, building it only when the pin or recipe changes.
apollo:
	./scripts/apollo-image.sh

test:
	image="$$(./scripts/apollo-image.sh)" \
		&& docker build --progress=plain --build-arg APOLLO_IMAGE="$$image" -f Dockerfile.test -t muun-wrapper-test .

build:
	image="$$(./scripts/apollo-image.sh)" && APOLLO_IMAGE="$$image" docker compose build

docs:
	python3 -m json.tool docs/openapi.json >/dev/null
	bash -n scripts/up.sh scripts/down.sh scripts/apollo-image.sh
	docker run --rm -v "$(CURDIR):/work" -w /work redocly/cli:1.25.6 lint docs/openapi.json

up:
	./scripts/up.sh

down:
	./scripts/down.sh

logs:
	docker compose logs -f
