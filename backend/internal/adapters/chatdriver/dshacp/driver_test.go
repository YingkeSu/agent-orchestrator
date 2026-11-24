package dshacp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	"gopkg.in/yaml.v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/dsh"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakePlugin struct {
	binary       string
	binaryErr    error
	status       ports.AgentAuthStatus
	statusErr    error
	resolveCalls int
	authCalls    int
}

func (p *fakePlugin) ResolveBinary(context.Context) (string, error) {
	p.resolveCalls++
	return p.binary, p.binaryErr
}

func (p *fakePlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	p.authCalls++
	return p.status, p.statusErr
}

func TestProbeChecksBinaryAndVersionWithoutAuthProbe(t *testing.T) {
	plugin := &fakePlugin{binary: "/usr/local/bin/dsh", status: ports.AgentAuthStatusUnauthorized}
	versionCalls := 0
	driver := newDriver(plugin, func(_ context.Context, bin string) error {
		versionCalls++
		if bin != "/usr/local/bin/dsh" {
			t.Fatalf("version probe binary = %q", bin)
		}
		return nil
	}, nil)

	caps, err := driver.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if driver.Harness() != domain.HarnessDSH {
		t.Fatalf("harness = %q, want dsh", driver.Harness())
	}
	if plugin.resolveCalls != 1 || versionCalls != 1 {
		t.Fatalf("resolve calls = %d, version calls = %d; want one each", plugin.resolveCalls, versionCalls)
	}
	if plugin.authCalls != 0 {
		t.Fatalf("AuthStatus calls = %d, want 0: DSH ACP probing must not inspect model credentials", plugin.authCalls)
	}
	for _, capability := range []ports.ChatCapability{
		ports.ChatCapabilityStreaming, ports.ChatCapabilityTools,
		ports.ChatCapabilityApprovals, ports.ChatCapabilityInterrupt,
		ports.ChatCapabilityResume, ports.ChatCapabilityUsage,
	} {
		if !caps.Has(capability) {
			t.Errorf("missing capability %q", capability)
		}
	}
	for _, capability := range []ports.ChatCapability{
		ports.ChatCapabilityHistory, ports.ChatCapabilityDiffs, ports.ChatCapabilityPlans,
	} {
		if caps.Has(capability) {
			t.Errorf("capability %q = true, but DSH ACP does not provide it", capability)
		}
	}
	if missing := ports.MissingProductionCapabilities(caps); len(missing) != 0 {
		t.Fatalf("missing production capabilities = %v, want none", missing)
	}
}

func TestProbeRejectsMissingBinary(t *testing.T) {
	want := errors.New("not found")
	driver := newDriver(&fakePlugin{binaryErr: want}, func(context.Context, string) error { return nil }, nil)
	_, err := driver.Probe(context.Background())
	if !errors.Is(err, ports.ErrChatDriverUnavailable) || !errors.Is(err, want) {
		t.Fatalf("Probe error = %v, want ErrChatDriverUnavailable wrapping the resolution error", err)
	}
}

func TestProbeRejectsIncompatibleVersion(t *testing.T) {
	want := errors.New("too old")
	driver := newDriver(&fakePlugin{binary: "/usr/local/bin/dsh"}, func(context.Context, string) error {
		return want
	}, nil)
	_, err := driver.Probe(context.Background())
	if !errors.Is(err, ports.ErrChatDriverIncompatible) || !errors.Is(err, want) {
		t.Fatalf("Probe error = %v, want ErrChatDriverIncompatible wrapping the version error", err)
	}
}

