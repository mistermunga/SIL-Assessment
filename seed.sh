#!/usr/bin/env bash
# Seeds the clinic API through its public endpoints: 4 doctors (House, Wilson, Foreman, Chase + the administrator
# as the 5th), 10 patients, weekly schedules, appointments over the next ~5 weeks,
# then some cancellations and reschedules.
#
# Usage:  ADMIN_PASSWORD='...' BASE_URL='https://your-app' ./seed.sh
# Needs:  bash >= 4, curl, jq, shuf, GNU date (macOS: brew install coreutils, use gdate)
# Re-runnable: existing users are reused, but every run adds more appointments.

set -uo pipefail
export TZ=Africa/Nairobi
RANDOM=2026   # deterministic slot/patient choices

BASE_URL="${BASE_URL:-http://localhost:8080}"
ADMIN_USER="administrator"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-CHANGE_ME}"      # <-- set here or via env
SEED_PASSWORD="${SEED_PASSWORD:-Clinic#Pass2026}"  # password for every seeded user

[[ $ADMIN_PASSWORD == CHANGE_ME ]] && { echo "Set ADMIN_PASSWORD first." >&2; exit 1; }
for bin in curl jq shuf; do
  command -v "$bin" >/dev/null || { echo "missing dependency: $bin" >&2; exit 1; }
done

BASE_URL="${BASE_URL%/}"
STATUS=""; RESP=""; TOKEN=""

# ---------- helpers ----------

# api METHOD PATH [TOKEN] [JSON_BODY]  ->  sets STATUS and RESP
api() {
  local method=$1 path=$2 token=${3:-} data=${4:-} out
  local args=(-sS -X "$method" -w $'\n%{http_code}' "$BASE_URL$path")
  [[ -n $token ]] && args+=(-H "Authorization: Bearer $token")
  [[ -n $data ]] && args+=(-H "Content-Type: application/json" -d "$data")
  if ! out=$(curl "${args[@]}"); then STATUS=000; RESP=""; return 1; fi
  STATUS=${out##*$'\n'}
  RESP=${out%$'\n'*}
}

errcode() { jq -r '.error.code // "?"' <<<"$RESP" 2>/dev/null; }

# login USER PASS -> sets TOKEN
login() {
  api POST /login "" "$(jq -nc --arg u "$1" --arg p "$2" '{username:$u,password:$p}')"
  [[ $STATUS == 200 ]] || { TOKEN=""; return 1; }
  TOKEN=$(jq -r .token <<<"$RESP")
}

# Tokens live 15 minutes; refresh lazily.
ADMIN_TOK=""; ADMIN_AT=-9999
refresh_admin() {
  if (( SECONDS - ADMIN_AT > 700 )); then
    login "$ADMIN_USER" "$ADMIN_PASSWORD" || { echo "admin login failed (HTTP $STATUS)" >&2; exit 1; }
    ADMIN_TOK=$TOKEN; ADMIN_AT=$SECONDS
  fi
}
refresh_patient() {
  local i=$1
  if (( SECONDS - PAT_AT[i] > 700 )); then
    login "${PAT_USER[i]}" "$SEED_PASSWORD" && { PAT_TOK[i]=$TOKEN; PAT_AT[i]=$SECONDS; }
  fi
}

# expand_days "1-5" | "1,3,5" -> list of weekdays
expand_days() { if [[ $1 == *-* ]]; then seq "${1%-*}" "${1#*-}"; else tr ',' ' ' <<<"$1"; fi; }

# free_slots DOCTOR_ID YYYY-MM-DD -> fills SLOTS (RFC 3339 starts)
free_slots() {
  SLOTS=()
  refresh_admin
  api GET "/doctors/$1/availability?date=$2" "$ADMIN_TOK" || return 1
  [[ $STATUS == 200 ]] || return 1
  mapfile -t SLOTS < <(jq -r '.slots[].start' <<<"$RESP")
}

# ---------- data ----------

# username|first|last
DOCTORS=(
  "dr.house|Gregory|House"
  "dr.wilson|James|Wilson"
  "dr.foreman|Eric|Foreman"
  "dr.chase|Robert|Chase"
)

PAT_USER=(); PAT_TOK=(); PAT_AT=()
PATIENTS=(
  "rebecca.adler|Rebecca|Adler"
  "stacy.warner|Stacy|Warner"
  "mark.warner|Mark|Warner"
  "michael.tritter|Michael|Tritter"
  "lucas.douglas|Lucas|Douglas"
  "amber.volakis|Amber|Volakis"
  "dominika.petrova|Dominika|Petrova"
  "cate.milton|Cate|Milton"
  "jack.moriarty|Jack|Moriarty"
  "edward.vogler|Edward|Vogler"
)

# who weekdays start end break_start break_end   ("-" = no break; ADMIN = the administrator)
SCHEDULES='
Wilson   1-5    08:00 17:00 13:00 14:00
Foreman  1-4    09:00 16:00 12:30 13:30
Foreman  5      09:00 13:00 -     -
House    1,3,5  08:30 16:30 12:30 13:00
House    2,4    10:00 18:00 14:00 15:00
Chase    2-5    08:00 15:00 11:00 11:30
ADMIN    1,3    10:00 14:00 -     -
'

REASONS=(
  "Feeling better, no longer need the visit"
  "Work conflict"
  "Travel plans changed"
  "Family emergency"
  "Booked by mistake"
  "Transport difficulties"
)

# ---------- 1. doctors ----------

echo "== Doctors"
refresh_admin
for row in "${DOCTORS[@]}"; do
  IFS='|' read -r user first last <<<"$row"
  api POST /doctors "$ADMIN_TOK" "$(jq -nc --arg u "$user" --arg f "$first" --arg l "$last" --arg p "$SEED_PASSWORD" \
    '{username:$u,email:($u+"@clinic.example.com"),password:$p,firstname:$f,lastname:$l,is_admin:false}')"
  case $STATUS in
    201) echo "  created  $user" ;;
    409) echo "  exists   $user" ;;
    *)   echo "  FAILED   $user (HTTP $STATUS $(errcode))" ;;
  esac
