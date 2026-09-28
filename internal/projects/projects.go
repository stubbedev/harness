package projects

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/fsext"
	"github.com/stubbedev/harness/internal/lock"
)

const projectsFileName = "projects.json"

// Project represents a tracked project directory.
type Project struct {
	Path         string    `json:"path"`
	DataDir      string    `json:"data_dir"`
	LastAccessed time.Time `json:"last_accessed"`
}

// ProjectList holds the list of tracked projects.
type ProjectList struct {
	Projects []Project `json:"projects"`
}

// lockTimeout bounds how long an update waits for another harness
// process to finish its own.
const lockTimeout = 5 * time.Second

// projectsFilePath returns the path to the projects.json file.
func projectsFilePath() string {
	return filepath.Join(filepath.Dir(config.GlobalConfigData()), projectsFileName)
}

// Load reads the projects list from disk. The file is replaced by rename,
// never rewritten in place, so a read sees a whole list or the previous
// one, never a torn write.
func Load() (*ProjectList, error) {
	data, err := os.ReadFile(projectsFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			return &ProjectList{Projects: []Project{}}, nil
		}
		return nil, err
	}

	var list ProjectList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return &list, nil
}

// Save writes the projects list to disk atomically.
func Save(list *ProjectList) error {
	path := projectsFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return fsext.WriteFileAtomic(path, data, 0o600)
}

// update applies fn to the stored list under a lock every harness process
// takes, so two instances registering at once cannot each read the old
// list and drop the other's entry.
func update(fn func(*ProjectList)) error {
	path := projectsFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), lockTimeout)
	defer cancel()
	release, err := lock.File(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer release()

	list, err := Load()
	if err != nil {
		return err
	}
	fn(list)
	return Save(list)
}

// Register adds or updates a project in the list.
func Register(workingDir, dataDir string) error {
	return update(func(list *ProjectList) {
		now := time.Now().UTC()
		found := false
		for i, p := range list.Projects {
			if p.Path == workingDir {
				list.Projects[i].DataDir = dataDir
				list.Projects[i].LastAccessed = now
				found = true
				break
			}
		}
		if !found {
			list.Projects = append(list.Projects, Project{
				Path:         workingDir,
				DataDir:      dataDir,
				LastAccessed: now,
			})
		}
		// Most recently accessed first.
		slices.SortFunc(list.Projects, func(a, b Project) int {
			return b.LastAccessed.Compare(a.LastAccessed)
		})
	})
}

// List returns all tracked projects sorted by last accessed.
func List() ([]Project, error) {
	list, err := Load()
	if err != nil {
		return nil, err
	}
	return list.Projects, nil
}
