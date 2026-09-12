package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jhump/protoreflect/dynamic"
	"github.com/jhump/protoreflect/dynamic/grpcdynamic"
	"github.com/jhump/protoreflect/grpcreflect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// grpcInvokeTimeout bounds how long a single unary gRPC call (including
// the reflection lookups needed to find it) is allowed to take, same
// spirit as the HTTP client's timeout. Not a const so tests can shrink it
// (grpc-go's default connection backoff means "nothing listening" can
// otherwise take the full timeout to fail).
var grpcInvokeTimeout = 30 * time.Second

// StripGRPCScheme removes a "grpc://" or "grpcs://" prefix from a server
// address, if present. grpc.NewClient wants a bare "host:port" target, not
// a URL - "grpc://" isn't a registered resolver scheme, so dialing one
// verbatim fails with a confusing "unsupported protocol scheme" error
// that actually comes from net/http-shaped code elsewhere, not gRPC at
// all. The Server field is stored with the scheme (it reads better in the
// UI, and is what most reference examples show), so this needs to run
// right before dialing.
func StripGRPCScheme(server string) string {
	server = strings.TrimSpace(server)
	server = strings.TrimPrefix(server, "grpcs://")
	server = strings.TrimPrefix(server, "grpc://")
	return server
}

// InvokeGRPC dials cfg.Server, uses server reflection to discover
// cfg.Service/cfg.Method's descriptors (no pre-compiled protobuf stubs
// needed - same approach grpcurl/Postman/Insomnia use), builds the
// request message from cfg.Message (JSON matching the method's input
// type), sends it, and returns the response wrapped in the same
// *Response shape HTTP responses use so the existing Response panel needs
// no changes to display it. Only unary calls are supported - streaming
// methods are rejected with a clear error rather than silently hanging.
func InvokeGRPC(cfg *GRPCConfig) (*Response, error) {
	if cfg == nil {
		return nil, fmt.Errorf("no gRPC config for this request")
	}
	server := StripGRPCScheme(cfg.Server)
	if server == "" {
		return nil, fmt.Errorf("no server address set (Server tab)")
	}
	if cfg.Service == "" {
		return nil, fmt.Errorf("no service set (Server tab) - e.g. \"myapp.v1.UserService\"")
	}
	if cfg.Method == "" {
		return nil, fmt.Errorf("no method set (Server tab) - e.g. \"GetUser\"")
	}

	start := time.Now()

	creds, err := grpcTransportCredentials(cfg.TLS)
	if err != nil {
		return nil, fmt.Errorf("TLS config: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), grpcInvokeTimeout)
	defer cancel()

	// Attach outgoing metadata (e.g. Authorization) before any RPC is made -
	// including the reflection lookup below. Servers that gate all streams
	// (reflection included) behind auth interceptors will otherwise reject
	// or even panic on the reflection call since it'd carry no credentials,
	// even though the caller did supply them for the "real" invoke.
	if len(cfg.Metadata) > 0 {
		outMD := metadata.MD{}
		for _, kv := range cfg.Metadata {
			if kv.Enabled && kv.Key != "" {
				outMD.Append(kv.Key, kv.Value)
			}
		}
		if len(outMD) > 0 {
			ctx = metadata.NewOutgoingContext(ctx, outMD)
		}
	}

	conn, err := grpc.NewClient(server, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("dial %q: %w", server, err)
	}
	defer conn.Close()

	reflClient := grpcreflect.NewClientAuto(ctx, conn)
	defer reflClient.Reset()

	fd, err := reflClient.FileContainingSymbol(cfg.Service)
	if err != nil {
		return nil, fmt.Errorf("reflection lookup for service %q on %q: %w (does the server have reflection enabled?)", cfg.Service, server, err)
	}
	sd := fd.FindService(cfg.Service)
	if sd == nil {
		return nil, fmt.Errorf("service %q not found in the file reflection returned - check the fully-qualified name (package + service)", cfg.Service)
	}
	md := sd.FindMethodByName(cfg.Method)
	if md == nil {
		return nil, fmt.Errorf("method %q not found on service %q", cfg.Method, cfg.Service)
	}
	if md.IsClientStreaming() || md.IsServerStreaming() {
		return nil, fmt.Errorf("%q is a streaming method - only unary calls are supported so far", cfg.Method)
	}

	reqMsg := dynamic.NewMessage(md.GetInputType())
	message := strings.TrimSpace(cfg.Message)
	if message == "" {
		message = "{}"
	}
	if err := reqMsg.UnmarshalJSON([]byte(message)); err != nil {
		return nil, fmt.Errorf("request message doesn't match %s: %w", md.GetInputType().GetFullyQualifiedName(), err)
	}

	stub := grpcdynamic.NewStub(conn)
	var headerMD metadata.MD
	respMsg, callErr := stub.InvokeRpc(ctx, md, reqMsg, grpc.Header(&headerMD))
	elapsed := time.Since(start)

	if callErr != nil {
		st, _ := status.FromError(callErr)
		return &Response{
			StatusCode: int(st.Code()),
			Status:     st.Code().String(),
			Headers:    grpcMDToHeaders(headerMD),
			Body:       st.Message(),
			Time:       elapsed,
			Size:       int64(len(st.Message())),
		}, callErr
	}

	dynResp, err := dynamic.AsDynamicMessage(respMsg)
	if err != nil {
		return nil, fmt.Errorf("converting response message: %w", err)
	}
	bodyBytes, err := dynResp.MarshalJSONIndent()
	if err != nil {
		return nil, fmt.Errorf("marshaling response to JSON: %w", err)
	}

	return &Response{
		StatusCode: 0, // gRPC codes.OK
		Status:     "OK",
		Headers:    grpcMDToHeaders(headerMD),
		Body:       string(bodyBytes),
		Time:       elapsed,
		Size:       int64(len(bodyBytes)),
	}, nil
}

// grpcMDToHeaders converts gRPC response metadata into the same
// map[string][]string shape api.Response.Headers already uses for HTTP,
// so the Response panel's Headers tab works unmodified for gRPC too.
func grpcMDToHeaders(md metadata.MD) map[string][]string {
	out := make(map[string][]string, len(md))
	for k, v := range md {
		out[k] = v
	}
	return out
}

// grpcTransportCredentials builds TLS transport credentials from a
// GRPCTLSConfig - plaintext (insecure.NewCredentials()) if TLS isn't
// enabled at all, otherwise a real tls.Config covering server-only TLS
// (just CAFile/ServerName), mTLS (CertFile+KeyFile as the client
// identity), and the insecure-skip-verify dev escape hatch.
func grpcTransportCredentials(tlsCfg *GRPCTLSConfig) (credentials.TransportCredentials, error) {
	if tlsCfg == nil || !tlsCfg.Enabled {
		return insecure.NewCredentials(), nil
	}

	conf := &tls.Config{
		ServerName:         tlsCfg.ServerName,
		InsecureSkipVerify: tlsCfg.InsecureSkipVerify,
	}

	if tlsCfg.CAFile != "" {
		caBytes, err := os.ReadFile(tlsCfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("reading CA file %q: %w", tlsCfg.CAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caBytes) {
			return nil, fmt.Errorf("no certificates found in CA file %q", tlsCfg.CAFile)
		}
		conf.RootCAs = pool
	}

	if tlsCfg.CertFile != "" || tlsCfg.KeyFile != "" {
		if tlsCfg.CertFile == "" || tlsCfg.KeyFile == "" {
			return nil, fmt.Errorf("mTLS needs both cert_file and key_file set - only one was provided")
		}
		cert, err := tls.LoadX509KeyPair(tlsCfg.CertFile, tlsCfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("loading client cert/key: %w", err)
		}
		conf.Certificates = []tls.Certificate{cert}
	}

	return credentials.NewTLS(conf), nil
}
