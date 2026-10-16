package nexus

import (
	"context"
	"fmt"
	"time"

	nexuscurlv1 "github.com/dtour68fc/LazyCurl/internal/nexus/pb/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client is a thin wrapper around the generated gRPC client for one
// NexusCurlServer connection - no TLS/auth yet (matching the server's
// current state), that's planned as a later pass on both sides.
type Client struct {
	conn *grpc.ClientConn
	rpc  nexuscurlv1.NexusCurlServiceClient
}

// Dial connects to a NexusCurlServer at "host:port". Uses grpc.NewClient
// (lazy dial, same as the ad-hoc gRPC request feature) so this returns
// immediately - actual connectivity problems surface on the first RPC.
func Dial(server string) (*Client, error) {
	conn, err := grpc.NewClient(server, grpc.WithTransportCredentials(insecure.NewCredentials()))
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

// Pull fetches the current snapshot of every Resource in a project.
func (c *Client) Pull(ctx context.Context, project string) ([]*nexuscurlv1.Resource, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
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
