package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadCheckTimeout(t *testing.T) {
	for _, tc := range []struct {
		line string
		want time.Duration
	}{
		{"", 0},
		{"check_timeout: 20m\n", 20 * time.Minute},
	} {
		t.Run(tc.line, func(t *testing.T) {
			root := t.TempDir()
			write(t, filepath.Join(root, ".atm.yaml"), complete+tc.line)
			c, err := Load(root, t.TempDir(), Agent{})
			if err != nil {
				t.Fatal(err)
			}
			// Reflection keeps this regression runnable against the original Config.
			field := reflect.ValueOf(c).FieldByName("CheckTimeout")
			if !field.IsValid() || time.Duration(field.Int()) != tc.want {
				t.Fatalf("check_timeout = %v, want %s", field, tc.want)
			}
		})
	}
}

func TestLoadRejectsInvalidCheckTimeout(t *testing.T) {
	for _, value := range []string{"", `""`, "~", "null", "20", "abc", "0", "0s", "-5m", "9999999999h"} {
		t.Run(value, func(t *testing.T) {
			root := t.TempDir()
			write(t, filepath.Join(root, ".atm.yaml"), complete+"check_timeout: "+value+"\n")
			_, err := Load(root, t.TempDir(), Agent{})
			if err == nil || !strings.Contains(err.Error(), "check_timeout") ||
				!strings.Contains(err.Error(), "positive duration") {
				t.Fatalf("want check_timeout positive duration error, got %v", err)
			}
		})
	}
}
