package main

import (
	"testing"
	"uuid"
)

func TestLoadLocalModelRequiresExplicitScope(t *testing.T) {
	for _, test := range []struct {
		env, endpoint    string
		enabled, invalid bool
	}{
		{"", "", false, false},
		{"", "http://127.0.0.1:11434", false, true},
		{uuid.NewV7().String(), "", false, true},
		{uuid.NewV7().String(), "http://127.0.0.1:11434", true, false},
	} {
		t.Setenv("HELMR_LOCAL_MODEL_ENVIRONMENT_ID", test.env)
		t.Setenv("HELMR_LOCAL_MODEL_ENDPOINT", test.endpoint)
		gateway, err := loadLocalModel()
		if (err != nil) != test.invalid || (gateway != nil) != test.enabled {
			t.Fatalf("configuration result %v %v", gateway, err)
		}
	}
}
