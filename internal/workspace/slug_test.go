package workspace

import "testing"

func TestSlug(t *testing.T) {
	got := Slug("MQTT.Ingestion_Basic")
	if got != "mqtt-ingestion-basic" {
		t.Fatalf("Slug() = %q", got)
	}
}

func TestDNSLabelTruncates(t *testing.T) {
	got := DNSLabel("abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz")
	if len(got) != 63 {
		t.Fatalf("len(DNSLabel()) = %d", len(got))
	}
	if got[52] != '-' {
		t.Fatalf("expected hash separator at byte 52, got %q", got[52])
	}
}

func TestValidateLabelValue(t *testing.T) {
	for _, value := range []string{
		"run-fixed-test",
		"run_1.2",
		"abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijk",
	} {
		if err := ValidateLabelValue("field", value); err != nil {
			t.Fatalf("ValidateLabelValue(%q) returned %v", value, err)
		}
	}
	for _, value := range []string{
		"",
		"-run",
		"run-",
		"run/value",
		"abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijkl",
	} {
		if err := ValidateLabelValue("field", value); err == nil {
			t.Fatalf("ValidateLabelValue(%q) unexpectedly passed", value)
		}
	}
}

func TestValidateDNS1123Label(t *testing.T) {
	for _, value := range []string{
		"spex-test",
		"a",
		"abc123",
	} {
		if err := ValidateDNS1123Label("field", value); err != nil {
			t.Fatalf("ValidateDNS1123Label(%q) returned %v", value, err)
		}
	}
	for _, value := range []string{
		"",
		"Spex",
		"spex_test",
		"-spex",
		"spex-",
		"abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijkl",
	} {
		if err := ValidateDNS1123Label("field", value); err == nil {
			t.Fatalf("ValidateDNS1123Label(%q) unexpectedly passed", value)
		}
	}
}

func TestValidateDNS1123Subdomain(t *testing.T) {
	for _, value := range []string{
		"mqtt-probe-credentials",
		"service.account",
		"a",
	} {
		if err := ValidateDNS1123Subdomain("field", value); err != nil {
			t.Fatalf("ValidateDNS1123Subdomain(%q) returned %v", value, err)
		}
	}
	for _, value := range []string{
		"",
		"MQTT",
		"mqtt_probe_credentials",
		"-mqtt",
		"mqtt-",
		"mqtt.-probe",
	} {
		if err := ValidateDNS1123Subdomain("field", value); err == nil {
			t.Fatalf("ValidateDNS1123Subdomain(%q) unexpectedly passed", value)
		}
	}
}
