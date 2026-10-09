package segments

import (
	"math"
	"reflect"
	"testing"
)

func TestNormalizeKeepsSeparateCreditsAndRejectsInvalidTimes(t *testing.T) {
	intro := Segment{Type: Intro, Start: 30, End: 90, Source: "chapters", AutoSkip: true}
	input := []Segment{
		{Type: Credits, Start: 900, End: 1000}, intro,
		{Type: Credits, Start: 1100, End: 1200}, intro,
		{Type: Intro, Start: -1, End: 20},
		{Type: Intro, Start: 3, End: 3},
		{Type: Intro, Start: 5, End: 4},
		{Type: Intro, Start: 3, End: 1201},
		{Type: Intro, Start: math.NaN(), End: 100},
		{Type: Credits, Start: 30, End: math.Inf(1)},
		{Type: "scene", Start: 30, End: 100},
	}
	want := []Segment{intro, {Type: Credits, Start: 900, End: 1000}, {Type: Credits, Start: 1100, End: 1200}}
	if got := Normalize(input, 1200); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if len(Normalize(input, 0)) != 0 || len(Normalize(input, math.Inf(1))) != 0 {
		t.Fatal("unknown duration accepted")
	}
	if input[0].Start != 900 {
		t.Fatal("input was mutated")
	}
}
