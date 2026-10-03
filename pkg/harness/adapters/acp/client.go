package acp

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	sdk "github.com/coder/acp-go-sdk"
)

// errUnsupported is returned for every client method Ploeg deliberately does
// not implement. The capabilities we advertise on initialize already tell a
// conforming agent not to call them; this is the backstop for one that does.
var errUnsupported = errors.New("acp: method not supported by this client")

// client is Ploeg's half of the ACP connection.
//
// Deliberately small. Only session/request_permission does real work here;
// session/update is a no-op because state is folded off the raw wire instead
// (see below). Everything else is refused:
//
//   - fs/read_text_file, fs/write_text_file: ACP's filesystem methods exist so
//     an EDITOR can serve unsaved buffer contents. Ploeg is not an editor: the
//     agent's cwd is a real clone in the same container and every agent has
//     native file tools. Implementing them would buy nothing and cost a path
//     jail plus a real hazard — pkg/worker embeds AGENT_BUILDER_TOKEN in the
//     clone URL, so it lands in .git/config, and an fs/read of that file would
//     hand the forge token to the model provider through a protocol-blessed
//     path.
//   - terminal/*: the tempting argument is "make the agent run the gates
//     through us so we see exit codes". It does not hold — when the agent uses
//     its own shell we already receive the command, its status and its output
//     as tool_call/tool_call_update, at the same fidelity, for free.
//   - elicitation/*: an agent that needs human input mid-run IS the stuck →
//     needs_human state. Interactivity belongs above Ploeg (backlog #101),
//     never inside a one-shot pod.
//
// CONCURRENCY: every method here runs on the SDK's protocol read loop. They may
// take the state mutex and send non-blocking on a buffered channel. They must
// never do HTTP, disk I/O or a blocking send — that wedges the dispatcher and
// the whole session stops.
type client struct {
	state     *sessionState
	perms     *PermissionPolicy
	log       *slog.Logger
	storm     chan struct{} // closed once, when the permission caps trip
	stormOnce sync.Once
}

func newClient(s *sessionState, p *PermissionPolicy, log *slog.Logger) *client {
	return &client{state: s, perms: p, log: log, storm: make(chan struct{})}
}

// SessionUpdate deliberately does nothing — see the body.
func (c *client) SessionUpdate(context.Context, sdk.SessionNotification) error {
	// Intentionally a no-op. State is folded from the RAW protocol line by the
	// launcher's tap, BEFORE the SDK parses it, because the SDK's generated
	// union silently drops variants its schema version predates — the ACP v1
	// usage shape among them. Accumulating here too would double-count every
	// event and still miss exactly the fields we care about.
	return nil
}

// RequestPermission answers immediately and deterministically. There is no
// human in a worker pod, so the only wrong answers are "block" and "prompt".
func (c *client) RequestPermission(_ context.Context, params sdk.RequestPermissionRequest) (sdk.RequestPermissionResponse, error) {
	req := PermissionRequest{Options: make([]PermissionOption, 0, len(params.Options))}
	if k := params.ToolCall.Kind; k != nil {
		req.ToolKind = ParseToolKind(string(*k))
	}
	if t := params.ToolCall.Title; t != nil {
		req.Title = *t
	}
	for _, o := range params.Options {
		req.Options = append(req.Options, PermissionOption{
			ID: string(o.OptionId), Name: o.Name, Kind: string(o.Kind),
		})
	}

	d := c.perms.Decide(req)
	switch {
	case d.Storm:
		c.stormOnce.Do(func() { close(c.storm) })
		total, top := c.perms.Stats()
		c.log.Warn("permission storm; cancelling the session",
			"requests", total, "topTools", top)
		return cancelledPermission(), nil
	case d.OptionID == "":
		// Nothing acceptable was offered. Answering "cancelled" is honest;
		// guessing at an option could silently grant a mutation.
		c.log.Warn("no acceptable permission option offered", "tool", req.Title, "kind", req.ToolKind)
		return cancelledPermission(), nil
	default:
		return sdk.RequestPermissionResponse{
			Outcome: sdk.RequestPermissionOutcome{
				Selected: &sdk.RequestPermissionOutcomeSelected{
					OptionId: sdk.PermissionOptionId(d.OptionID),
				},
			},
		}, nil
	}
}

func cancelledPermission() sdk.RequestPermissionResponse {
	return sdk.RequestPermissionResponse{
		Outcome: sdk.RequestPermissionOutcome{
			Cancelled: &sdk.RequestPermissionOutcomeCancelled{},
		},
	}
}

// stormed reports the channel that closes when the permission caps trip.
func (c *client) stormed() <-chan struct{} { return c.storm }

// --- refused methods -------------------------------------------------------

func (c *client) ReadTextFile(context.Context, sdk.ReadTextFileRequest) (sdk.ReadTextFileResponse, error) {
	return sdk.ReadTextFileResponse{}, errUnsupported
}

func (c *client) WriteTextFile(context.Context, sdk.WriteTextFileRequest) (sdk.WriteTextFileResponse, error) {
	return sdk.WriteTextFileResponse{}, errUnsupported
}

func (c *client) CreateTerminal(context.Context, sdk.CreateTerminalRequest) (sdk.CreateTerminalResponse, error) {
	return sdk.CreateTerminalResponse{}, errUnsupported
}

func (c *client) KillTerminal(context.Context, sdk.KillTerminalRequest) (sdk.KillTerminalResponse, error) {
	return sdk.KillTerminalResponse{}, errUnsupported
}

func (c *client) TerminalOutput(context.Context, sdk.TerminalOutputRequest) (sdk.TerminalOutputResponse, error) {
	return sdk.TerminalOutputResponse{}, errUnsupported
}

func (c *client) ReleaseTerminal(context.Context, sdk.ReleaseTerminalRequest) (sdk.ReleaseTerminalResponse, error) {
	return sdk.ReleaseTerminalResponse{}, errUnsupported
}

func (c *client) WaitForTerminalExit(context.Context, sdk.WaitForTerminalExitRequest) (sdk.WaitForTerminalExitResponse, error) {
	return sdk.WaitForTerminalExitResponse{}, errUnsupported
}

// compile-time proof we satisfy the SDK's client contract
var _ sdk.Client = (*client)(nil)
