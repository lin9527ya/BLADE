package db

import (
	"reflect"
	"testing"
)

func TestBuiltinColdCoffeeVisibility(t *testing.T) {
	got := builtinSkillVisibility["coldcoffee"]
	want := []string{"auto", "pentest", "mainagent"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("coldcoffee default visibility = %v, want %v", got, want)
	}
}
