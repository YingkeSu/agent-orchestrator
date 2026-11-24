# DeepSeek Harness (DSH)

AO supports DeepSeek Harness in Chat mode through DSH's automation-only ACP
stdio server:

```bash
dsh --profile acp
```

AO launches the user's own `dsh` binary; it never bundles, downloads, or
substitutes DSH. The DSH terminal UI is not integrated, so a DSH session is
Chat-only: AO refuses to build a TUI launch command, and there is no
TUI-to-Chat or Chat-to-TUI handoff.

## Install

Install DSH with its supported distribution and make sure the `dsh` executable
is available:

```bash
npm install -g @deepseek-ai/dsh
dsh --version
```

AO's tested minimum is `0.2.1-rc.2`. Newer releases, including newer
prereleases, are accepted. An older build is rejected before an ACP process
starts.

AO resolves `dsh` from:

- `PATH`
- `/usr/local/bin/dsh` and `/opt/homebrew/bin/dsh`
- `/home/syk/.local/bin/dsh` (the lab's user-level install) and `~/.local/bin/dsh`
- npm global bin directories (`~/.npm-global/bin`, `~/.npm/bin`, `%APPDATA%\npm`)
- Node version manager global bins (Volta, nvm, fnm)

AO never invokes `npm`, `npx`, `pnpm`, or a network installer as a fallback.

The `acp` profile initializes from DSH's shipped template on first use under
`~/.dsh/profiles/acp`. AO passes `--profile acp` on every launch and does not
modify the user's profile.

## Model and provider configuration

DSH owns provider routing and credentials. AO never copies, stores, or probes
them.

The ACP server advertises a live `model` configuration option; AO renders those
provider-owned choices in Chat and sends the selected opaque value back with
`session/set_config_option`. When the selected model offers reasoning levels,
DSH also advertises `reasoning_effort`, which AO forwards as the per-turn
reasoning choice.

The `acp` profile's own default is the `acp` row's `provider` and `model`
fields. To change the default for DSH sessions on this machine, edit the user
patch layer at `~/.dsh/profiles/acp/cordis.patch.yml`:

```yaml
- id: acp
  config:
    provider: deepseek-official
    model: deepseek-v4-pro
```

Inspect the composed profile without starting a process:

```bash
dsh --profile acp --dump-config
```

Provider credentials stay in DSH's own configuration. The DeepSeek API-key
route reads `DEEPSEEK_API_KEY` from the process environment, which AO passes
through unchanged; account-backed routes use the account state DSH manages.

## System prompt behavior

AO passes its standing instructions through a per-session patch file under
AO's data directory, for example:

```text
$AO_DATA_DIR/prompts/<session-id>/dsh/ao.patch.yml
```

The file overrides the ACP profile's `system-prompt` row:

```yaml
- id: system-prompt
  config:
    personaPrefix: |
      <AO standing instructions for this session>
```

DSH composes patch layers in order — bundle layers, the profile patch, the home
patch, then `--patch` overlays — so AO's session instructions apply only to
that process and never change the user's `~/.dsh` files. `personaPrefix`
precedes first-party guidance, which keeps DSH's own coding-agent instructions
and tool schemas intact.

Project `AGENTS.md` and DSH's other instruction resources are still loaded by
DSH's `agent-instructions` plugin. AO adds its generated standing prompt; it
does not replace project guidance. A resumed Chat session starts a new DSH
process and receives the recomputed instructions again.

## Permissions and approvals

DSH ACP supports `session/request_permission`, so AO can present approvals.

- `default` leaves every permission request for the user. DSH's
  `workspace-write` sandbox stays in force.
- `accept-edits` automatically allows file edit, delete, and move requests;
  command execution still asks.
- `auto` automatically allows every offered tool request while keeping the
  `workspace-write` sandbox.
- `bypass-permissions` launches DSH with
  `DSH_PERMISSION_MODE=danger-full-access`, which disables the sandbox and
  approval prompts. Changing into or out of this mode requires restarting Chat
  because DSH reads the preset at process launch.

## Protocol mapping

| Surface | AO behavior |
| --- | --- |
| Text and thinking | ACP message chunks stream into Chat events. |
| Tools | ACP tool-call lifecycle maps to activity events. |
| Approvals | `session/request_permission` is surfaced; the selected permission mode answers only the requests it already authorizes. |
| Images and attachments | DSH's ACP profile reports `image: false` by default, so AO does not claim native image prompts. |
| Load/resume | DSH advertises `session/resume`, so AO can reconnect provider context after a restart. DSH ACP does not implement `session/load`, so AO reports typed history replay as unavailable. |
| Models | The live ACP `model` and `reasoning_effort` options drive AO's per-turn choices. |
| Diffs and plans | DSH ACP does not send diff or plan updates, so AO reports both capabilities as false. |
| MCP | DSH ACP accepts stdio and Streamable HTTP MCP servers. AO passes configured session servers through the standard ACP session request. |

## Auth

AO reports DSH authentication as `unknown`. DSH has no cheap,
non-interactive local status probe, and model credentials or account state can
fail later for reasons AO cannot see. An `unknown` result is not evidence that
DSH is logged out; a Chat probe only verifies the binary and `dsh --version`.

## Troubleshooting

- **`dsh is not installed`** — confirm `dsh --version` works in the same
  environment as the AO daemon. GUI-launched daemons often miss a shell-managed
  `PATH`; install DSH in one of the user-level locations AO searches.
- **`older than AO's tested minimum`** — upgrade DSH to `0.2.1-rc.2` or newer.
- **`unsupported ACP agent`** — the resolved binary is not DSH's ACP server, or
  the profile was replaced. Reinstall DSH and verify
  `dsh --profile acp --dump-config` includes the `@deepseek-ai/dsh-acp` row.
- **Model or credential failure** — inspect DSH directly. Run
  `dsh --profile acp --dump-config`, verify the `acp` row's provider/model, and
  confirm the matching credential or account state. AO does not hold provider
  credentials.
- **Permission mode change refused** — bypass mode is launch-time only. Start a
  new Chat conversation to change into or out of it.
