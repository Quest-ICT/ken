package lang_test

import (
	"testing"

	"github.com/Quest-ICT/ken/internal/lang"
)

func TestDetectKnownLanguages(t *testing.T) {
	d := lang.New()
	cases := map[string]string{
		"en": "The quick brown fox jumps over the lazy dog and then runs across the green field toward the river.",
		"es": "El rápido zorro marrón salta sobre el perro perezoso y luego corre por el campo verde hacia el río.",
		"fr": "Le rapide renard brun saute par-dessus le chien paresseux puis court à travers le champ vert vers la rivière.",
		"zh": "快速的棕色狐狸跳过了懒惰的狗，然后穿过绿色的田野一直跑向河流边上的树林。",
	}
	for want, text := range cases {
		if got := d.Detect(text); got != want {
			t.Errorf("Detect(%.20q…) = %q, want %q", text, got, want)
		}
	}
}

func TestDetectUndecidable(t *testing.T) {
	d := lang.New()
	for _, text := range []string{"", "  ", "hi", "ok", "12 34 56", "x", "->", "a b c"} {
		if got := d.Detect(text); got != lang.Und {
			t.Errorf("Detect(%q) = %q, want %q (too short/no letters ⇒ undetermined)", text, got, lang.Und)
		}
	}
}
