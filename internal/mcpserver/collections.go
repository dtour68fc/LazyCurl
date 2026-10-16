package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dtour68fc/LazyCurl/internal/api"
	"github.com/dtour68fc/LazyCurl/internal/import/postman"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
)

func (s *Server) registerCollectionTools(m *mcpsrv.MCPServer) {
	m.AddTool(mcp.NewTool("list_collections",
		mcp.WithDescription("List every Collection in the pinned project, with folder/request counts (counts recurse into nested folders)."),
	), s.handleListCollections)

	m.AddTool(mcp.NewTool("get_collection",
		mcp.WithDescription("Fetch the full structure of one Collection (folders, nested folders, requests) as JSON. Also serves as live schema documentation for what a request/folder object looks like."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Exact, case-sensitive Collection name.")),
	), s.handleGetCollection)

	m.AddTool(mcp.NewTool("create_collection",
		mcp.WithDescription("Create a new, empty Collection in the pinned project. Fails if a Collection with this name already exists."),
		mcp.WithString("name", mcp.Required()),
		mcp.WithString("description"),
	), s.handleCreateCollection)

	m.AddTool(mcp.NewTool("create_folder",
		mcp.WithDescription("Create a folder inside a Collection, at an optional nested path (auto-creating any missing intermediate folders). Fails if a folder with this exact name already exists at that level."),
		mcp.WithString("collection", mcp.Required()),
		mcp.WithArray("path", mcp.Description("Folder names from outermost to innermost identifying where to create the new folder; omit/empty for the Collection root.")),
		mcp.WithString("name", mcp.Required(), mcp.Description("Name of the new folder.")),
		mcp.WithString("description"),
	), s.handleCreateFolder)

	m.AddTool(mcp.NewTool("create_request",
		mcp.WithDescription("Create one request inside a Collection, at an optional nested folder path (auto-creating any missing intermediate folders). An ID is generated server-side; any caller-supplied 'id' field inside request is ignored. Call get_collection first to see the exact request JSON shape (CollectionRequest: name, protocol \"http\"|\"grpc\", method, url, params, headers, auth, body, scripts, tests, grpc{...})."),
		mcp.WithString("collection", mcp.Required()),
		mcp.WithArray("path", mcp.Description("Folder path (outermost to innermost); omit/empty for the Collection root.")),
		mcp.WithObject("request", mcp.Required(), mcp.Description("A CollectionRequest object (minus id).")),
	), s.handleCreateRequest)

	m.AddTool(mcp.NewTool("update_request",
		mcp.WithDescription("Replace every field (except ID) of an existing request anywhere in a Collection's folder tree, identified by request_id."),
		mcp.WithString("collection", mcp.Required()),
		mcp.WithString("request_id", mcp.Required()),
		mcp.WithObject("request", mcp.Required(), mcp.Description("Full replacement CollectionRequest object (ID is preserved regardless of what's passed here).")),
	), s.handleUpdateRequest)

	m.AddTool(mcp.NewTool("delete_request",
		mcp.WithDescription("Delete a request anywhere in a Collection's folder tree, identified by request_id."),
		mcp.WithString("collection", mcp.Required()),
		mcp.WithString("request_id", mcp.Required()),
	), s.handleDeleteRequest)

	m.AddTool(mcp.NewTool("bulk_create",
		mcp.WithDescription("Create many folders and/or requests in one Collection with a SINGLE underlying push - the primary tool for migrating large Postman/Insomnia exports or mass-creating gRPC requests. Each item: {path: [...], type: \"folder\"|\"request\", folder: {name, description}, request: {...CollectionRequest...}}. Bad items are skipped (not aborted) and reported back in errors."),
		mcp.WithString("collection", mcp.Required()),
		mcp.WithArray("items", mcp.Required(), mcp.Description("Array of {path, type, folder?, request?} items - see tool description.")),
	), s.handleBulkCreate)

	m.AddTool(mcp.NewTool("import_postman_collection",
		mcp.WithDescription("Import a full Postman Collection v2.1 export (as raw JSON text) as a brand-new Collection in the pinned project. Fails if a Collection with the final name already exists. Returns any conversion warnings (e.g. things Postman-specific that didn't survive, like pre-request scripts)."),
		mcp.WithString("raw_json", mcp.Required(), mcp.Description("The full raw Postman Collection v2.1 export JSON text.")),
		mcp.WithString("collection_name", mcp.Description("Optional override for the imported Collection's name.")),
	), s.handleImportPostmanCollection)
}

type collectionSummary struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	FolderCount  int    `json:"folder_count"`
	RequestCount int    `json:"request_count"`
}

