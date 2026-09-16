.PHONY: test build up down logs

test:
	docker build --progress=plain -f Dockerfile.test -t muun-wrapper-test .

build:
	docker compose build

up:
	docker compose up -d

down:
	docker compose down

logs:
	docker compose logs -f