func TestLaunchUsesACPProfileAndWritesStandingPatch(t *testing.T) {
	dataDir := t.TempDir()
	cfg := buildConfig(&fakePlugin{binary: "/usr/local/bin/dsh"}, nil, nil)
	if err := cfg.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}

	prompt := "AO standing instructions.\nFollow the repository rules: keep changes small.\n"
	launch, err := cfg.Launch(context.Background(), acpdriver.LaunchConfig{
		SessionID:     "session-1",
		DataDir:       dataDir,
		WorkspacePath: filepath.Join(dataDir, "worktree"),
		Env:           map[string]string{"PATH": "/usr/local/bin", "KEEP": "yes"},
		SystemPrompt:  prompt,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if launch.Command != "/usr/local/bin/dsh" {
		t.Fatalf("command = %q", launch.Command)
	}
	patchPath := filepath.Join(dataDir, "prompts", "session-1", "dsh", "ao.patch.yml")
	wantArgs := []string{"--profile", "acp", "--patch", patchPath}
	if !reflect.DeepEqual(launch.Args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", launch.Args, wantArgs)
	}
	if launch.Env["PATH"] != "/usr/local/bin" || launch.Env["KEEP"] != "yes" {
		t.Fatalf("env = %#v, want the session environment preserved", launch.Env)
	}
	if _, ok := launch.Env["DSH_PERMISSION_MODE"]; ok {
		t.Fatalf("env unexpectedly sets DSH_PERMISSION_MODE: %#v", launch.Env)
	}

	data, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatalf("read generated patch: %v", err)
	}
	var entries []struct {
		ID     string `yaml:"id"`
		Config struct {
			PersonaPrefix string `yaml:"personaPrefix"`
		} `yaml:"config"`
	}
	if err := yaml.Unmarshal(data, &entries); err != nil {
		t.Fatalf("parse generated patch: %v\n%s", err, data)
	}
	if len(entries) != 1 || entries[0].ID != "system-prompt" {
		t.Fatalf("patch entries = %#v, want one system-prompt override\n%s", entries, data)
	}
	if got, want := entries[0].Config.PersonaPrefix, strings.TrimSpace(prompt); got != want {
		t.Fatalf("personaPrefix = %q, want %q", got, want)
	}
}

