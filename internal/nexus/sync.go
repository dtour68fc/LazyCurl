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

// resourceIdentity is just enough of a collection/environment's JSON to
// match it against what's already on disk - Name/Project, not the
// filename, since a collection's display Name can be renamed in the UI
// without its file being renamed to match.
type resourceIdentity struct {
	Name    string `json:"name"`
	Project string `json:"project"`
}

// ImportResourcesToGlobal writes each given resource into the normal
// global collections/environments directories, like WriteResourcesToCache
// does for a workspace cache dir - except it does NOT wipe the
// directories first, so it merges into whatever's already there (other
// local collections/environments, or previously-imported ones from other
// projects) rather than replacing it wholesale.
//
// Each resource is matched against existing files by (Name, Project)
// rather than by a freshly-derived filename, and overwritten in place if
// found. Filenames only ever get invented for genuinely new resources -
// otherwise a collection renamed after its first import would get
// written to a second, differently-named file instead of updating the
// original, duplicating it locally on every subsequent push/pull.
// Returns how many resources were written.
func ImportResourcesToGlobal(collectionsDir, environmentsDir string, resources []*nexuscurlv1.Resource) (int, error) {
	for _, dir := range []string{collectionsDir, environmentsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, fmt.Errorf("creating dir %q: %w", dir, err)
		}
	}

	// existingPath[dir][name+"\x00"+project] = file path already on disk
	// for that identity, if any.
	existingPath := make(map[string]map[string]string, 2)
	for _, dir := range []string{collectionsDir, environmentsDir} {
		lookup := make(map[string]string)
		if dir == collectionsDir {
			cols, err := api.LoadAllCollections(dir)
			if err != nil {
				return 0, fmt.Errorf("loading existing collections from %q: %w", dir, err)
			}
			for _, c := range cols {
				lookup[c.Name+"\x00"+c.Project] = c.FilePath
			}
		} else {
			envs, err := api.LoadAllEnvironments(dir)
			if err != nil {
				return 0, fmt.Errorf("loading existing environments from %q: %w", dir, err)
			}
			for _, e := range envs {
				lookup[e.Name+"\x00"+e.Project] = e.FilePath
			}
		}
		existingPath[dir] = lookup
	}

	// usedPaths tracks filenames allocated to a *different* identity
	// during this same import, so two distinctly-named-but-slug-colliding
	// new resources in one batch don't clobber each other.
	usedPaths := make(map[string]string)

	imported := 0
	for _, r := range resources {
		var dir string
		switch r.GetType() {
		case ResourceTypeCollection:
			dir = collectionsDir
		case ResourceTypeEnvironment:
			dir = environmentsDir
		default:
			continue // unknown resource type - ignore rather than fail the whole import
		}

		var id resourceIdentity
		if err := json.Unmarshal(r.GetContent(), &id); err != nil {
			return imported, fmt.Errorf("parsing %q for identity: %w", r.GetName(), err)
		}
		key := id.Name + "\x00" + id.Project

		path, ok := existingPath[dir][key]
		if !ok {
			path = usedPaths[key]
		}
		if path == "" {
			path = filepath.Join(dir, filenameFor(r.GetName())+".json")
			for i := 2; pathTakenByOther(usedPaths, path, key); i++ {
				path = filepath.Join(dir, fmt.Sprintf("%s-%d.json", filenameFor(r.GetName()), i))
			}
		}
		usedPaths[key] = path

		if err := os.WriteFile(path, r.GetContent(), 0o644); err != nil {
			return imported, fmt.Errorf("writing %q: %w", path, err)
		}
		imported++
	}
	return imported, nil
}

// pathTakenByOther reports whether path is already allocated in used to a
// key other than key.
func pathTakenByOther(used map[string]string, path, key string) bool {
	for k, p := range used {
		if p == path && k != key {
			return true
		}
	}
	return false
}

// RemoveProjectResources deletes every local collection/environment file
// in the given directories whose Project field matches project - used
// when the user deselects a project from a workspace's checklist.
//
// Known tradeoff: this also removes any local-only collection/
// environment that happens to share that exact Project tag, even if it
// was never imported from that workspace. That's consistent with the
// rest of this app's design, where Project is a single shared grouping
// key used by both the local-only environment-auto-switch feature and
// NexusCurl import/export - there's no separate "this came from workspace
// X" provenance tag to distinguish the two cases.
func RemoveProjectResources(collectionsDir, environmentsDir, project string) (int, error) {
	removed := 0

	collections, err := api.LoadAllCollections(collectionsDir)
	if err != nil {
		return removed, fmt.Errorf("loading collections from %q: %w", collectionsDir, err)
	}
	for _, c := range collections {
		if c.Project != project {
			continue
		}
		if err := os.Remove(c.FilePath); err != nil && !os.IsNotExist(err) {
			return removed, fmt.Errorf("removing %q: %w", c.FilePath, err)
		}
		removed++
	}

	environments, err := api.LoadAllEnvironments(environmentsDir)
	if err != nil {
		return removed, fmt.Errorf("loading environments from %q: %w", environmentsDir, err)
	}
	for _, e := range environments {
		if e.Project != project {
			continue
		}
		if err := os.Remove(e.FilePath); err != nil && !os.IsNotExist(err) {
			return removed, fmt.Errorf("removing %q: %w", e.FilePath, err)
		}
		removed++
	}

	return removed, nil
}

// CacheCollectionsDir and CacheEnvironmentsDir are the subdirectories
// WriteResourcesToCache uses - exported so the UI layer can point
// CollectionsView/EnvironmentsView at them directly.
func CacheCollectionsDir(cacheDir string) string  { return filepath.Join(cacheDir, "collections") }
func CacheEnvironmentsDir(cacheDir string) string { return filepath.Join(cacheDir, "environments") }
