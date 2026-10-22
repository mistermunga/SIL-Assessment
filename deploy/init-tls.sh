#!/usr/bin/env bash
# First-time TLS setup: gets a Let's Encrypt certificate for $DOMAIN (from .env).
#
#   deploy/init-tls.sh --staging       trial run against Let's Encrypt's staging CA (browsers distrust it)
#   deploy/init-tls.sh                 the certificate browsers trust
#   deploy/init-tls.sh --force         replace an existing certificate (e.g. staging -> production)
#   deploy/init-tls.sh --self-signed   a year-long self-signed certificate, for local testing
#
# Renewal is automatic afterwards (the certbot-renew service).

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

log()  { printf '%s  %s\n' "$(date -u +%H:%M:%S)" "$*"; }
fail() { log "FAILED: $*" >&2; exit 1; }
dc()   { docker compose "$@"; }

staging=0 selfsigned=0 force=0
for arg in "$@"; do
  case $arg in
    --staging)     staging=1 ;;
    --self-signed) selfsigned=1 ;;
    --force)       force=1 ;;
    *) fail "unknown option $arg" ;;
  esac
done

[[ -f .env ]] || fail ".env is missing"
dc config -q || fail "docker-compose.yaml or .env is invalid (are DOMAIN and LETSENCRYPT_EMAIL set?)"

# Runs a shell snippet in the certbot image, which has the certificate and challenge volumes.
in_certbot() { dc run --rm -T --no-deps --entrypoint sh certbot -c "$1"; }

# A throwaway self-signed certificate, so nginx can start before the real one exists.
write_dummy() { # days
  in_certbot "
    set -e
    apk add --no-cache openssl >/dev/null
    mkdir -p /etc/letsencrypt/live/clinic
    openssl req -x509 -nodes -newkey rsa:2048 -days $1 -subj '/CN=localhost' \
      -keyout /etc/letsencrypt/live/clinic/privkey.pem \
      -out /etc/letsencrypt/live/clinic/fullchain.pem 2>/dev/null"
}

wipe() {
  in_certbot 'rm -rf /etc/letsencrypt/live/clinic /etc/letsencrypt/archive/clinic /etc/letsencrypt/renewal/clinic.conf'
}

nginx_up() {
  local id
  id=$(dc ps -q nginx)
  [[ -n $id ]] && [[ $(docker inspect --format '{{.State.Health.Status}}' "$id" 2>/dev/null) == healthy ]]
}

managed=$(in_certbot '[ -f /etc/letsencrypt/renewal/clinic.conf ] && echo yes || echo no' | tr -d '\r')
if [[ $managed == yes && $force -eq 0 ]]; then
  fail "a certificate already exists; use --force to replace it (Let's Encrypt limits how often certificates are issued)"
fi

log "installing a temporary certificate so nginx can start"
wipe
write_dummy $(( selfsigned ? 365 : 1 ))

log "starting nginx"
dc up -d nginx
for _ in $(seq 1 30); do nginx_up && break; sleep 2; done
nginx_up || fail "nginx did not become healthy; see: docker compose logs nginx"

if (( selfsigned )); then
  dc exec -T nginx nginx -s reload
  log "self-signed certificate installed (valid for a year). Browsers will warn, and curl needs -k."
  exit 0
fi

label="" extra=""
if (( staging )); then label=" (staging)"; extra="--staging"; fi

log "requesting the certificate from Let's Encrypt$label"
wipe
if ! in_certbot "certbot certonly --webroot -w /var/www/certbot --cert-name clinic \
      -d \"\$DOMAIN\" --email \"\$LETSENCRYPT_EMAIL\" --agree-tos --no-eff-email --non-interactive $extra"; then
  write_dummy 1 || true   # keep nginx able to restart
  fail "Let's Encrypt refused the request. Check that the DNS A record points at this server, that ports 80 and 443 are open, and try --staging first."
fi

dc exec -T nginx nginx -s reload || fail "the certificate was issued but nginx did not reload"
log "certificate installed$label; renewal is automatic"