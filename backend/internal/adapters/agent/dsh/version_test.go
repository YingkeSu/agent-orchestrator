package dsh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		input string
		ok    bool
	}{
		{input: "0.2.1-rc.2", ok: true},
		{input: "0.2.1", ok: true},
		{input: "v0.2.1", ok: true},
		{input: "dsh 0.2.1-rc.2", ok: true},
		{input: "0.2.1+build.5", ok: true},
		{input: "0.2.1-rc.2+build.5", ok: true},
		{input: "0.2.1-rc.2\nwarning: 9.9.9 available", ok: true},
		{input: "not a version"},
		{input: ""},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			_, ok := ParseVersion(test.input)
			if ok != test.ok {
				t.Fatalf("ParseVersion(%q) ok = %v, want %v", test.input, ok, test.ok)
			}
		})
	}
}

func TestVersionComparison(t *testing.T) {
	minimum := mustVersion(t, MinimumVersion)
	tests := []struct {
		version string
		older   bool
	}{
		{version: "0.2.1-rc.2"},
		{version: "0.2.1-rc.3"},
		{version: "0.2.1-rc.10"},
		{version: "0.2.1"},
		{version: "0.2.2"},
		{version: "0.3.0"},
		{version: "0.2.0", older: true},
		{version: "0.2.1-rc.1", older: true},
		{version: "0.2.1-alpha", older: true},
	}
	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			got := mustVersion(t, test.version)
			if got.Less(minimum) != test.older {
				t.Fatalf("%s less than %s = %v, want %v", test.version, MinimumVersion, got.Less(minimum), test.older)
			}
		})
	}
}

func TestValidateVersionOutput(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		wantErr bool
	}{
		{name: "tested minimum", output: "0.2.1-rc.2\n"},
		{name: "newer release", output: "0.3.0\n"},
		{name: "newer prerelease", output: "0.2.1-rc.3\n"},
		{name: "older", output: "0.2.0\n", wantErr: true},
		{name: "older prerelease", output: "0.2.1-rc.1\n", wantErr: true},
		{name: "unrecognized", output: "deepseek harness\n", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateVersionOutput(test.output)
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateVersionOutput(%q) error = %v, wantErr %v", test.output, err, test.wantErr)
			}
		})
	}
}

func TestProbeVersionRunsDSHVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake dsh binary uses a Unix shebang")
	}
	bin := fakeVersionBinary(t, "0.2.1-rc.2")
	if err := ProbeVersion(context.Background(), bin); err != nil {
		t.Fatalf("ProbeVersion: %v", err)
	}
}

func TestProbeVersionRejectsOlderBuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake dsh binary uses a Unix shebang")
	}
	bin := fakeVersionBinary(t, "0.2.0")
	err := ProbeVersion(context.Background(), bin)
	if err == nil || !strings.Contains(err.Error(), MinimumVersion) {
		t.Fatalf("ProbeVersion error = %v, want a minimum-version error naming %s", err, MinimumVersion)
	}
}

func TestProbeVersionRejectsMissingBinary(t *testing.T) {
	err := ProbeVersion(context.Background(), filepath.Join(t.TempDir(), "missing-dsh"))
	if err == nil {
		t.Fatal("ProbeVersion error = nil, want an execution error")
	}
}

func TestProbeVersionHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ProbeVersion(ctx, "/bin/true"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ProbeVersion error = %v, want context.Canceled", err)
	}
}

func fakeVersionBinary(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dsh")
	script := "#!/bin/sh\nif [ \"${1:-}\" = \"--version\" ]; then\n  echo \"" + version + "\"\n  exit 0\nfi\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustVersion(t *testing.T, value string) Version {
	t.Helper()
	parsed, ok := ParseVersion(value)
	if !ok {
		t.Fatalf("ParseVersion(%q) failed", value)
	}
	return parsed
}


func TestProbeVersionIgnoresStderrWarnings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake dsh binary uses a Unix shebang")
	}
	path := filepath.Join(t.TempDir(), "dsh")
	script := "#!/bin/sh\nif [ \"${1:-}\" = \"--version\" ]; then\n  echo \"warning: Node 18.20.4\" >&2\n  echo \"0.2.1-rc.2\"\n  exit 0\nfi\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ProbeVersion(context.Background(), path); err != nil {
		t.Fatalf("ProbeVersion with stderr noise: %v", err)
	}
}
