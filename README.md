# Clinic Booking API

REST API for a small clinic: patients view a doctor's free 30-minute slots, book, cancel and reschedule; doctors manage their own schedules and appointment outcomes; an administrator creates doctors.

**Live:** `https://clinic.ryansbeanfactory.xyz` (health check: `/healthz`)
**Stack:** Go (stdlib `net/http`), PostgreSQL 18, sqlc, goose, JWT, nginx, Docker Compose, GitHub Actions.

The full requirements, assumptions, schema and security reasoning are in [`design/`](design/). This README covers what ships, how to run it, and how to use it.

## Design in brief

- **Double bookings are prevented by the database.** A partial unique index on `(doctor_id, appointment_start) WHERE status = 'booked'` makes the second concurrent insert fail (reported as `slot_taken`). Cancelled rows don't block rebooking.
- **Availability is computed**, not stored: 30-minute steps across the day's shift, minus the break, minus booked slots.
- **Roles come from table membership** (`patients`, `doctors`, `doctors.is_admin`), not a role column. The role is put in the token at login; admin routes re-check it against the database.
- **Stateless JWT (HS256, 15 min).** Trade-off: tokens can't be revoked early; the short expiry and the admin re-check bound the exposure.
- **Layered by feature.** Each module in `internal/` owns its routes, service and error mapping; handlers return errors and one place turns them into JSON responses.
- **"Completed" is derived** (a `booked` appointment in the past), and only `no_show` is stored. Doctors can also record an outcome explicitly.
- **Doctor leave is a TODO.** It is designed (see `design/`) but there is no endpoint to record it yet.

## Run locally

Requires Docker with Compose v2.

```bash
cp .env.example .env            # fill in the secrets; see "Configuration"
docker build --target runtime -t clinic-app:current .
deploy/init-tls.sh --self-signed   # throwaway certificate so nginx can start
docker compose up -d               # postgres -> migrations -> app, behind nginx
curl -k https://localhost/healthz
```

Set `HTTP_PORT` / `HTTPS_PORT` in `.env` if 80/443 are taken. The administrator account is created from `ADMIN_USERNAME` / `ADMIN_PASSWORD` (default username `administrator`).

### Tests

```bash
docker build --target test -t clinic-test:current .
docker compose run --rm migrate-test
docker compose run --rm test       # go test -p 1 -race ./...
```

Unit tests sit next to the service they cover. `internal/app` holds the integration suite, which exercises the whole API over HTTPS, including a concurrent-booking test (many simultaneous requests for one slot produce exactly one booking). Without Docker: set `TEST_DATABASE_URL`, run `goose -dir db/migrations postgres "$TEST_DATABASE_URL" up`, then `go test -p 1 -race ./...`.

### Configuration

| Variable | Notes |
|:--|:--|
| `POSTGRES_PASSWORD`, `CLINIC_DB_PASSWORD`, `CLINIC_TEST_DB_PASSWORD` | Required. Used once to create the roles and databases. |
| `JWT_SECRET` | Required, at least 32 random bytes. |
| `CLINIC_TIMEZONE` | Required, e.g. `Africa/Nairobi`. Dates are read and times shown in this zone. |
| `ADMIN_PASSWORD` | Required. `ADMIN_USERNAME`, `ADMIN_EMAIL` are optional. |
| `DOMAIN`, `LETSENCRYPT_EMAIL` | Required (TLS). |
| `ACCESS_TOKEN_TTL` | Optional, default `15m`. |

## CI/CD

`.github/workflows/ci.yml`, three jobs:

1. **test** (every push to `main` and every pull request): `gofmt`, `sqlc diff` (generated code is current), goose migrations, `go vet`, `go test -race` against a throwaway Postgres 18.
2. **docker** (same triggers): builds the image, smoke-tests it, validates `docker-compose.yaml`, runs `nginx -t`, lints the shell scripts with shellcheck.
3. **deploy** (push to **`main`** only, after both jobs pass; the `production` environment, one deploy at a time): SSHs to the server, checks out the commit and runs `deploy/deploy.sh`, then curls `https://<domain>/healthz`.

`deploy/deploy.sh` on the server:

1. Checks the certificate and that postgres and nginx are healthy (a failure here changes nothing).
2. Builds the app and test images, tagged with the commit SHA.
3. Migrates the test database and runs the full suite again; production is untouched if it fails.
4. Takes a `pg_dump` backup (last 10 kept), then migrates production.
5. Replaces the app container and waits for `/healthz` to answer through nginx.
6. If it doesn't, puts the previous image back (migrations stay applied, so they must stay backward compatible).