func (s *Server) handleListCollections(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	resources, err := s.listCollectionResources(ctx)
	if err != nil {
		return errResult(err), nil
	}

	summaries := make([]collectionSummary, 0, len(resources))
	for _, r := range resources {
		var col api.CollectionFile
		if err := json.Unmarshal(r.GetContent(), &col); err != nil {
			return errResult(fmt.Errorf("parsing collection %q: %w", r.GetName(), err)), nil
		}
		summaries = append(summaries, collectionSummary{
			Name:         col.Name,
			Description:  col.Description,
			FolderCount:  countFolders(col.Folders),
			RequestCount: countRequests(col.Requests, col.Folders),
		})
	}
	return jsonResult(summaries)
}

func (s *Server) handleGetCollection(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, err := req.RequireString("name")
	if err != nil {
		return errResult(err), nil
	}
	col, _, err := s.pullCollection(ctx, name)
	if err != nil {
		return errResult(err), nil
	}
	return jsonResult(col)
}

func (s *Server) handleCreateCollection(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, err := req.RequireString("name")
	if err != nil {
		return errResult(err), nil
	}
	description := req.GetString("description", "")

	if _, _, err := s.pullCollection(ctx, name); err == nil {
		return errResult(fmt.Errorf("collection %q already exists", name)), nil
	}

	col := &api.CollectionFile{Name: name, Description: description, Project: s.project}
	if err := s.pushCollection(ctx, col); err != nil {
		return errResult(err), nil
	}
	return jsonResult(map[string]any{"success": true, "name": name})
}

func (s *Server) handleCreateFolder(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	collectionName, err := req.RequireString("collection")
	if err != nil {
		return errResult(err), nil
	}
	name, err := req.RequireString("name")
	if err != nil {
		return errResult(err), nil
	}
	description := req.GetString("description", "")
	path := req.GetStringSlice("path", nil)

	col, _, err := s.pullCollection(ctx, collectionName)
	if err != nil {
		return errResult(err), nil
	}

	folders, _, err := navigateFolders(col, path, true)
	if err != nil {
		return errResult(err), nil
	}
	if findFolderAtLevel(*folders, name) {
		return errResult(fmt.Errorf("folder %q already exists at this path", name)), nil
	}
	*folders = append(*folders, api.Folder{Name: name, Description: description})

	if err := s.pushCollection(ctx, col); err != nil {
		return errResult(err), nil
	}
	return jsonResult(map[string]any{"success": true, "name": name})
}

func (s *Server) handleCreateRequest(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	collectionName, err := req.RequireString("collection")
	if err != nil {
		return errResult(err), nil
	}
	path := req.GetStringSlice("path", nil)
	rawRequest, ok := req.GetArguments()["request"]
	if !ok {
		return errResult(fmt.Errorf("request is required")), nil
	}

	col, _, err := s.pullCollection(ctx, collectionName)
	if err != nil {
		return errResult(err), nil
	}

	_, requests, err := navigateFolders(col, path, true)
	if err != nil {
		return errResult(err), nil
	}

	var newReq api.CollectionRequest
	if err := decodeInto(rawRequest, &newReq); err != nil {
		return errResult(err), nil
	}
	newReq.ID = api.GenerateID()
	*requests = append(*requests, newReq)

	if err := s.pushCollection(ctx, col); err != nil {
		return errResult(err), nil
	}
	return jsonResult(map[string]any{"success": true, "id": newReq.ID})
}

func (s *Server) handleUpdateRequest(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	collectionName, err := req.RequireString("collection")
	if err != nil {
		return errResult(err), nil
	}
	requestID, err := req.RequireString("request_id")
	if err != nil {
		return errResult(err), nil
	}
	rawRequest, ok := req.GetArguments()["request"]
	if !ok {
		return errResult(fmt.Errorf("request is required")), nil
	}

	col, _, err := s.pullCollection(ctx, collectionName)
	if err != nil {
		return errResult(err), nil
	}

	existing, found := findRequestByID(col, requestID)
	if !found {
		return errResult(fmt.Errorf("request %q not found", requestID)), nil
	}

	var updated api.CollectionRequest
	if err := decodeInto(rawRequest, &updated); err != nil {
		return errResult(err), nil
	}
	updated.ID = existing.ID
	*existing = updated

	if err := s.pushCollection(ctx, col); err != nil {
		return errResult(err), nil
	}
	return jsonResult(map[string]any{"success": true, "id": existing.ID})
}

