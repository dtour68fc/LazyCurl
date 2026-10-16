package mcpserver

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dtour68fc/LazyCurl/internal/api"
	"github.com/dtour68fc/LazyCurl/internal/nexus"
	"github.com/mark3labs/mcp-go/mcp"
)

// nexusCurlServerRepoRoot locates the sibling NexusCurlServer checkout
// this LazyCurl checkout develops alongside. Cross-module package import
// of its internal/server and internal/store packages is blocked by Go's
// internal-visibility rule even with a go.mod replace directive (the
// import path doesn't share NexusCurlServer's module prefix), so this
// test instead builds and runs the real nexuscurlserver binary as a
// subprocess - still exercising the actual server, just over the wire
// instead of in-process.
const nexusCurlServerRepoRoot = "/home/davis/GitHub/extras/NexusCurlServer"

// startRealNexusCurlServer builds the real nexuscurlserver binary from
// the sibling repo, runs it on an ephemeral localhost port with no API
// keys configured (today's open/back-compat behavior), and returns a
// *nexus.Client dialed to it once it's confirmed ready.
func startRealNexusCurlServer(t *testing.T) *nexus.Client {
	t.Helper()

	binPath := filepath.Join(t.TempDir(), "nexuscurlserver")
	buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/nexuscurlserver")
	buildCmd.Dir = nexusCurlServerRepoRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("building nexuscurlserver from %s: %v\n%s", nexusCurlServerRepoRoot, err, out)
	}

	// Reserve a free port, then release it immediately before handing it
	// to the subprocess - small race window, acceptable for a test.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	addr := lis.Addr().String()
	if err := lis.Close(); err != nil {
		t.Fatalf("closing probe listener: %v", err)
	}

	dataDir := t.TempDir()
	cmd := exec.Command(binPath,
		"--addr", addr,
		"--data", dataDir,
		"--apikeys", filepath.Join(dataDir, "apikeys.yaml"),
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting nexuscurlserver: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	client, err := nexus.Dial(addr)
	if err != nil {
		t.Fatalf("nexus.Dial() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, lastErr = client.ListProjects(ctx)
		cancel()
		if lastErr == nil {
			return client
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("nexuscurlserver never became ready: %v", lastErr)
	return client
}

func mustCallTool(t *testing.T, s *Server, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	var handler func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)
	switch name {
	case "create_collection":
		handler = s.handleCreateCollection
	case "create_folder":
		handler = s.handleCreateFolder
	case "create_request":
		handler = s.handleCreateRequest
	case "get_collection":
		handler = s.handleGetCollection
	case "update_request":
		handler = s.handleUpdateRequest
	case "delete_request":
		handler = s.handleDeleteRequest
	case "bulk_create":
		handler = s.handleBulkCreate
	case "list_collections":
		handler = s.handleListCollections
	case "create_environment":
		handler = s.handleCreateEnvironment
	case "set_environment_variables":
		handler = s.handleSetEnvironmentVariables
	case "delete_environment_variable":
		handler = s.handleDeleteEnvironmentVariable
	case "import_postman_collection":
		handler = s.handleImportPostmanCollection
	default:
		t.Fatalf("unknown tool %q in test helper", name)
	}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = args

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("tool %q transport error: %v", name, err)
	}
	if result != nil && result.IsError {
		t.Fatalf("tool %q returned error result: %v", name, textOf(result))
	}
	return result
}

func textOf(result *mcp.CallToolResult) string {
	for _, c := range result.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

func decodeResult[T any](t *testing.T, result *mcp.CallToolResult) T {
	t.Helper()
	var out T
	if err := json.Unmarshal([]byte(textOf(result)), &out); err != nil {
		t.Fatalf("decoding tool result: %v\nraw: %s", err, textOf(result))
	}
	return out
}

func TestMCPServerEndToEnd(t *testing.T) {
	client := startRealNexusCurlServer(t)
	s := New(client, "TESTPROJ")
	ctx := context.Background()

	// create_collection
	mustCallTool(t, s, "create_collection", map[string]any{
		"name":        "My API",
		"description": "test collection",
	})
	if _, _, err := s.pullCollection(ctx, "My API"); err != nil {
		t.Fatalf("collection not found after create: %v", err)
	}

	// create_folder, nested path with auto-created intermediate folder
	mustCallTool(t, s, "create_folder", map[string]any{
		"collection": "My API",
		"path":       []any{"Users"},
		"name":       "Admin",
		"description": "admin-only endpoints",
	})

	// create_request under the nested folder
	createResult := mustCallTool(t, s, "create_request", map[string]any{
		"collection": "My API",
		"path":       []any{"Users", "Admin"},
		"request": map[string]any{
			"name":   "List Admins",
			"method": "GET",
			"url":    "https://example.com/admins",
		},
	})
	createdID := decodeResult[struct {
		Success bool   `json:"success"`
		ID      string `json:"id"`
	}](t, createResult).ID
	if createdID == "" {
		t.Fatal("create_request did not return an id")
	}

	// get_collection - verify structure
	getResult := mustCallTool(t, s, "get_collection", map[string]any{"name": "My API"})
	var col api.CollectionFile
	if err := json.Unmarshal([]byte(textOf(getResult)), &col); err != nil {
		t.Fatalf("decoding get_collection result: %v", err)
	}
	if len(col.Folders) != 1 || col.Folders[0].Name != "Users" {
		t.Fatalf("expected top-level Users folder, got %+v", col.Folders)
	}
	if len(col.Folders[0].Folders) != 1 || col.Folders[0].Folders[0].Name != "Admin" {
		t.Fatalf("expected nested Admin folder, got %+v", col.Folders[0].Folders)
	}
	if len(col.Folders[0].Folders[0].Requests) != 1 || col.Folders[0].Folders[0].Requests[0].ID != createdID {
		t.Fatalf("expected the created request nested under Users/Admin, got %+v", col.Folders[0].Folders[0].Requests)
	}

	// update_request
	mustCallTool(t, s, "update_request", map[string]any{
		"collection": "My API",
		"request_id": createdID,
		"request": map[string]any{
			"name":   "List All Admins",
			"method": "GET",
			"url":    "https://example.com/admins?all=true",
		},
	})
	_, _, err := s.pullCollection(ctx, "My API")
	if err != nil {
		t.Fatalf("pullCollection after update: %v", err)
	}
	col2, _, _ := s.pullCollection(ctx, "My API")
	updated, found := findRequestByID(col2, createdID)
	if !found || updated.Name != "List All Admins" {
		t.Fatalf("update_request did not persist, got %+v found=%v", updated, found)
	}
	if updated.ID != createdID {
		t.Fatalf("update_request changed the ID: got %q want %q", updated.ID, createdID)
	}

	// bulk_create - several folders+requests in one call; verify ONE push
	beforePull, err := client.Pull(ctx, "TESTPROJ")
	if err != nil {
		t.Fatalf("Pull() before bulk_create: %v", err)
	}
	var beforeRevision int64
	for _, r := range beforePull {
		if r.GetName() == "My API" {
			beforeRevision = r.GetRevision()
		}
	}

	bulkResult := mustCallTool(t, s, "bulk_create", map[string]any{
		"collection": "My API",
		"items": []any{
			map[string]any{
				"path": []any{},
				"type": "folder",
				"folder": map[string]any{
					"name":        "Orders",
					"description": "order endpoints",
				},
			},
			map[string]any{
				"path": []any{"Orders"},
				"type": "request",
				"request": map[string]any{
					"name":   "List Orders",
					"method": "GET",
					"url":    "https://example.com/orders",
				},
			},
			map[string]any{
				"path": []any{"Orders"},
				"type": "request",
				"request": map[string]any{
					"name":   "Create Order",
					"method": "POST",
					"url":    "https://example.com/orders",
				},
			},
		},
	})
	bulkSummary := decodeResult[struct {
		FoldersCreated  int      `json:"folders_created"`
		RequestsCreated int      `json:"requests_created"`
		Errors          []string `json:"errors"`
	}](t, bulkResult)
	if bulkSummary.FoldersCreated != 1 || bulkSummary.RequestsCreated != 2 {
		t.Fatalf("bulk_create summary = %+v, want 1 folder + 2 requests", bulkSummary)
	}
	if len(bulkSummary.Errors) != 0 {
		t.Fatalf("bulk_create unexpected errors: %v", bulkSummary.Errors)
	}

	afterPull, err := client.Pull(ctx, "TESTPROJ")
	if err != nil {
		t.Fatalf("Pull() after bulk_create: %v", err)
	}
	var afterRevision int64
	for _, r := range afterPull {
		if r.GetName() == "My API" {
			afterRevision = r.GetRevision()
		}
	}
	if afterRevision != beforeRevision+1 {
		t.Fatalf("bulk_create did not result in exactly ONE push: revision went %d -> %d, want +1", beforeRevision, afterRevision)
	}

	col3, _, err := s.pullCollection(ctx, "My API")
	if err != nil {
		t.Fatalf("pullCollection after bulk_create: %v", err)
	}
	foundOrders := false
	for _, f := range col3.Folders {
		if f.Name == "Orders" {
			foundOrders = true
			if len(f.Requests) != 2 {
				t.Fatalf("Orders folder has %d requests, want 2", len(f.Requests))
			}
		}
	}
	if !foundOrders {
		t.Fatalf("Orders folder not found after bulk_create, folders=%+v", col3.Folders)
	}

	// list_collections
	listResult := mustCallTool(t, s, "list_collections", nil)
	var summaries []collectionSummary
	if err := json.Unmarshal([]byte(textOf(listResult)), &summaries); err != nil {
		t.Fatalf("decoding list_collections: %v", err)
	}
	if len(summaries) != 1 || summaries[0].Name != "My API" {
		t.Fatalf("list_collections = %+v, want exactly [My API]", summaries)
	}
	if summaries[0].RequestCount != 3 { // List All Admins + List Orders + Create Order
		t.Fatalf("list_collections request_count = %d, want 3", summaries[0].RequestCount)
	}

	// create_environment
	mustCallTool(t, s, "create_environment", map[string]any{
		"name": "Staging",
		"variables": map[string]any{
			"base_url": map[string]any{"value": "https://staging.example.com"},
		},
	})

	// set_environment_variables twice with different keys - verify merge
	mustCallTool(t, s, "set_environment_variables", map[string]any{
		"name": "Staging",
		"variables": map[string]any{
			"api_token": map[string]any{"value": "secret-token", "secret": true},
		},
	})
	mustCallTool(t, s, "set_environment_variables", map[string]any{
		"name": "Staging",
		"variables": map[string]any{
			"timeout_ms": map[string]any{"value": "5000"},
		},
	})

	env, err := s.pullEnvironment(ctx, "Staging")
	if err != nil {
		t.Fatalf("pullEnvironment: %v", err)
	}
	for _, key := range []string{"base_url", "api_token", "timeout_ms"} {
		if _, ok := env.Variables[key]; !ok {
			t.Fatalf("expected variable %q to survive merge, got variables=%+v", key, env.Variables)
		}
	}
	if !env.Variables["api_token"].Secret {
		t.Fatal("api_token variable lost its secret flag")
	}

	// delete_environment_variable
	mustCallTool(t, s, "delete_environment_variable", map[string]any{
		"name": "Staging",
		"key":  "timeout_ms",
	})
	env2, err := s.pullEnvironment(ctx, "Staging")
	if err != nil {
		t.Fatalf("pullEnvironment after delete: %v", err)
	}
	if _, ok := env2.Variables["timeout_ms"]; ok {
		t.Fatal("timeout_ms should have been deleted")
	}
	if _, ok := env2.Variables["base_url"]; !ok {
		t.Fatal("base_url should have survived the single-key delete")
	}

	// import_postman_collection
	fixturePath := filepath.Join("..", "import", "postman", "testdata", "simple_collection.json")
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("reading postman fixture: %v", err)
	}
	importResult := mustCallTool(t, s, "import_postman_collection", map[string]any{
		"raw_json":        string(raw),
		"collection_name": "Imported From Postman",
	})
	importSummary := decodeResult[struct {
		Success  bool     `json:"success"`
		Name     string   `json:"name"`
		Warnings []string `json:"warnings"`
	}](t, importResult)
	if importSummary.Name != "Imported From Postman" {
		t.Fatalf("import_postman_collection name = %q, want override to apply", importSummary.Name)
	}

	importedCol, _, err := s.pullCollection(ctx, "Imported From Postman")
	if err != nil {
		t.Fatalf("pullCollection for imported collection: %v", err)
	}
	requestNames := map[string]bool{}
	for _, r := range importedCol.Requests {
		requestNames[r.Name] = true
	}
	for _, want := range []string{"Get Users", "Create User", "Update User", "Delete User", "Patch User"} {
		if !requestNames[want] {
			t.Fatalf("expected imported request %q, got requests=%+v", want, importedCol.Requests)
		}
	}
	if importedCol.Project != "TESTPROJ" {
		t.Fatalf("imported collection Project = %q, want TESTPROJ", importedCol.Project)
	}
}