**Infrastructure:** one VPS (`Digital Ocean`) running Compose: nginx terminates TLS (Let's Encrypt, auto-renewed) and rate-limits `/login` and `/patients` to 10/min per IP; the app runs as a non-root user on a read-only filesystem; postgres sits on an internal network with no outbound route.

**One-time setup:** clone the repo to `/srv/clinic`, create `.env`, run `deploy/init-tls.sh`. GitHub secrets: `DEPLOY_HOST`, `DEPLOY_USER`, `DEPLOY_SSH_KEY`, `DEPLOY_KNOWN_HOSTS`; variable: `DEPLOY_DOMAIN`.

## Using the API

Every request and response body is JSON. Timestamps are RFC 3339 with an offset (`2026-10-26T10:00:00+03:00`); dates are `YYYY-MM-DD`. Authenticated routes need `Authorization: Bearer <token>`. Slots are 30 minutes, Monday to Friday.

```bash
BASE=https://<your-domain>

# 1. Register (returns a token) or log in
curl -s $BASE/patients -d '{"username":"jane","email":"jane@example.com","password":"S3cure-pass!","firstname":"Jane","lastname":"Doe"}'
TOKEN=$(curl -s $BASE/login -d '{"username":"jane","password":"S3cure-pass!"}' | jq -r .token)
AUTH="Authorization: Bearer $TOKEN"

# 2. Find a doctor and a free slot
curl -s -H "$AUTH" $BASE/doctors
curl -s -H "$AUTH" "$BASE/doctors/2/availability?date=2026-10-26"

# 3. Book, then list your upcoming appointments
curl -s -H "$AUTH" $BASE/appointments -d '{"doctor_id":2,"start":"2026-10-26T10:00:00+03:00"}'
curl -s -H "$AUTH" "$BASE/appointments?upcoming=true&limit=20&offset=0"

# 4. Reschedule or cancel
curl -s -X PATCH -H "$AUTH" $BASE/appointments/7/reschedule -d '{"start":"2026-10-27T11:30:00+03:00"}'
curl -s -X PATCH -H "$AUTH" $BASE/appointments/7/cancel -d '{"reason":"Work conflict"}'
```

### Endpoints

| Method and path | Who | Notes |
|:--|:--|:--|
| `POST /patients` | open | Register; returns user and token. |
| `POST /login` | open | Returns `token`, `token_type`, `expires_at`. |
| `GET /healthz` | open | Checks the database. |
| `GET /doctors` | any user | Doctor IDs and names. |
| `GET /doctors/{id}/availability?date=` | any user | Free slots for that day. |
| `POST /appointments` | patient | `{doctor_id, start}` |
| `GET /appointments` | patient | Own appointments. `upcoming`, `limit`, `offset`. |
| `PATCH /appointments/{id}/cancel` | patient | `{reason}`; 204. |
| `PATCH /appointments/{id}/reschedule` | patient | `{start}`; same validation as a new booking. |
| `GET /doctors/{id}/appointments?date=` | doctor | Own agenda; an admin may read any. |
| `PATCH /appointments/{id}/outcome` | doctor | `{outcome: "completed" or "no_show", notes}` |
| `GET /doctors/me/pending-outcomes` | doctor | Past appointments with no outcome yet. |
| `GET /doctors/{id}/schedule` | doctor | Own shifts; an admin may read any. |
| `PUT /doctors/{id}/schedule/{weekday}` | doctor | Weekday 1 (Mon) to 5 (Fri); `{start, end, break_start, break_end}` as `HH:MM`, break optional. |
| `DELETE /doctors/{id}/schedule/{weekday}` | doctor | Remove a day's shift. |
| `POST /doctors` | admin | Create a doctor; `is_admin` makes them an admin. |

`GET /appointments` stands in for `GET /patients/{id}/appointments`: the patient is taken from the token, so no one can request another patient's list.

### Errors

All errors share one shape: `{"error": {"code": "...", "message": "...", "problems": {...}}}`.

| Status | Meaning |
|:--|:--|
| 400 | Malformed JSON, unknown field, bad path value. |
| 401 / 403 | Missing or invalid token / wrong role or not your resource. |
| 404 | `appointment_not_found`, `doctor_not_found`, `shift_not_found`, `route_not_found`. |
| 409 | `slot_taken`, `patient_busy`, `already_cancelled`, `not_cancellable`, `not_reschedulable`, `not_markable`, `schedule_conflict`, `username_taken`, `email_taken`. |
| 422 | `validation_failed` (field problems in `problems`) or `slot_unavailable` (outside working hours, in the past, or not on the slot grid). |
| 429 | Rate limited by nginx. |
