// Package dsh implements AO's DeepSeek Harness (dsh) agent adapter.
//
// DeepSeek Harness is integrated as a Chat-only harness through its ACP stdio
// server (`dsh --profile acp`). AO does not drive the DSH terminal UI, so this
// adapter deliberately refuses to build a TUI launch command; it exists to give
// the Chat driver a single, shared answer for binary discovery and auth status.
//
// The adapter never downloads, bundles, or substitutes the user's dsh
// installation. Provider credentials, profiles, and model routing remain owned
// by DSH under its own home directory.
package dsh

import (
	"context"
	"errors"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const adapterID = "dsh"

// ErrChatOnly reports that AO does not integrate the DSH terminal UI. DSH can
// only run through its ACP server in Chat mode.
var ErrChatOnly = errors.New("DeepSeek Harness is available in Chat mode only; AO does not integrate the DSH terminal UI")

// Plugin is the DeepSeek Harness agent adapter. It is safe for concurrent use;
// binary resolution is cached after the first successful lookup.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register DeepSeek Harness adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentBinaryResolver = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "DeepSeek Harness",
		Description: "Run DeepSeek Harness Chat sessions through `dsh --profile acp`.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports the per-project agent config keys DSH understands.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	return agentbase.ModelConfigSpec(ctx, "Model override for DeepSeek Harness Chat sessions.")
}

// GetLaunchCommand always fails: DSH's terminal UI is not integrated, so AO must
// not launch it as a TUI harness. Chat sessions launch through the dshacp driver.
func (p *Plugin) GetLaunchCommand(ctx context.Context, _ ports.LaunchConfig) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, ErrChatOnly
}

// GetRestoreCommand reports that this adapter has no native TUI session to
// restore. Chat continuation is owned by the ACP driver.
func (p *Plugin) GetRestoreCommand(ctx context.Context, _ ports.RestoreConfig) ([]string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return nil, false, nil
}

// SessionInfo surfaces metadata captured by AO's generic session machinery.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

// AuthStatus reports unknown because DSH has no cheap, non-interactive local
// auth probe. Model credentials are resolved by DSH itself when a Chat session
// starts; an inconclusive answer here is not evidence that DSH is logged out.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return ports.AgentAuthStatusUnknown, nil
}

var dshBinarySpec = binaryutil.BinarySpec{
	Label:         "dsh",
	Names:         []string{"dsh"},
	WinNames:      []string{"dsh.cmd", "dsh.exe", "dsh"},
	UnixPaths:     []string{"/usr/local/bin/dsh", "/opt/homebrew/bin/dsh", "/home/syk/.local/bin/dsh"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("dsh", []string{".local", "bin", "dsh"}),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "dsh.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "dsh.exe"}},
	},
}

// ResolveDSHBinary finds the `dsh` binary, searching PATH then common install
// locations. It never invokes npm, npx, or any network installer.
func ResolveDSHBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, dshBinarySpec)
}

// ResolveBinary resolves the executable path for the plugin.
func (p *Plugin) ResolveBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}
	binary, err := ResolveDSHBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}
