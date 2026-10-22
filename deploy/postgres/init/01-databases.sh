#!/usr/bin/env bash
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres \
     -v app_pw="$CLINIC_DB_PASSWORD" -v test_pw="$CLINIC_TEST_DB_PASSWORD" <<'EOSQL'
CREATE ROLE clinic LOGIN PASSWORD :'app_pw';
CREATE DATABASE clinic OWNER clinic;
CREATE ROLE clinic_test LOGIN PASSWORD :'test_pw';
CREATE DATABASE clinic_test OWNER clinic_test;
-- each role can reach only its own database
REVOKE CONNECT ON DATABASE clinic FROM PUBLIC;
REVOKE CONNECT ON DATABASE clinic_test FROM PUBLIC;
EOSQL