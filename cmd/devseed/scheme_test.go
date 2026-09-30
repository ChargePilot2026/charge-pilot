package main

import "testing"

func TestDemoSchemeUsesAllConfiguredModes(t *testing.T) {
	s := demoScheme()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.Amount == nil || s.Energy == nil || len(s.Packages) != 3 || s.Card.PackageID != 2 {
		t.Fatalf("%+v", s)
	}
}
