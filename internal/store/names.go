package store

import (
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

// FullName joins nullable first and last names, skipping missing parts.
func FullName(first, last pgtype.Text) string {
	return strings.TrimSpace(first.String + " " + last.String)
}