done

# Resolve doctor IDs by surname; the one doctor left over is the administrator.
api GET /doctors "$ADMIN_TOK"
declare -A DOC_ID
known=""
for row in "${DOCTORS[@]}"; do
  IFS='|' read -r _ _ last <<<"$row"
  id=$(jq -r --arg n "$last" '[.doctors[] | select(.name | contains($n))][0].id // empty' <<<"$RESP")
  [[ -n $id ]] && { DOC_ID[$last]=$id; known+="${known:+,}$id"; }
done
admin_id=$(jq -r --argjson k "[$known]" \
  '[.doctors[] | select(([.id] | inside($k)) | not)] | if length == 1 then .[0].id else empty end' <<<"$RESP")
[[ -n $admin_id ]] && DOC_ID[ADMIN]=$admin_id || echo "  note: could not identify the administrator's doctor ID; skipping their schedule"

# ---------- 2. schedules ----------

echo "== Schedules"
while read -r who days start end bs be; do
  [[ -z ${who:-} ]] && continue
  did=${DOC_ID[$who]:-}
  [[ -z $did ]] && continue
  body=$(jq -nc --arg s "$start" --arg e "$end" --arg bs "$bs" --arg be "$be" \
    '{start:$s,end:$e} + (if $bs == "-" then {} else {break_start:$bs,break_end:$be} end)')
  for wd in $(expand_days "$days"); do
    refresh_admin
    api PUT "/doctors/$did/schedule/$wd" "$ADMIN_TOK" "$body"
    [[ $STATUS == 200 ]] || echo "  FAILED $who weekday $wd (HTTP $STATUS $(errcode): $(jq -r '.error.problems // .error.message' <<<"$RESP"))"
  done
  echo "  $who  days=$days  $start-$end  break=$bs-$be"
done <<<"$SCHEDULES"

# ---------- 3. patients ----------

echo "== Patients"
for i in "${!PATIENTS[@]}"; do
  IFS='|' read -r user first last <<<"${PATIENTS[i]}"
  PAT_USER[i]=$user
  api POST /patients "" "$(jq -nc --arg u "$user" --arg f "$first" --arg l "$last" --arg p "$SEED_PASSWORD" \
    '{username:$u,email:($u+"@example.com"),password:$p,firstname:$f,lastname:$l}')"
  if [[ $STATUS == 201 ]]; then
    PAT_TOK[i]=$(jq -r .token <<<"$RESP"); PAT_AT[i]=$SECONDS
    echo "  created  $user"
  elif [[ $STATUS == 409 ]] && login "$user" "$SEED_PASSWORD"; then
    PAT_TOK[i]=$TOKEN; PAT_AT[i]=$SECONDS
    echo "  exists   $user"
  else
    echo "  FAILED   $user (HTTP $STATUS $(errcode))"
    PAT_TOK[i]=""; PAT_AT[i]=-9999
  fi
