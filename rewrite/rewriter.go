// Package rewrite provides functions to rewrite Dockerfiles
// and docker-compose files from a Lockfile.
package rewrite

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/michaelperel/docker-lock/generate"
)

// Rewriter is used to rewrite base images in docker and docker-compose files
// with their digests.
type Rewriter struct {
	Lockfile *generate.Lockfile
	Suffix   string
	TempDir  string
}

// NewRewriter creates a Rewriter from command line flags.
func NewRewriter(flags *Flags) (*Rewriter, error) {
	lfile, err := readLockfile(flags.LockfilePath)
	if err != nil {
		return nil, err
	}

	dIms, err := dImsNotInCfiles(lfile)
	if err != nil {
		return nil, err
	}

	lfile.DockerfileImages = dIms

	return &Rewriter{
		Lockfile: lfile,
		Suffix:   flags.Suffix,
		TempDir:  flags.TempDir,
	}, nil
}

// Rewrite rewrites docker and docker-compose files' base images with
// digests from a Lockfile.
//
// Rewrite has "transaction"-like properties to ensure all rewrites succeed or
// fail together. The method follows the following steps:
//
// (1) Create a temporary directory in the system default temporary directory
// location or in the location supplied via the command line arg.
//
// (2) Rewrite every file to a file in the temporary directory.
//
// (3) If all rewrites succeed, rename each temporary file to its desired
// outpath. Providing a suffix ensures that the temporary file will not
// overwrite the original. Instead, a new file of the form Dockerfile-suffix,
// docker-compose-suffix.yml, or docker-compose-suffix.yaml will be written.
//
// (4) If an error occurs during renaming, revert all files back to their
// original content.
//
// (5) If reverting fails, return an error with the paths that failed
// to revert.
//
// (6) Delete the temporary directory.
//
// Note: If the Lockfile references a Dockerfile and that same Dockerfile
// is referenced by another docker-compose file, the Dockerfile will be
// rewritten according to the docker-compose file.
func (r *Rewriter) Rewrite() (err error) {
	if len(r.Lockfile.DockerfileImages) == 0 &&
		len(r.Lockfile.ComposefileImages) == 0 {
		return nil
	}

	tmpDirPath, err := ioutil.TempDir(r.TempDir, "docker-lock-tmp")
	if err != nil {
		return err
	}

	defer func() {
		if rmErr := os.RemoveAll(tmpDirPath); rmErr != nil {
			err = fmt.Errorf("%v: %v", rmErr, err)
		}
	}()

	doneCh := make(chan struct{})
	rnCh := r.writeFiles(tmpDirPath, doneCh)

	if err := r.renameFiles(rnCh); err != nil {
		close(doneCh)
		return err
	}

	return nil
}

// dImsNotInCfiles returns all DockerfileImages not referenced by any
// service in a docker-compose file.
func dImsNotInCfiles(
	lfile *generate.Lockfile,
) (map[string][]*generate.DockerfileImage, error) {
	// map (Dockerfile path) -> (set of "docker-compose path/service name")
	dPathsInCPathSvcs := map[string]map[string]struct{}{}

	for cPath, ims := range lfile.ComposefileImages {
		for _, im := range ims {
			dPath := im.DockerfilePath

			if dPath != "" {
				cPathSvc := fmt.Sprintf("%s/%s", cPath, im.ServiceName)

				// In a docker-compose file, a path to a Dockerfile could
				// be absolute or relative yet refer to the same Dockerfile.
				// These file paths should be treated as equal,
				// so all absolute paths are converted to relative paths.
				if filepath.IsAbs(dPath) {
					wd, err := os.Getwd()
					if err != nil {
						return nil, err
					}

					wd = filepath.ToSlash(wd)

					if rel, err := filepath.Rel(wd, dPath); err == nil {
						dPath = filepath.ToSlash(rel)
					}
				}

				switch dPathsInCPathSvcs[dPath] {
				case nil:
					dPathsInCPathSvcs[dPath] = map[string]struct{}{
						cPathSvc: {},
					}
				default:
					dPathsInCPathSvcs[dPath][cPathSvc] = struct{}{}
				}
			}
		}
	}

	// Warn if multiple docker-compose services refer to the same Dockerfile.
	for dPath, cPathSvcs := range dPathsInCPathSvcs {
		if len(cPathSvcs) > 1 {
			dupCPathSvcs := make([]string, len(cPathSvcs))

			i := 0

			for cPathSvc := range cPathSvcs {
				dupCPathSvcs[i] = cPathSvc
				i++
			}

			fmt.Fprintf(
				os.Stderr,
				"WARNING: '%s' referenced in multiple "+
					"docker-compose services '%s', which will result in a "+
					"non-deterministic rewrite of '%s' if the docker-compose "+
					"services would lead to different rewrites.",
				dPath, dupCPathSvcs, dPath,
			)
		}
	}

	// Collect DockerfileImages that are not referenced by docker-compose
	// files.
	dImsNotInCfiles := map[string][]*generate.DockerfileImage{}

	for dPath, ims := range lfile.DockerfileImages {
		if dPathsInCPathSvcs[dPath] == nil {
			dImsNotInCfiles[dPath] = ims
		}
	}

	return dImsNotInCfiles, nil
}

// readLockfile returns a Lockfile from its file path.
func readLockfile(lPath string) (*generate.Lockfile, error) {
	lByt, err := ioutil.ReadFile(lPath) // nolint: gosec
	if err != nil {
		return nil, err
	}

	lfile := generate.Lockfile{}
	if err = json.Unmarshal(lByt, &lfile); err != nil {
		return nil, err
	}

	return &lfile, err
}

// addErrToPathCh adds an error to the rename channel, ensuring not to
// block the calling goroutine.
func addErrToRnCh(err error, rnCh chan<- *rnInfo, doneCh <-chan struct{}) {
	select {
	case <-doneCh:
	case rnCh <- &rnInfo{err: err}:
	}
}
