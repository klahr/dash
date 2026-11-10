package passhash

import (
	"strings"
	"testing"
)

// This package's tests never call UseCheapParamsInTests: Hash runs the
// production setting here (TestHashAndVerify checks its parameters).
func TestProductionParams(t *testing.T) {
	t.Parallel()
	if current() != production || production != (params{memoryKiB: 19 * 1024, passes: 2, lanes: 1}) {
		t.Fatalf("Hash uses %+v, want the production 19 MiB, 2 passes, 1 lane", current())
	}
}

// A hash made with the cheap test setting carries its parameters, so it
// verifies under the production setting, and the other way round.
func TestCheapAndProductionHashesBothVerify(t *testing.T) {
	t.Parallel()
	c, err := hashWith(cheap, "pw")
	if err != nil || !strings.HasPrefix(c, "$argon2id$v=19$m=8,t=1,p=1$") {
		t.Fatalf("cheap hash %q (%v)", c, err)
	}
	if ok, rehash, err := Verify(c, "pw"); !ok || !rehash || err != nil {
		t.Fatalf("cheap hash with production current: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	if ok, _, err := Verify(c, "nope"); ok || err != nil {
		t.Fatalf("cheap hash, wrong password: ok=%v err=%v", ok, err)
	}
	p, err := hashWith(production, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if ok, rehash, err := Verify(p, "pw"); !ok || rehash || err != nil {
		t.Fatalf("production hash: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
}
