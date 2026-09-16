#!/usr/bin/env bash
set -euo pipefail

project_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$project_dir"

if [[ ! -f compose.secrets ]]; then
  printf 'Falta compose.secrets. Copiá .env.example a compose.secrets y reemplazá los secretos de ejemplo.\n' >&2
  exit 1
fi

docker compose up --build -d "$@"

for _ in {1..20}; do
  if curl --fail --silent --show-error http://127.0.0.1:18080/healthz >/dev/null 2>&1; then
    printf 'muun-wrapper disponible en http://127.0.0.1:18080\n'
    exit 0
  fi
  sleep 1
done

printf 'muun-wrapper no respondió en 20 segundos. Revisá: docker compose logs --tail=100\n' >&2
exit 1
