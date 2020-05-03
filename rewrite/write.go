package rewrite

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/michaelperel/docker-lock/generate"
	"gopkg.in/yaml.v2"
)

// cImageLine represents an image line such as python:3.6 for a
// docker-compose service that is specified as image: "..."
// rather than in a Dockerfile.
type composeImageLine struct {
	serviceName string
	line        string
}

// writeFiles writes Dockerfiles and docker-compose files to temporary files,
// adding digests from the Lockfile to the base images.
func (r *Rewriter) writeFiles(
	tmpDirPath string,
	doneCh <-chan struct{},
) chan *rnInfo {
	rnCh := make(chan *rnInfo)
	wg := sync.WaitGroup{}

	wg.Add(1)

	go r.writeCfiles(tmpDirPath, rnCh, &wg, doneCh)

	wg.Add(1)

	go r.writeDfiles(tmpDirPath, rnCh, &wg, doneCh)

	go func() {
		wg.Wait()
		close(rnCh)
	}()

	return rnCh
}

// writeDfiles writes Dockerfiles to temporary files, adding digests from
// the Lockfile to the base images.
func (r *Rewriter) writeDfiles(
	tmpDirPath string,
	rnCh chan<- *rnInfo,
	wg *sync.WaitGroup,
	doneCh <-chan struct{},
) {
	defer wg.Done()

	for dPath, ims := range r.Lockfile.DockerfileImages {
		wg.Add(1)

		go r.writeDfile(dPath, ims, tmpDirPath, rnCh, wg, doneCh)
	}
}

// writeCfiles writes docker-compose files to temporary files, adding digests
// from the Lockfile to the base images.
func (r *Rewriter) writeCfiles(
	tmpDirPath string,
	rnCh chan<- *rnInfo,
	wg *sync.WaitGroup,
	doneCh <-chan struct{},
) {
	defer wg.Done()

	for cPath, ims := range r.Lockfile.ComposefileImages {
		wg.Add(1)

		go r.writeCfile(cPath, ims, tmpDirPath, rnCh, wg, doneCh)
	}
}

// writeDfile writes a Dockerfile to a temporary file, adding digests from
// the Lockfile to the base images. The method replaces each
// base image in a FROM instruction with the same image + digest. If a suffix
// is provided, the temporary file will be named Dockerfile-suffix.
func (r *Rewriter) writeDfile(
	dPath string,
	ims []*generate.DockerfileImage,
	tmpDirPath string,
	rnCh chan<- *rnInfo,
	wg *sync.WaitGroup,
	doneCh <-chan struct{},
) {
	defer wg.Done()

	dByt, err := ioutil.ReadFile(dPath) // nolint: gosec
	if err != nil {
		addErrToRnCh(err, rnCh, doneCh)
		return
	}

	stageNames := map[string]bool{}
	statements := strings.Split(string(dByt), "\n")
	imIndex := 0

	for i, s := range statements {
		fields := strings.Fields(s)

		const instIndex = 0 // for instance, FROM is an instruction
		if len(fields) > 0 && strings.ToLower(fields[instIndex]) == "from" {
			// FROM instructions may take the form:
			// FROM <image>
			// FROM <image> AS <stage>
			// FROM <stage> AS <another stage>
			// Only replace the image, never the stage.
			const imLineIndex = 1
			if !stageNames[fields[imLineIndex]] {
				if imIndex >= len(ims) {
					err := fmt.Errorf(
						"more images exist in '%s' than in the Lockfile",
						dPath,
					)
					addErrToRnCh(err, rnCh, doneCh)

					return
				}

				fields[imLineIndex] = fmt.Sprintf(
					"%s:%s@sha256:%s", ims[imIndex].Name,
					ims[imIndex].Tag, ims[imIndex].Digest,
				)
				imIndex++
			}
			// Ensure stage is added to the stage name set:
			// FROM <image> AS <stage>

			// Ensure another stage is added to the stage name set:
			// FROM <stage> AS <another stage>
			const maxNumFields = 4
			if len(fields) == maxNumFields {
				const stageIndex = 3
				stageName := fields[stageIndex]
				stageNames[stageName] = true
			}

			statements[i] = strings.Join(fields, " ")
		}
	}

	if imIndex != len(ims) {
		err := fmt.Errorf(
			"more images exist in the Lockfile than in '%s'", dPath,
		)
		addErrToRnCh(err, rnCh, doneCh)

		return
	}

	wByt := []byte(strings.Join(statements, "\n"))
	oPath := r.dPathWithSuffix(dPath)

	r.writeTempFile(oPath, wByt, tmpDirPath, rnCh, doneCh)
}

