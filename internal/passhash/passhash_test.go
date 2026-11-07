package passhash

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

func TestHashAndVerify(t *testing.T) {
	h, err := Hash("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("hash %q", h)
	}
	if h2, _ := Hash("correct horse"); h2 == h {
		t.Fatal("same salt twice")
	}
	if ok, rehash, err := Verify(h, "correct horse"); !ok || rehash || err != nil {
		t.Fatalf("right password: %v %v %v", ok, rehash, err)
	}
	if ok, _, err := Verify(h, "wrong"); ok || err != nil {
		t.Fatalf("wrong password: %v %v", ok, err)
	}
}

func TestWeakerParamsAskForRehash(t *testing.T) {
	salt := []byte("saltsaltsaltsalt")
	enc := base64.RawStdEncoding
	weak := fmt.Sprintf("$argon2id$v=19$m=8192,t=1,p=1$%s$%s", enc.EncodeToString(salt),
		enc.EncodeToString(argon2.IDKey([]byte("pw"), salt, 1, 8192, 1, 32)))
	ok, rehash, err := Verify(weak, "pw")
	if !ok || !rehash || err != nil {
		t.Fatalf("weaker parameters: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
}

func TestMalformed(t *testing.T) {
	for _, s := range []string{"", "plain", "$2a$10$abc", "$argon2id$v=19$m=x$a$b", "$argon2id$v=16$m=1,t=1,p=1$YQ$YQ"} {
		if ok, _, err := Verify(s, "x"); ok || err == nil {
			t.Errorf("%q: ok=%v err=%v", s, ok, err)
		}
	}
}
