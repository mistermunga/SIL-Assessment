#!/usr/bin/env bash
# Deploys the commit checked out in this repository.
#
#   1. check the certificate, start postgres and nginx, check they are healthy (a failure changes nothing)
#   2. build the images
#   3. migrate the test database and run the tests        (production is untouched)
#   4. back up and migrate the production database
#   5. replace the app container, wait until it answers through nginx
#   6. if it does not, put the previous image back
#
# The caller checks out the commit first:   git checkout --detach <sha> && deploy/deploy.sh

set -euo pipefail

STATE=.deploy
KEEP_IMAGES=5
KEEP_BACKUPS=10

log()  { printf '%s  %s\n' "$(date -u +%H:%M:%S)" "$*"; }
fail() { log "FAILED: $*" >&2; exit 1; }
dc()   { docker compose "$@"; }

# healthy | starting | unhealthy | missing | ...
health() {
  local id
  id=$(dc ps -q "$1" 2>/dev/null || true)
  [[ -n $id ]] || { echo missing; return 0; }
  docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$id" 2>/dev/null || echo gone
}

wait_healthy() { # service seconds
  local waited=0
  while (( waited < $2 )); do
    [[ $(health "$1") == healthy ]] && return 0
    sleep 2
    waited=$((waited + 2))
  done
  return 1
}

# Asks nginx (on its internal listener) for /healthz, so nginx -> app -> database is checked.
wait_ready() { # seconds
  local waited=0
  while (( waited < $1 )); do
    dc exec -T nginx wget -q -O /dev/null http://127.0.0.1:8081/healthz 2>/dev/null && return 0
    sleep 2
    waited=$((waited + 2))
  done
  return 1
}

# Runs the given image as the app and waits until it is healthy.
start_app() { # tag
  APP_TAG=$1 dc up -d --no-deps app || return 1
  wait_healthy app 60 || return 1
  wait_ready 30
}

# Removes old sha-tagged images, keeping the newest $2 and any protected tags.
prune() { # repo keep [protected-tag...]
  local repo=$1 keep=$2
  shift 2
  docker images --format '{{.Tag}}' "$repo" \
    | grep -E '^[0-9a-f]{12}$' \
    | grep -vxF -f <(printf '%s\n' "$@") \
    | tail -n +$((keep + 1)) \
    | while read -r t; do docker rmi "$repo:$t" >/dev/null 2>&1 || true; done
}

main() {
  cd "$(dirname "${BASH_SOURCE[0]}")/.."
  mkdir -p "$STATE" backups

  exec 9>"$STATE/lock"
  flock -n 9 || fail "another deployment is running"

  [[ -f .env ]] || fail ".env is missing"
  git diff --quiet && git diff --cached --quiet || fail "the working tree has uncommitted changes"

  local tag prev out dump
  tag=$(git rev-parse --short=12 HEAD)
  prev=$(cat "$STATE/current" 2>/dev/null || true)
  export APP_TAG=$tag
  dc config -q || fail "docker-compose.yaml or .env is invalid"
  log "deploying $tag (running now: ${prev:-nothing})"

  dc run --rm -T --no-deps --entrypoint test certbot -f /etc/letsencrypt/live/clinic/fullchain.pem \
    || fail "there is no TLS certificate yet; run deploy/init-tls.sh first"

  log "checking postgres and nginx"
  dc up -d db nginx certbot-renew || fail "could not start postgres, nginx and certbot"
  wait_healthy db 90    || fail "postgres is not healthy; nothing was changed"
  wait_healthy nginx 60 || fail "nginx is not healthy; nothing was changed"
  out=$(dc exec -T nginx nginx -t 2>&1) || { printf '%s\n' "$out" >&2; fail "nginx configuration is invalid; nothing was changed"; }
  dc exec -T nginx nginx -s reload >/dev/null 2>&1 || fail "nginx reload failed"

  log "building images"
  docker build --target runtime -t "clinic-app:$tag"  . || fail "building the app image failed"
  docker build --target test    -t "clinic-test:$tag" . || fail "building the test image failed"

  log "migrating the test database and running the tests"
  dc run --rm -T migrate-test || fail "test database migration failed; production was not touched"
  dc run --rm -T test         || fail "tests failed; production was not touched"

  log "backing up and migrating the production database"
  dump="backups/clinic-$(date -u +%Y%m%dT%H%M%SZ)-$tag.dump"
  if ! dc exec -T db pg_dump -U postgres -Fc clinic > "$dump" || [[ ! -s $dump ]]; then
    rm -f "$dump"
    fail "backup failed; production was not touched"
  fi
  ls -1t backups/*.dump 2>/dev/null | tail -n +$((KEEP_BACKUPS + 1)) | xargs -r rm -- || true
  dc run --rm -T migrate || fail "production migration failed; the running app was not touched"

  log "replacing the app container (${prev:-nothing} -> $tag)"
  if start_app "$tag"; then
    docker tag "clinic-app:$tag" clinic-app:current
    printf '%s\n' "$tag" > "$STATE/current"
    prune clinic-app "$KEEP_IMAGES" "$tag" "$prev" || true
    prune clinic-test 2 || true
    log "deployed $tag"
    return 0
  fi

  log "the new release is not healthy; its last log lines:" >&2
  dc logs --no-color --tail 40 app >&2 || true

  if [[ -z $prev ]]; then
    dc stop app >/dev/null 2>&1 || true
    fail "there is no previous release to roll back to; the app is stopped"
  fi

  log "rolling back to $prev"
  if start_app "$prev"; then
    docker tag "clinic-app:$prev" clinic-app:current
    fail "release $tag was unhealthy and rolled back to $prev (migrations stay applied)"
  fi
  fail "release $tag was unhealthy AND the rollback to $prev failed; intervene manually"
}

# One line, so bash never reads further into this file if git replaces it mid-run.
main "$@"; exit $?