// writeCfile writes a docker-compose file to a temporary file, adding
// digests from the Lockfile to the base images. This method handles the
// two ways a docker-compose file can specify base images through services:
// (1) As an "image:" key
// (2) In referenced Dockerfiles with FROM instructions
func (r *Rewriter) writeCfile(
	cPath string,
	ims []*generate.ComposefileImage,
	tmpDirPath string,
	rnCh chan<- *rnInfo,
	wg *sync.WaitGroup,
	doneCh <-chan struct{},
) {
	defer wg.Done()

	cByt, err := ioutil.ReadFile(cPath) // nolint: gosec
	if err != nil {
		addErrToRnCh(err, rnCh, doneCh)
		return
	}

	comp := compose{}
	if err := yaml.Unmarshal(cByt, &comp); err != nil {
		err = fmt.Errorf("from '%s': %v", cPath, err)
		addErrToRnCh(err, rnCh, doneCh)

		return
	}

	svcIms := map[string][]*generate.ComposefileImage{}

	for _, im := range ims {
		svcIms[im.ServiceName] = append(svcIms[im.ServiceName], im)
	}

	if len(comp.Services) != len(svcIms) {
		err := fmt.Errorf(
			"'%s' has '%d' service(s), yet the Lockfile has '%d'", cPath,
			len(comp.Services), len(svcIms),
		)
		addErrToRnCh(err, rnCh, doneCh)

		return
	}

	cilCh := make(chan *composeImageLine)
	cwg := sync.WaitGroup{}

	for svcName, svc := range comp.Services {
		if _, ok := svcIms[svcName]; !ok {
			err := fmt.Errorf(
				"service '%s' exists in '%s', but not in the Lockfile", svcName,
				cPath,
			)
			addErrToRnCh(err, rnCh, doneCh)

			return
		}

		cwg.Add(1)
		wg.Add(1)

		go r.writeDfileOrGetCImageLine(
			svc, svcIms[svcName], tmpDirPath, cilCh, rnCh, &cwg, wg, doneCh,
		)
	}

	go func() {
		cwg.Wait()
		close(cilCh)
	}()

	r.writeCfileFromCImageLine(
		cPath, cByt, tmpDirPath, cilCh, rnCh, doneCh,
	)
}

// writeDfileOrGetCImageLine checks if a docker-compose service
// references a Dockerfile. If it does, the method writes the Dockerfile
// to a temporary file, adding digests from the Lockfile to the base images.
// If it does not, then the service must have an 'image:' key, in which case
// the method places the Lockfile's base image on a channel to be processed
// by the calling goroutine.
func (r *Rewriter) writeDfileOrGetCImageLine(
	svc *service,
	ims []*generate.ComposefileImage,
	tmpDirPath string,
	cilCh chan<- *composeImageLine,
	rnCh chan<- *rnInfo,
	cwg *sync.WaitGroup,
	wg *sync.WaitGroup,
	doneCh <-chan struct{},
) {
	defer wg.Done()
	defer cwg.Done()

	hasDfile := false

	switch build := svc.Build.(type) {
	case map[interface{}]interface{}:
		if build["context"] != nil || build["dockerfile"] != nil {
			hasDfile = true
		}
	case string:
		hasDfile = true
	}

	switch hasDfile {
	case true:
		dIms := make([]*generate.DockerfileImage, len(ims))
		// There must be at least one Image, and all Images must reference
		// the same Dockerfile.
		dPath := ims[0].DockerfilePath

		for i, im := range ims {
			dIms[i] = &generate.DockerfileImage{Image: im.Image}
		}

		wg.Add(1)

		go r.writeDfile(dPath, dIms, tmpDirPath, rnCh, wg, doneCh)
	case false:
		// There must be and can be at most one 'image:' key and therefore
		// one Image in services that do not reference Dockerfiles.
		im := ims[0]

		select {
		case <-doneCh:
		case cilCh <- &composeImageLine{
			serviceName: im.ServiceName,
			line: fmt.Sprintf(
				"%s:%s@sha256:%s", im.Name, im.Tag, im.Digest,
			),
		}:
		}
	}
}