func TestLaunchWithoutSystemPromptSkipsPatch(t *testing.T) {
	dataDir := t.TempDir()
	cfg := buildConfig(&fakePlugin{binary: "dsh"}, nil, nil)
	launch, err := cfg.Launch(context.Background(), acpdriver.LaunchConfig{
		SessionID: "session-2",
		DataDir:   dataDir,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if want := []string{"--profile", "acp"}; !reflect.DeepEqual(launch.Args, want) {
		t.Fatalf("args = %#v, want %#v", launch.Args, want)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "prompts", "session-2", "dsh")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("instruction directory stat error = %v, want os.ErrNotExist", err)
	}
}

func TestLaunchMapsBypassPermissionsToDangerFullAccess(t *testing.T) {
	cfg := buildConfig(&fakePlugin{binary: "dsh"}, nil, nil)
	launch, err := cfg.Launch(context.Background(), acpdriver.LaunchConfig{
		SessionID:   "session-3",
		DataDir:     t.TempDir(),
		Permissions: ports.PermissionModeBypassPermissions,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if got := launch.Env["DSH_PERMISSION_MODE"]; got != "danger-full-access" {
		t.Fatalf("DSH_PERMISSION_MODE = %q, want danger-full-access", got)
	}
}

func TestLaunchKeepsWorkspaceWriteSandboxForOtherModes(t *testing.T) {
	for _, mode := range []ports.PermissionMode{
		ports.PermissionModeDefault, ports.PermissionModeAcceptEdits, ports.PermissionModeAuto,
	} {
		t.Run(string(mode), func(t *testing.T) {
			cfg := buildConfig(&fakePlugin{binary: "dsh"}, nil, nil)
			launch, err := cfg.Launch(context.Background(), acpdriver.LaunchConfig{
				SessionID:   "session-4",
				DataDir:     t.TempDir(),
				Permissions: mode,
			})
			if err != nil {
				t.Fatalf("Launch: %v", err)
			}
			if _, ok := launch.Env["DSH_PERMISSION_MODE"]; ok {
				t.Fatalf("env = %#v, want DSH's own workspace-write default", launch.Env)
			}
		})
	}
}

func TestLaunchHonorsCanceledContextBeforeWritingPatch(t *testing.T) {
	dataDir := t.TempDir()
	cfg := buildConfig(&fakePlugin{binary: "dsh"}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := cfg.Launch(ctx, acpdriver.LaunchConfig{
		SessionID:    "session-5",
		DataDir:      dataDir,
		SystemPrompt: "AO standing instructions",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Launch error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "prompts", "session-5", "dsh")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("instruction directory stat error = %v, want os.ErrNotExist", err)
	}
}

func TestLaunchRequiresAbsoluteDataDirectoryForPatch(t *testing.T) {
	cfg := buildConfig(&fakePlugin{binary: "dsh"}, nil, nil)
	_, err := cfg.Launch(context.Background(), acpdriver.LaunchConfig{
		SessionID:    "session-6",
		DataDir:      "relative/data",
		SystemPrompt: "AO standing instructions",
	})
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("Launch error = %v, want an absolute-data-directory error", err)
	}
}

func TestSessionOptionsUseDSHConfigOptions(t *testing.T) {
	if got := sessionOptions(ports.ChatTurnSettings{}); got != nil {
		t.Fatalf("empty settings = %#v, want nil", got)
	}
	got := sessionOptions(ports.ChatTurnSettings{
		Model:  `["deepseek-official","deepseek-v4-pro"]`,
		Effort: "max",
	})
	want := []acpdriver.SessionOption{
		{ID: "model", Value: `["deepseek-official","deepseek-v4-pro"]`},
		{ID: "reasoning_effort", Value: "max"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("session options = %#v, want %#v", got, want)
	}
	if got := sessionOptions(ports.ChatTurnSettings{Model: "  "}); got != nil {
		t.Fatalf("blank model settings = %#v, want nil", got)
	}
}

func TestValidateInitialize(t *testing.T) {
	info := func(name, version string) acpsdk.InitializeResponse {
		return acpsdk.InitializeResponse{AgentInfo: &acpsdk.Implementation{Name: name, Version: version}}
	}
	for _, test := range []struct {
		name    string
		init    acpsdk.InitializeResponse
		wantErr bool
	}{
		{name: "tested bridge", init: info("deepseek-harness-acp", "0.0.1")},
		{name: "newer bridge", init: info("deepseek-harness-acp", "0.1.0")},
		{name: "missing agent info is accepted", init: acpsdk.InitializeResponse{}},
		{name: "anonymous agent info is accepted", init: info("", "")},
		{name: "wrong agent", init: info("pi-acp", "0.0.1"), wantErr: true},
		{name: "unparseable version", init: info("deepseek-harness-acp", "unknown"), wantErr: true},
		{name: "older bridge", init: info("deepseek-harness-acp", "0.0.0"), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateInitialize(test.init)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateInitialize error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestDefaultVersionProbeIsDSHVersion(t *testing.T) {
	if os.Getenv("AO_TEST_REAL_DSH") == "" {
		t.Skip("set AO_TEST_REAL_DSH=1 to exercise the installed dsh binary")
	}
	driver := newDriver(&fakePlugin{binary: "/home/syk/.local/bin/dsh"}, dsh.ProbeVersion, nil)
	if _, err := driver.Probe(context.Background()); err != nil {
		t.Fatalf("Probe installed dsh: %v", err)
	}
}

func TestPermissionPolicyAnswersOnlyAuthorizedModes(t *testing.T) {
	request := func(kind acpsdk.ToolKind, options ...acpsdk.PermissionOption) acpsdk.RequestPermissionRequest {
		toolKind := kind
		return acpsdk.RequestPermissionRequest{
			ToolCall: acpsdk.ToolCallUpdate{Kind: &toolKind},
			Options:  options,
		}
	}
	allowOnce := acpsdk.PermissionOption{Kind: acpsdk.PermissionOptionKindAllowOnce, OptionId: "allow-once"}
	allowAlways := acpsdk.PermissionOption{Kind: acpsdk.PermissionOptionKindAllowAlways, OptionId: "allow-always"}
	reject := acpsdk.PermissionOption{Kind: acpsdk.PermissionOptionKindRejectOnce, OptionId: "reject"}

	tests := []struct {
		name   string
		mode   ports.PermissionMode
		req    acpsdk.RequestPermissionRequest
		wantID string
		wantOK bool
	}{
		{
			name: "default leaves edits for a human", mode: ports.PermissionModeDefault,
			req: request(acpsdk.ToolKindEdit, allowOnce),
		},
		{
			name: "accept edits allows file mutation", mode: ports.PermissionModeAcceptEdits,
			req: request(acpsdk.ToolKindEdit, reject, allowOnce), wantID: "allow-once", wantOK: true,
		},
		{
			name: "accept edits allows delete", mode: ports.PermissionModeAcceptEdits,
			req: request(acpsdk.ToolKindDelete, allowOnce), wantID: "allow-once", wantOK: true,
		},
		{
			name: "accept edits leaves command execution for a human", mode: ports.PermissionModeAcceptEdits,
			req: request(acpsdk.ToolKindExecute, allowOnce),
		},
		{
			name: "auto prefers allow always", mode: ports.PermissionModeAuto,
			req: request(acpsdk.ToolKindExecute, allowOnce, allowAlways), wantID: "allow-always", wantOK: true,
		},
		{
			name: "auto falls back to allow once", mode: ports.PermissionModeAuto,
			req: request(acpsdk.ToolKindExecute, reject, allowOnce), wantID: "allow-once", wantOK: true,
		},
		{
			name: "auto without allow option is left pending", mode: ports.PermissionModeAuto,
			req: request(acpsdk.ToolKindExecute, reject),
		},
		{
			name: "bypass allows", mode: ports.PermissionModeBypassPermissions,
			req: request(acpsdk.ToolKindExecute, allowOnce), wantID: "allow-once", wantOK: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotID, gotOK := permissionPolicy(test.mode, test.req)
			if string(gotID) != test.wantID || gotOK != test.wantOK {
				t.Fatalf("permissionPolicy = (%q, %v), want (%q, %v)", gotID, gotOK, test.wantID, test.wantOK)
			}
		})
	}
}

func TestValidateTurnSettingsRestrictsBypassToProcessLaunch(t *testing.T) {
	tests := []struct {
		name      string
		initial   ports.PermissionMode
		requested ports.PermissionMode
		wantErr   bool
	}{
		{name: "empty keeps launch mode", initial: ports.PermissionModeDefault},
		{name: "same mode", initial: ports.PermissionModeAuto, requested: ports.PermissionModeAuto},
		{name: "default to accept edits", initial: ports.PermissionModeDefault, requested: ports.PermissionModeAcceptEdits},
		{name: "accept edits to auto", initial: ports.PermissionModeAcceptEdits, requested: ports.PermissionModeAuto},
		{name: "into bypass", initial: ports.PermissionModeDefault, requested: ports.PermissionModeBypassPermissions, wantErr: true},
		{name: "out of bypass", initial: ports.PermissionModeBypassPermissions, requested: ports.PermissionModeAuto, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateTurnSettings(test.initial, ports.ChatTurnSettings{Approval: test.requested})
			if (err != nil) != test.wantErr {
				t.Fatalf("validateTurnSettings error = %v, wantErr %v", err, test.wantErr)
			}
			if test.wantErr && !errors.Is(err, acpdriver.ErrACPSetterUnsupported) {
				t.Fatalf("error = %v, want ErrACPSetterUnsupported", err)
			}
		})
	}
}
