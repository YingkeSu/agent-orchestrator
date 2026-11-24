// Package dshacp binds the user's own DeepSeek Harness (dsh) installation to
// AO's reusable ACP Chat transport.
//
// DSH exposes an automation-only ACP stdio server as `dsh --profile acp`. AO
// never downloads or bundles dsh. The user's profile, provider credentials,
// model routing, and home patch layer remain DSH-owned; AO adds only a
// per-session patch file for its standing instructions and the launch-time
// permission policy described below.
package dshacp

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"gopkg.in/yaml.v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/dsh"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	// acpAgentName is the ACP implementation dsh-acp reports in initialize.
	acpAgentName = "deepseek-harness-acp"
	// acpAgentNameAlias tolerates a shortened identity from a future dsh-acp
	// build without accepting an unrelated ACP agent.
	acpAgentNameAlias = "dsh-acp"
	// minimumACPVersion is the ACP bridge implementation version AO tested. It
	// is independent of the dsh CLI release, which Probe gates with
	// `dsh --version` and dsh.MinimumVersion.
	minimumACPVersion = "0.0.1"
	// patchFileName is the per-session overlay AO writes under its data dir.
	patchFileName = "ao.patch.yml"
	// permissionModeEnv is the DSH-native permission preset read at process
	// start. The shipped profile maps danger-full-access to no sandbox and no
	// approval prompts; every other preset keeps the workspace-write sandbox
	// and asks for permission through ACP.
	permissionModeEnv      = "DSH_PERMISSION_MODE"
	dangerFullAccessPreset = "danger-full-access"
	versionProbeTimeout    = 5 * time.Second
)

// New launches `dsh --profile acp` from the exact binary resolved by the DSH
// agent plugin. Chat is the only AO mode DSH supports.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return newDriver(plugin, dsh.ProbeVersion, log)
}

func newDriver(plugin nativeacp.Plugin, probe nativeacp.VersionProbe, log *slog.Logger) ports.ChatDriver {
	return acpdriver.New(buildConfig(plugin, probe, log), log)
}

func buildConfig(plugin nativeacp.Plugin, probe nativeacp.VersionProbe, log *slog.Logger) acpdriver.Config {
	return acpdriver.Config{
		Harness: domain.HarnessDSH,
		Capabilities: ports.ChatCapabilities{
			ports.ChatCapabilityStreaming: true,
			ports.ChatCapabilityTools:     true,
			ports.ChatCapabilityApprovals: true,
			ports.ChatCapabilityInterrupt: true,
			ports.ChatCapabilityResume:    true,
			ports.ChatCapabilityUsage:     true,
			// dsh-acp intentionally omits session/load, diff updates, and plan
			// updates. It can resume provider context through session/resume but
			// cannot replay a typed transcript.
			ports.ChatCapabilityHistory: false,
			ports.ChatCapabilityDiffs:   false,
			ports.ChatCapabilityPlans:   false,
		},
		Probe: func(ctx context.Context) error {
			binary, err := plugin.ResolveBinary(ctx)
			if err != nil {
				return fmt.Errorf("%w: dsh is not installed: %w", ports.ErrChatDriverUnavailable, err)
			}
			if probe == nil {
				return nil
			}
			versionCtx, cancel := context.WithTimeout(ctx, versionProbeTimeout)
			defer cancel()
			if err := probe(versionCtx, binary); err != nil {
				return fmt.Errorf("%w: %w", ports.ErrChatDriverIncompatible, err)
			}
			// Deliberately no credential probe: model/provider auth belongs to
			// DSH and is reported only when the ACP session starts.
			return nil
		},
		Launch: func(ctx context.Context, cfg acpdriver.LaunchConfig) (acpdriver.Launch, error) {
			binary, err := plugin.ResolveBinary(ctx)
			if err != nil {
				return acpdriver.Launch{}, fmt.Errorf("%w: dsh is not installed: %w", ports.ErrChatDriverUnavailable, err)
			}
			args, overrides, err := configure(ctx, cfg)
			if err != nil {
				return acpdriver.Launch{}, err
			}
			env := cloneEnv(cfg.Env)
			for key, value := range overrides {
				env[key] = value
			}
			return acpdriver.Launch{Command: binary, Args: args, Env: env}, nil
		},
		ValidateInitialize:   validateInitialize,
		SessionOptions:       sessionOptions,
		PermissionPolicy:     permissionPolicy,
		ValidateTurnSettings: validateTurnSettings,
	}
}

func cloneEnv(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for key, value := range in {
		out[key] = value
	}
	return out
}

// configure builds the launcher arguments and environment for one dsh ACP
// process. AO writes its standing instructions into a per-session patch file
// and passes it with --patch, which DSH composes after the profile and home
// layers. The user's ~/.dsh files are never modified.
func configure(ctx context.Context, cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	args := []string{"--profile", "acp"}
	var env map[string]string
	if ports.NormalizePermissionMode(cfg.Permissions) == ports.PermissionModeBypassPermissions {
		env = map[string]string{permissionModeEnv: dangerFullAccessPreset}
	}
	if prompt := strings.TrimSpace(cfg.SystemPrompt); prompt != "" {
		patchPath, err := writeStandingPatch(ctx, cfg, prompt)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, "--patch", patchPath)
	}
	return args, env, nil
}

