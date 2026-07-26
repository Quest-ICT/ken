package passwd

import (
	"strings"
	"testing"
)

func TestHashVerify(t *testing.T) {
	h, err := Hash("s3cret-pass", High)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=32768,t=2,p=1$") {
		t.Fatalf("unexpected PHC string: %s", h)
	}
	ok, err := Verify("s3cret-pass", h)
	if err != nil || !ok {
		t.Fatalf("verify correct: ok=%v err=%v", ok, err)
	}
	if ok, _ := Verify("wrong-pass", h); ok {
		t.Fatal("verify should fail for a wrong password")
	}
}
