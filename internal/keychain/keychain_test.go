package keychain

import (
	"os"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("CI runners have no unlocked login keychain")
	}
	Service = "mac-cleaner-test"
	t.Cleanup(func() { Delete() })

	if err := Set("ts_test-key.123"); err != nil {
		t.Fatal(err)
	}
	if got, err := Get(); err != nil || got != "ts_test-key.123" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := Delete(); err != nil {
		t.Fatal(err)
	}
	if got, err := Get(); err != nil || got != "" {
		t.Fatalf("after Delete: %q, %v", got, err)
	}
}

func TestSetRejectsInjection(t *testing.T) {
	Service = "mac-cleaner-test"
	for _, k := range []string{"short", "has space in it", "key\nadd-generic-password -s x", `quote"breaks"out`} {
		if err := Set(k); err == nil {
			Delete()
			t.Errorf("accepted %q", k)
		}
	}
}