func (s *Server) handleDeleteRequest(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	collectionName, err := req.RequireString("collection")
	if err != nil {
		return errResult(err), nil
	}
	requestID, err := req.RequireString("request_id")
	if err != nil {
		return errResult(err), nil
	}

	col, _, err := s.pullCollection(ctx, collectionName)
	if err != nil {
		return errResult(err), nil
	}

	if !deleteRequestByID(col, requestID) {
		return errResult(fmt.Errorf("request %q not found", requestID)), nil
	}

	if err := s.pushCollection(ctx, col); err != nil {
		return errResult(err), nil
	}
	return jsonResult(map[string]any{"success": true})
}

func (s *Server) handleBulkCreate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	collectionName, err := req.RequireString("collection")
	if err != nil {
		return errResult(err), nil
	}
	rawItems, ok := req.GetArguments()["items"].([]any)
	if !ok {
		return errResult(fmt.Errorf("items must be a non-empty array")), nil
	}

	col, _, err := s.pullCollection(ctx, collectionName)
	if err != nil {
		return errResult(err), nil
	}

	var foldersCreated, requestsCreated int
	var itemErrors []string

	for i, rawItem := range rawItems {
		itemMap, ok := rawItem.(map[string]any)
		if !ok {
			itemErrors = append(itemErrors, fmt.Sprintf("item %d: not an object", i))
			continue
		}

		var path []string
		if rawPath, ok := itemMap["path"].([]any); ok {
			for _, p := range rawPath {
				if str, ok := p.(string); ok {
					path = append(path, str)
				}
			}
		}

		itemType, _ := itemMap["type"].(string)

		folders, requests, err := navigateFolders(col, path, true)
		if err != nil {
			itemErrors = append(itemErrors, fmt.Sprintf("item %d: %v", i, err))
			continue
		}

		switch itemType {
		case "folder":
			folderArg, ok := itemMap["folder"].(map[string]any)
			if !ok {
				itemErrors = append(itemErrors, fmt.Sprintf("item %d: missing folder object", i))
				continue
			}
			name, _ := folderArg["name"].(string)
			if name == "" {
				itemErrors = append(itemErrors, fmt.Sprintf("item %d: folder.name is required", i))
				continue
			}
			if findFolderAtLevel(*folders, name) {
				itemErrors = append(itemErrors, fmt.Sprintf("item %d: folder %q already exists at this path", i, name))
				continue
			}
			description, _ := folderArg["description"].(string)
			*folders = append(*folders, api.Folder{Name: name, Description: description})
			foldersCreated++

		case "request":
			requestArg, ok := itemMap["request"]
			if !ok {
				itemErrors = append(itemErrors, fmt.Sprintf("item %d: missing request object", i))
				continue
			}
			var newReq api.CollectionRequest
			if err := decodeInto(requestArg, &newReq); err != nil {
				itemErrors = append(itemErrors, fmt.Sprintf("item %d: %v", i, err))
				continue
			}
			newReq.ID = api.GenerateID()
			*requests = append(*requests, newReq)
			requestsCreated++

		default:
			itemErrors = append(itemErrors, fmt.Sprintf("item %d: unknown type %q (want \"folder\" or \"request\")", i, itemType))
		}
	}

	if err := s.pushCollection(ctx, col); err != nil {
		return errResult(err), nil
	}

	return jsonResult(map[string]any{
		"folders_created":  foldersCreated,
		"requests_created": requestsCreated,
		"errors":           itemErrors,
	})
}

func (s *Server) handleImportPostmanCollection(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	rawJSON, err := req.RequireString("raw_json")
	if err != nil {
		return errResult(err), nil
	}
	nameOverride := req.GetString("collection_name", "")

	result, err := postman.ImportCollectionFromBytes([]byte(rawJSON))
	if err != nil {
		return errResult(fmt.Errorf("importing postman collection: %w", err)), nil
	}
	if result.Collection == nil {
		return errResult(fmt.Errorf("postman import did not produce a collection (is raw_json a Collection export, not an Environment?)")), nil
	}

	col := result.Collection
	if nameOverride != "" {
		col.Name = nameOverride
	}
	col.Project = s.project

	if _, _, err := s.pullCollection(ctx, col.Name); err == nil {
		return errResult(fmt.Errorf("collection %q already exists", col.Name)), nil
	}

	if err := s.pushCollection(ctx, col); err != nil {
		return errResult(err), nil
	}

	return jsonResult(map[string]any{
		"success":  true,
		"name":     col.Name,
		"warnings": result.Summary.Warnings,
	})
}
