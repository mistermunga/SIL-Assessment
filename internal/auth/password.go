package auth

import "golang.org/x/crypto/bcrypt"

func HashPassword(plaintext string) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	return string(hashed), err
}