// writeCfileFromCImageLine writes the docker-compose file to a temporary
// file, adding digests from the Lockfile for services that do not
// reference Dockerfiles to the base images (the values for the 'image:'
// keys).
func (r *Rewriter) writeCfileFromCImageLine(
	cPath string,
	cByt []byte,
	tmpDirPath string,
	cilCh chan *composeImageLine,
	rnCh chan<- *rnInfo,
	doneCh <-chan struct{},
) {
	svcImLines := map[string]string{}

	for cil := range cilCh {
		svcImLines[cil.serviceName] = cil.line
	}

	if len(svcImLines) != 0 {
		svcName := ""
		statements := strings.Split(string(cByt), "\n")

		for i, s := range statements {
			possibleSvcName := strings.Trim(s, " :")

			if svcImLines[possibleSvcName] != "" {
				svcName = possibleSvcName
				continue
			}

			if svcName != "" &&
				strings.HasPrefix(strings.TrimLeft(s, " "), "image:") {
				imIndex := strings.Index(s, "image:")
				statements[i] = fmt.Sprintf(
					"%s %s", s[:imIndex+len("image:")], svcImLines[svcName],
				)
				svcName = ""
			}
		}

		wByt := []byte(strings.Join(statements, "\n"))
		oPath := r.cPathWithSuffix(cPath)

		r.writeTempFile(oPath, wByt, tmpDirPath, rnCh, doneCh)
	}
}

// writeTempFile writes bytes to a temporary file, placing the temporary
// file name, the desired output path name, and the bytes of the file
// currently at the desired output path on the rename channel. This
// information can later be used to rename the temporary file to the
// desired output path, and to rollback to the original bytes if there is an
// error.
func (r *Rewriter) writeTempFile(
	oPath string,
	wByt []byte,
	tmpDirPath string,
	rnCh chan<- *rnInfo,
	doneCh <-chan struct{},
) {
	tmpFile, err := ioutil.TempFile(tmpDirPath, "docker-lock-")

	if err != nil {
		addErrToRnCh(err, rnCh, doneCh)
		return
	}
	defer tmpFile.Close()

	if _, err = tmpFile.Write(wByt); err != nil {
		addErrToRnCh(err, rnCh, doneCh)
		return
	}

	origByt, err := r.origByt(oPath)
	if err != nil {
		addErrToRnCh(err, rnCh, doneCh)
		return
	}

	select {
	case <-doneCh:
	case rnCh <- &rnInfo{
		oPath:    oPath,
		tmpOPath: filepath.ToSlash(tmpFile.Name()),
		origByt:  origByt,
	}:
	}
}

// origByt returns the bytes at a path or an error if the path
// exists but has mode type bits set.
func (r *Rewriter) origByt(oPath string) ([]byte, error) {
	fi, err := os.Stat(oPath)
	if err != nil {
		return nil, nil
	}

	if mode := fi.Mode(); !mode.IsRegular() {
		return nil, fmt.Errorf("'%s' is not a regular file", oPath)
	}

	origByt, err := ioutil.ReadFile(oPath) // nolint: gosec
	if err != nil {
		return nil, err
	}

	return origByt, nil
}

// cPathWithSuffix converts a docker-compose path into one with a suffix. If
// a non-empty suffix is defined, the suffix will be inserted in between the
// .yaml or .yml suffix and the file. For instance, if the path is
// docker-compose.yaml, the returned path would be docker-compose-suffix.yaml.
// If the suffix is empty, the original path will be returned.
func (r *Rewriter) cPathWithSuffix(cPath string) string {
	switch r.Suffix {
	case "":
		return cPath
	default:
		ymlSuffix := ""

		switch {
		case strings.HasSuffix(cPath, ".yml"):
			ymlSuffix = ".yml"
		case strings.HasSuffix(cPath, ".yaml"):
			ymlSuffix = ".yaml"
		}

		return fmt.Sprintf(
			"%s-%s%s", cPath[:len(cPath)-len(ymlSuffix)], r.Suffix,
			ymlSuffix,
		)
	}
}

// dPathWithSuffix converts a Dockerfile path into one with a suffix. If
// a non-empty suffix is defined, the suffix will be added to the end of the
// file. For instance, if the path is Dockerfile, the returned path would be
// Dockerfile-suffix. If the suffix is empty, the original path will be
// returned.
func (r *Rewriter) dPathWithSuffix(dPath string) string {
	switch r.Suffix {
	case "":
		return dPath
	default:
		return fmt.Sprintf("%s-%s", dPath, r.Suffix)
	}
}
