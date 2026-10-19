package password

import "golang.org/x/crypto/bcrypt"

// MaxBytes is bcrypt's input limit; it ignores anything beyond it.
const MaxBytes = 72

var dummyHash = mustHash("timing-equaliser")

func mustHash(p string) []byte {
	b, err := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	if err != nil {
		panic(err) // only possible for an invalid cost or oversized input; neither applies
	}
	return b
}

func Hash(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(b), err
}

func Matches(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// SpendTime does one comparison against a throwaway hash, so rejecting an
// unknown username takes as long as rejecting a wrong password.
func SpendTime(plain string) {
	_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(plain))
}