// instructionPatchPath is the AO-owned per-session overlay path.
func instructionPatchPath(cfg acpdriver.LaunchConfig) string {
	return filepath.Join(cfg.DataDir, "prompts", string(cfg.SessionID), "dsh", patchFileName)
}

type patchEntry struct {
	ID     string      `yaml:"id"`
	Config patchConfig `yaml:"config"`
}

type patchConfig struct {
	// PersonaPrefix is the dsh-system-prompt configuration field for the
	// deployment persona that precedes first-party guidance.
	PersonaPrefix string `yaml:"personaPrefix"`
}

func writeStandingPatch(ctx context.Context, cfg acpdriver.LaunchConfig, prompt string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !filepath.IsAbs(cfg.DataDir) {
		return "", fmt.Errorf("dsh ACP requires an absolute AO data directory, got %q", cfg.DataDir)
	}
	path := instructionPatchPath(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("prepare dsh standing-instruction patch: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	content, err := yaml.Marshal([]patchEntry{{
		ID:     "system-prompt",
		Config: patchConfig{PersonaPrefix: prompt},
	}})
	if err != nil {
		return "", fmt.Errorf("encode dsh standing-instruction patch: %w", err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return "", fmt.Errorf("write dsh standing-instruction patch: %w", err)
	}
	return path, nil
}

func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	options := make([]acpdriver.SessionOption, 0, 2)
	if model := strings.TrimSpace(settings.Model); model != "" {
		options = append(options, acpdriver.SessionOption{ID: "model", Value: model})
	}
	if effort := strings.TrimSpace(settings.Effort); effort != "" {
		options = append(options, acpdriver.SessionOption{ID: "reasoning_effort", Value: effort})
	}
	if len(options) == 0 {
		return nil
	}
	return options
}

// permissionPolicy answers only the requests AO's selected permission mode
// already authorizes. Default leaves every request for a human, accept-edits
// auto-approves file mutations, and auto/bypass approve every offered tool.
// DSH's workspace-write sandbox remains in force unless the process was
// launched in bypass-permissions mode.
func permissionPolicy(mode ports.PermissionMode, params acpsdk.RequestPermissionRequest) (acpsdk.PermissionOptionId, bool) {
	switch ports.NormalizePermissionMode(mode) {
	case ports.PermissionModeAcceptEdits:
		kind := acpsdk.ToolKind("")
		if params.ToolCall.Kind != nil {
			kind = *params.ToolCall.Kind
		}
		switch kind {
		case acpsdk.ToolKindEdit, acpsdk.ToolKindDelete, acpsdk.ToolKindMove:
			return permissionOption(params.Options, acpsdk.PermissionOptionKindAllowOnce)
		}
	case ports.PermissionModeAuto, ports.PermissionModeBypassPermissions:
		if id, ok := permissionOption(params.Options, acpsdk.PermissionOptionKindAllowAlways); ok {
			return id, true
		}
		return permissionOption(params.Options, acpsdk.PermissionOptionKindAllowOnce)
	}
	return "", false
}

func permissionOption(options []acpsdk.PermissionOption, kind acpsdk.PermissionOptionKind) (acpsdk.PermissionOptionId, bool) {
	for _, option := range options {
		if option.Kind == kind {
			return option.OptionId, true
		}
	}
	return "", false
}

// validateTurnSettings rejects a live change that would require a different
// DSH process. The sandbox/approval preset is read at launch, so bypass mode
// can only be entered or left by restarting Chat; the other modes adapt
// per request through permissionPolicy.
func validateTurnSettings(initial ports.PermissionMode, settings ports.ChatTurnSettings) error {
	if settings.Approval == "" {
		return nil
	}
	initial = ports.NormalizePermissionMode(initial)
	requested := ports.NormalizePermissionMode(settings.Approval)
	if initial == requested {
		return nil
	}
	if initial == ports.PermissionModeBypassPermissions || requested == ports.PermissionModeBypassPermissions {
		return fmt.Errorf("%w: DSH ACP permission mode is fixed at process launch (%s); restart Chat to change it to %s",
			acpdriver.ErrACPSetterUnsupported, initial, requested)
	}
	return nil
}

// validateInitialize rejects an ACP peer that identifies itself as something
// other than dsh-acp, or as a bridge older than AO tested. A peer that sends no
// agent identity is accepted because the ACP handshake carries no other
// distribution assertion to check.
func validateInitialize(init acpsdk.InitializeResponse) error {
	info := init.AgentInfo
	if info == nil {
		return nil
	}
	name := strings.TrimSpace(info.Name)
	if name == "" {
		return nil
	}
	if name != acpAgentName && name != acpAgentNameAlias {
		return fmt.Errorf("unsupported ACP agent %q; install DeepSeek Harness and run `dsh --profile acp`", name)
	}
	installed, ok := dsh.ParseVersion(info.Version)
	if !ok {
		return fmt.Errorf("unrecognized %s ACP version %q", name, info.Version)
	}
	minimum, _ := dsh.ParseVersion(minimumACPVersion)
	if installed.Less(minimum) {
		return fmt.Errorf("%s ACP implementation %s is older than AO's tested minimum %s",
			name, info.Version, minimumACPVersion)
	}
	return nil
}