done

# ---------- 4. appointments ----------

echo "== Appointments (tomorrow through +35 days)"
APPTS=()   # "id|patient_index|doctor_id|start"
fail=0

# try_book PATIENT_IDX DOCTOR_ID START -> 0 on success, records APPTS
try_book() {
  refresh_patient "$1"
  api POST /appointments "${PAT_TOK[$1]}" \
    "$(jq -nc --argjson d "$2" --arg s "$3" '{doctor_id:$d,start:$s}')"
  if [[ $STATUS == 201 ]]; then
    APPTS+=("$(jq -r .id <<<"$RESP")|$1|$2|$3")
    return 0
  fi
  LAST_ERR=$(errcode)
  return 1
}

for off in $(seq 1 35); do
  day=$(date -d "+$off days" +%F)
  (( $(date -d "$day" +%u) > 5 )) && continue
  n=1; (( off <= 10 )) && n=2; (( off <= 3 )) && n=3   # busier near term

  for did in "${DOC_ID[@]}"; do
    (( RANDOM % 10 < 3 )) && continue                  # some doctor-days stay empty
    free_slots "$did" "$day" || continue
    for ((k = 0; k < n && ${#SLOTS[@]} > 0; k++)); do
      si=$((RANDOM % ${#SLOTS[@]}))
      slot=${SLOTS[si]}
      SLOTS=("${SLOTS[@]:0:si}" "${SLOTS[@]:si+1}")
      ok=0
      for attempt in 1 2 3; do                          # retry with another patient if one is busy
        pi=$((RANDOM % ${#PATIENTS[@]}))
        [[ -z ${PAT_TOK[$pi]} ]] && continue
        try_book "$pi" "$did" "$slot" && { ok=1; break; }
        [[ $LAST_ERR == patient_busy ]] || break
      done
      (( ok )) || { fail=$((fail + 1)); echo "  skip  doctor $did $slot ($LAST_ERR)"; }
    done
  done
done
echo "  booked ${#APPTS[@]}, skipped $fail"

# ---------- 5. cancellations and reschedules ----------

mapfile -t order < <(printf '%s\n' "${!APPTS[@]}" | shuf)
cancelled=0; moved=0

echo "== Cancellations"
for idx in "${order[@]:0:8}"; do
  IFS='|' read -r id pi did start <<<"${APPTS[idx]}"
  refresh_patient "$pi"
  reason=${REASONS[RANDOM % ${#REASONS[@]}]}
  api PATCH "/appointments/$id/cancel" "${PAT_TOK[$pi]}" "$(jq -nc --arg r "$reason" '{reason:$r}')"
  if [[ $STATUS == 204 ]]; then
    cancelled=$((cancelled + 1)); echo "  cancelled #$id ($reason)"
  else
    echo "  FAILED cancel #$id (HTTP $STATUS $(errcode))"
  fi
done

echo "== Reschedules"
for idx in "${order[@]:8:6}"; do
  IFS='|' read -r id pi did start <<<"${APPTS[idx]}"
  refresh_patient "$pi"
  target=""
  for shift_days in 7 14; do                            # same weekday, a week or two later
    d=$(date -d "${start%%T*} +$shift_days days" +%F)
    free_slots "$did" "$d" && (( ${#SLOTS[@]} > 0 )) && { target=${SLOTS[RANDOM % ${#SLOTS[@]}]}; break; }
  done
  [[ -z $target ]] && { echo "  no free slot to move #$id"; continue; }
  api PATCH "/appointments/$id/reschedule" "${PAT_TOK[$pi]}" "$(jq -nc --arg s "$target" '{start:$s}')"
  if [[ $STATUS == 200 ]]; then
    moved=$((moved + 1)); echo "  moved #$id -> $target"
  else
    echo "  FAILED reschedule #$id (HTTP $STATUS $(errcode))"
  fi
done

# ---------- summary ----------

echo
echo "Done: ${#APPTS[@]} booked, $cancelled cancelled, $moved rescheduled."
echo "Logins (password for all: $SEED_PASSWORD):"
for row in "${DOCTORS[@]}"; do echo "  doctor   ${row%%|*}"; done
echo "  admin    $ADMIN_USER"
printf '  patient  %s\n' "${PAT_USER[@]}"