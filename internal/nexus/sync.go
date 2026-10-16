package nexus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dtour68fc/LazyCurl/internal/api"
	nexuscurlv1 "github.com/dtour68fc/LazyCurl/internal/nexus/pb/v1"
)

// Resource type discriminators, as stored on the wire.
const (
	ResourceTypeCollection  = "collection"
	ResourceTypeEnvironment = "environment"
)

var unsafeFilenameChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// filenameFor turns a resource name into a safe-ish filename - matches
// how the rest of the app derives filenames from user-entered names
// (lowercased, spaces to dashes), see environments_view.go.
func filenameFor(name string) string {
	safe := unsafeFilenameChars.ReplaceAllString(strings.ToLower(name), "-")
	safe = strings.Trim(safe, "-")
	if safe == "" {
		safe = "unnamed"
	}
	return safe
}

// ResourceFromCollection serializes a CollectionFile into the wire
// Resource shape - content is exactly the same JSON already written to
// disk for it, so round-tripping through a NexusCurlServer is lossless.
func ResourceFromCollection(c *api.CollectionFile) (*nexuscurlv1.Resource, error) {
	content, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling collection %q: %w", c.Name, err)
	}
	return &nexuscurlv1.Resource{
		Type:    ResourceTypeCollection,
		Name:    c.Name,
		Content: content,
	}, nil
}

// ResourceFromEnvironment serializes an EnvironmentFile into the wire
// Resource shape, same reasoning as ResourceFromCollection.
func ResourceFromEnvironment(e *api.EnvironmentFile) (*nexuscurlv1.Resource, error) {
	content, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling environment %q: %w", e.Name, err)
	}
	return &nexuscurlv1.Resource{
		Type:    ResourceTypeEnvironment,
		Name:    e.Name,
		Content: content,
	}, nil
}

// WriteResourcesToCache replaces the entire contents of a workspace's
// cache directory with exactly the given resources - a full swap, not a
// merge, so a Collection/Environment that's been deleted upstream
// disappears locally too on the next Pull, same as everything else still
// present gets refreshed to the latest pulled copy.
//
// Layout matches what api.LoadAllCollections/LoadAllEnvironments already
// expect to scan (flat directory of .json files), so CollectionsView/
// EnvironmentsView need no special-casing to read from here - just a
// different directory than the normal global one.
func WriteResourcesToCache(cacheDir string, resources []*nexuscurlv1.Resource) error {
	collectionsDir := filepath.Join(cacheDir, "collections")
	environmentsDir := filepath.Join(cacheDir, "environments")

	for _, dir := range []string{collectionsDir, environmentsDir} {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("clearing cache dir %q: %w", dir, err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating cache dir %q: %w", dir, err)
		}
	}

	for _, r := range resources {
		var dir string
		switch r.GetType() {
		case ResourceTypeCollection:
			dir = collectionsDir
		case ResourceTypeEnvironment:
			dir = environmentsDir
		default:
			continue // unknown resource type - ignore rather than fail the whole pull
		}
		path := filepath.Join(dir, filenameFor(r.GetName())+".json")
		if err := os.WriteFile(path, r.GetContent(), 0o644); err != nil {
			return fmt.Errorf("writing %q: %w", path, err)
		}
	}
	return nil
}

// CacheCollectionsDir and CacheEnvironmentsDir are the subdirectories
// WriteResourcesToCache uses - exported so the UI layer can point
// CollectionsView/EnvironmentsView at them directly.
func CacheCollectionsDir(cacheDir string) string  { return filepath.Join(cacheDir, "collections") }
func CacheEnvironmentsDir(cacheDir string) string { return filepath.Join(cacheDir, "environments") }
