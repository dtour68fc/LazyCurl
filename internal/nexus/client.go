package nexus

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	nexuscurlv1 "github.com/dtour68fc/LazyCurl/internal/nexus/pb/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

// Client is a thin wrapper around the generated gRPC client for one
// NexusCurlServer connection. Dial connects over TLS but with
// InsecureSkipVerify - this is confidentiality-only encryption (keeps
// API keys and Environment variable contents off the wire in
// cleartext), not full server-identity verification, matching
// NexusCurlServer's self-signed-cert-with-no-CA setup; see
// internal/tlsutil in that repo for the tradeoff rationale. API key
// auth is opt-in via SetAPIKey: callers (like the TUI) that never call
// it keep talking to the server exactly as before.
type Client struct {
	conn   *grpc.ClientConn
	rpc    nexuscurlv1.NexusCurlServiceClient
	apiKey string
}

// SetAPIKey configures the API key sent with every subsequent
// Pull/Push/Subscribe call via the "x-api-key" gRPC metadata header. An
// empty key (the default/zero value) sends no header at all, matching
// today's unauthenticated behavior exactly.
func (c *Client) SetAPIKey(key string) {
	c.apiKey = key
}

// withAPIKey attaches the configured API key to ctx's outgoing metadata,
// if one has been set.
func (c *Client) withAPIKey(ctx context.Context) context.Context {
	if c.apiKey == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "x-api-key", c.apiKey)
}

// Dial connects to a NexusCurlServer at "host:port" over TLS (see the
// Client doc comment for why InsecureSkipVerify is used instead of
// verifying a CA chain). Uses grpc.NewClient (lazy dial, same as the
// ad-hoc gRPC request feature) so this returns immediately - actual
// connectivity problems (including a plaintext-only server on the other
// end rejecting the TLS handshake) surface on the first RPC.
func Dial(server string) (*Client, error) {
	creds := credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})
	conn, err := grpc.NewClient(server, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("dial %q: %w", server, err)
	}
	return &Client{conn: conn, rpc: nexuscurlv1.NewNexusCurlServiceClient(conn)}, nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}

// ListProjects returns the name of every project this server currently
// has data for - used when adding a workspace, so the user can pick a
// project instead of typing its name blind. A brand-new project with
// nothing pushed yet won't appear here; callers should still allow
// typing a new name alongside whatever this returns.
func (c *Client) ListProjects(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := c.rpc.ListProjects(ctx, &nexuscurlv1.ListProjectsRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetProjects(), nil
}

// IssueAPIKey asks the server to generate a brand-new API key scoped to
// project, activating it server-side immediately - no SetAPIKey call
// needed afterward to use the returned value elsewhere (e.g. writing it
// into a .mcp.json). Deliberately not gated by any existing key itself,
// same as ListProjects - see the RPC's doc comment in the .proto.
func (c *Client) IssueAPIKey(ctx context.Context, project, owner string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := c.rpc.IssueAPIKey(ctx, &nexuscurlv1.IssueAPIKeyRequest{Project: project, Owner: owner})
	if err != nil {
		return "", err
	}
	return resp.GetKey(), nil
}

// Pull fetches the current snapshot of every Resource in a project.
func (c *Client) Pull(ctx context.Context, project string) ([]*nexuscurlv1.Resource, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ctx = c.withAPIKey(ctx)
	resp, err := c.rpc.Pull(ctx, &nexuscurlv1.PullRequest{Project: project})
	if err != nil {
		return nil, err
	}
	return resp.GetResources(), nil
}

// Push publishes a new revision of one Resource, returning the
// server-assigned copy (revision/timestamp filled in).
func (c *Client) Push(ctx context.Context, project string, res *nexuscurlv1.Resource) (*nexuscurlv1.Resource, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ctx = c.withAPIKey(ctx)
	resp, err := c.rpc.Push(ctx, &nexuscurlv1.PushRequest{Project: project, Resource: res})
	if err != nil {
		return nil, err
	}
	return resp.GetResource(), nil
}

// Subscribe opens the long-lived change-notification stream and forwards
// everything it receives onto the returned channel, which is closed when
// the stream ends (context canceled, or the connection drops) - the
// caller decides whether/how to reconnect (see WatchWithReconnect for
// the "auto-retry, give up and fall back to local after repeated
// failures" policy this app actually wants).
func (c *Client) Subscribe(ctx context.Context, project string) (<-chan *nexuscurlv1.ChangeNotification, error) {
	ctx = c.withAPIKey(ctx)
	stream, err := c.rpc.Subscribe(ctx, &nexuscurlv1.SubscribeRequest{Project: project})
	if err != nil {
		return nil, err
	}

	out := make(chan *nexuscurlv1.ChangeNotification)
	go func() {
		defer close(out)
		for {
			notif, err := stream.Recv()
			if err != nil {
				return
			}
			select {
			case out <- notif:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}
