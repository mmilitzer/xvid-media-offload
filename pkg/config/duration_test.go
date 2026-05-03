package config

import (
	"testing"
	"time"
)

func TestParseHumanDurationStandard(t *testing.T) {
	d, err := ParseHumanDuration("720h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != 720*time.Hour {
		t.Errorf("expected 720h, got %v", d)
	}
}

func TestParseHumanDurationDays(t *testing.T) {
	d, err := ParseHumanDuration("30d")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != 30*24*time.Hour {
		t.Errorf("expected 30d, got %v", d)
	}
}

func TestParseHumanDurationWeeks(t *testing.T) {
	d, err := ParseHumanDuration("2w")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != 14*24*time.Hour {
		t.Errorf("expected 2w, got %v", d)
	}
}

func TestParseHumanDurationMonths(t *testing.T) {
	d, err := ParseHumanDuration("6mo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != 180*24*time.Hour {
		t.Errorf("expected 6mo, got %v", d)
	}
}

func TestParseHumanDurationYears(t *testing.T) {
	d, err := ParseHumanDuration("1y")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != 365*24*time.Hour {
		t.Errorf("expected 1y, got %v", d)
	}
}

func TestParseHumanDurationCompound(t *testing.T) {
	d, err := ParseHumanDuration("1y 6mo 15d")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := 365*24*time.Hour + 180*24*time.Hour + 15*24*time.Hour
	if d != expected {
		t.Errorf("expected %v, got %v", expected, d)
	}
}

func TestParseHumanDurationInvalid(t *testing.T) {
	_, err := ParseHumanDuration("")
	if err == nil {
		t.Fatal("expected error for empty duration")
	}
	_, err = ParseHumanDuration("10x")
	if err == nil {
		t.Fatal("expected error for unknown unit")
	}
}
