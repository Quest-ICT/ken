package embed

import "testing"

func TestFromEnvNoneAndHashDim(t *testing.T) {
	// Unset provider -> disabled (nil, nil).
	t.Setenv("KEN_EMBED_PROVIDER", "")
	if e, err := FromEnv(); err != nil || e != nil {
		t.Fatalf("no provider should be (nil,nil): %v %v", e, err)
	}

	// Hash provider with a non-positive dimension must error, not panic later.
	t.Setenv("KEN_EMBED_PROVIDER", "hash")
	t.Setenv("KEN_EMBED_DIM", "0")
	if _, err := FromEnv(); err == nil {
		t.Fatal("hash provider with DIM=0 should return an error")
	}

	// A valid dimension works.
	t.Setenv("KEN_EMBED_DIM", "64")
	e, err := FromEnv()
	if err != nil || e == nil || e.Dimension() != 64 {
		t.Fatalf("hash dim 64 should work: e=%v err=%v", e, err)
	}
	if vecs, err := e.Embed(t.Context(), []string{"hello world tokens"}); err != nil || len(vecs) != 1 || len(vecs[0]) != 64 {
		t.Fatalf("embed: %v %v", vecs, err)
	}
}
