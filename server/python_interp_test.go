package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestValidatePythonInterp(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	dir := t.TempDir()
	notExec := filepath.Join(dir, "python3")
	if err := os.WriteFile(notExec, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	notPython := filepath.Join(dir, "sh")
	if err := os.WriteFile(notPython, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"empty resets to auto-detect", "", false},
		{"relative path", "python3", true},
		{"directory", dir, true},
		{"not executable", notExec, true},
		{"basename not python", notPython, true},
		{"missing file", filepath.Join(dir, "python3.11"), true},
		{"real python3", python, false},
	}
	for _, tc := range cases {
		err := validatePythonInterp(tc.path)
		if tc.wantErr && err == nil {
			t.Errorf("%s: want error for %q", tc.name, tc.path)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: unexpected error for %q: %v", tc.name, tc.path, err)
		}
	}
}
