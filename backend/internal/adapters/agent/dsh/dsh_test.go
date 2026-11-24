package dsh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifest(t *testing.T) {
	m := (&Plugin{}).Manifest()
	if m.ID != "dsh" {
		t.Fatalf("ID = %q, want dsh", m.ID)
	}
	if m.Name != "DeepSeek Harness" {
		t.Fatalf("Name = %q, want DeepSeek Harness", m.Name)
	}
	hasAgent := false
	for _, capability := range m.Capabilities {
		if capability == adapters.CapabilityAgent {
			hasAgent = true
		}
	}
	if !hasAgent {
		t.Fatal("missing CapabilityAgent")
	}
}

func TestGetConfigSpecReportsModelField(t *testing.T) {
	spec, err := (&Plugin{}).GetConfigSpec(context.Background())
	if err != nil {
		t.Fatalf("GetConfigSpec: %v", err)
	}
	if len(spec.Fields) != 1 || spec.Fields[0].Key != "model" || spec.Fields[0].Type != ports.ConfigFieldString {
		t.Fatalf("config fields = %#v, want one string model field", spec.Fields)
	}
}

func TestGetLaunchCommandReportsChatOnly(t *testing.T) {
	cmd, err := (&Plugin{resolvedBinary: "dsh"}).GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt: "implement the issue",
	})
	if err == nil {
		t.Fatalf("GetLaunchCommand = %#v, nil; want a Chat-only error", cmd)
	}
	if !errors.Is(err, ErrChatOnly) {
		t.Fatalf("error = %v, want ErrChatOnly", err)
	}
	if !strings.Contains(err.Error(), "Chat mode only") {
		t.Fatalf("error = %q, want it to say Chat mode only", err)
	}
	if cmd != nil {
		t.Fatalf("command = %#v, want nil", cmd)
	}
}

func TestGetRestoreCommandReportsNoNativeRestore(t *testing.T) {
	cmd, ok, err := (&Plugin{resolvedBinary: "dsh"}).GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "native-1"}},
	})
	if err != nil {
		t.Fatalf("GetRestoreCommand: %v", err)
	}
	if ok || cmd != nil {
		t.Fatalf("cmd = %#v, ok = %v; want nil, false because DSH is Chat-only", cmd, ok)
	}
}

func TestSessionInfoUsesStandardMetadata(t *testing.T) {
	info, ok, err := (&Plugin{}).SessionInfo(context.Background(), ports.SessionRef{
		Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: "native-1",
			ports.MetadataKeyTitle:          "Fix the tests",
		},
	})
	if err != nil {
		t.Fatalf("SessionInfo: %v", err)
	}
	if !ok || info.AgentSessionID != "native-1" || info.Title != "Fix the tests" {
		t.Fatalf("info = %+v, ok = %v", info, ok)
	}
}

func TestAuthStatusIsUnknownWithoutProbing(t *testing.T) {
	status, err := (&Plugin{resolvedBinary: filepath.Join(t.TempDir(), "missing-dsh")}).AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want unknown", status)
	}
}

func TestResolveBinaryPrefersPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake dsh binary uses a Unix shebang")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "dsh")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 0.2.1-rc.2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	got, err := ResolveDSHBinary(context.Background())
	if err != nil {
		t.Fatalf("ResolveDSHBinary: %v", err)
	}
	if got != fake {
		t.Fatalf("resolved %q, want %q", got, fake)
	}
}

func TestResolveBinaryMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix candidate layout is the subject of this test")
	}
	original := dshBinarySpec
	dshBinarySpec = binaryutil.BinarySpec{
		Label:    "dsh",
		Names:    []string{"dsh"},
		WinNames: []string{"dsh.cmd", "dsh.exe", "dsh"},
	}
	t.Cleanup(func() { dshBinarySpec = original })
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("VOLTA_HOME", t.TempDir())
	t.Setenv("FNM_DIR", t.TempDir())

	_, err := ResolveDSHBinary(context.Background())
	if !errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatalf("ResolveDSHBinary error = %v, want ErrAgentBinaryNotFound", err)
	}
}

func TestResolveBinaryHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ResolveDSHBinary(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveDSHBinary error = %v, want context.Canceled", err)
	}
}

func TestPluginCachesResolvedBinary(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "/opt/dsh"}
	got, err := plugin.ResolveBinary(context.Background())
	if err != nil {
		t.Fatalf("ResolveBinary: %v", err)
	}
	if got != "/opt/dsh" {
		t.Fatalf("ResolveBinary = %q, want cached path", got)
	}
}

func TestBinarySpecCoversRequiredLocations(t *testing.T) {
	if !reflect.DeepEqual(dshBinarySpec.Names, []string{"dsh"}) {
		t.Fatalf("Names = %#v", dshBinarySpec.Names)
	}
	foundUserLocal := false
	for _, path := range dshBinarySpec.UnixPaths {
		if path == "/home/syk/.local/bin/dsh" {
			foundUserLocal = true
		}
	}
	if !foundUserLocal {
		t.Fatalf("UnixPaths = %#v, want the explicit /home/syk/.local/bin/dsh fallback", dshBinarySpec.UnixPaths)
	}
	foundHomeLocal := false
	for _, parts := range dshBinarySpec.UnixHomePaths {
		if reflect.DeepEqual(parts, []string{".local", "bin", "dsh"}) {
			foundHomeLocal = true
		}
	}
	if !foundHomeLocal {
		t.Fatalf("UnixHomePaths = %#v, want ~/.local/bin/dsh", dshBinarySpec.UnixHomePaths)
	}
	if !dshBinarySpec.NodeManaged {
		t.Fatal("NodeManaged = false, want npm/Node-managed global bins searched")
	}
}
