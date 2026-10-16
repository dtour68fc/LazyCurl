package nexus

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dtour68fc/LazyCurl/internal/api"
	nexuscurlv1 "github.com/dtour68fc/LazyCurl/internal/nexus/pb/v1"
)

// TestImportResourcesToGlobal_MismatchedSlugDoesNotDuplicate covers the
// actual root cause of the reported push/pull duplication: a collection
// created locally via CreateEmptyCollection uses a different (unslugified)
// filename convention than filenameFor, which ImportResourcesToGlobal used
// to always regenerate from on every import. Re-importing the same
// collection (same Name+Project) must update the existing file in place,
// not invent a second differently-named one.
func TestImportResourcesToGlobal_MismatchedSlugDoesNotDuplicate(t *testing.T) {
	tmp := t.TempDir()
	colDir := filepath.Join(tmp, "collections")
	envDir := filepath.Join(tmp, "environments")
	if err := os.MkdirAll(colDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Simulate a pre-existing local file whose name doesn't match what
	// filenameFor would generate for the same collection.
	existing := &api.CollectionFile{Name: "PMC (API)", Project: "PMC"}
	existing.FilePath = filepath.Join(colDir, "pmc-(api).json")
	if err := api.SaveCollection(existing, existing.FilePath); err != nil {
		t.Fatal(err)
	}

	// Re-import the same collection, unchanged, as a push+pull round trip
	// would.
	r, err := ResourceFromCollection(&api.CollectionFile{Name: "PMC (API)", Project: "PMC"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportResourcesToGlobal(colDir, envDir, []*nexuscurlv1.Resource{r}); err != nil {
		t.Fatal(err)
	}

	files, _ := os.ReadDir(colDir)
	if len(files) != 1 {
		names := []string{}
		for _, f := range files {
			names = append(names, f.Name())
		}
		t.Fatalf("expected 1 file, got %d: %v", len(files), names)
	}
}

// TestImportResourcesToGlobal_RepeatedImportIsStable ensures importing the
// same unchanged resource multiple times in a row (repeated push/pull)
// never creates more than one file for it.
func TestImportResourcesToGlobal_RepeatedImportIsStable(t *testing.T) {
	tmp := t.TempDir()
	colDir := filepath.Join(tmp, "collections")
	envDir := filepath.Join(tmp, "environments")

	r, err := ResourceFromCollection(&api.CollectionFile{Name: "PMO(API)", Project: "PMO"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := ImportResourcesToGlobal(colDir, envDir, []*nexuscurlv1.Resource{r}); err != nil {
			t.Fatal(err)
		}
	}

	files, _ := os.ReadDir(colDir)
	if len(files) != 1 {
		names := []string{}
		for _, f := range files {
			names = append(names, f.Name())
		}
		t.Fatalf("expected 1 file after repeated imports, got %d: %v", len(files), names)
	}
}
