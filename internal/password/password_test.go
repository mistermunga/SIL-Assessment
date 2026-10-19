package password_test

import (
	"testing"

	"clinic/internal/password"
)

func TestHashAndMatches(t *testing.T) {
	h, err := password.Hash("correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	if !password.Matches(h, "correct-horse") {
		t.Error("correct password rejected")
	}
	if password.Matches(h, "wrong") {
		t.Error("wrong password accepted")
	}
}
