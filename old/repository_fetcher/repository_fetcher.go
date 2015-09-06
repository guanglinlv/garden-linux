package repository_fetcher

import (
	"fmt"
	"io"
	"io/ioutil"
	"path/filepath"
	"os"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cloudfoundry-incubator/garden"
	"github.com/cloudfoundry-incubator/garden-linux/process"
	"github.com/docker/docker/image"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/runconfig"
	"github.com/docker/docker/registry"
	"github.com/docker/docker/utils"
	"github.com/pivotal-golang/clock"
	"github.com/pivotal-golang/lager"
)

type RepositoryFetcher interface {
	Fetch(logger lager.Logger, url *url.URL, tag string) (imageID string, envvars process.Env, volumes []string, err error)
	FetcherCommitAndSaveRootFS(logger lager.Logger, id, imageID, dest string, layerData archive.ArchiveReader) error
}

// apes dockers registry.NewEndpoint
var RegistryNewEndpoint = registry.NewEndpoint

// apes dockers registry.NewSession
var RegistryNewSession = registry.NewSession

// apes docker's *registry.Registry
//go:generate counterfeiter . Registry
type Registry interface {
	GetRepositoryData(repoName string) (*registry.RepositoryData, error)
	GetRemoteTags(registries []string, repository string, token []string) (map[string]string, error)
	GetRemoteHistory(imageID string, registry string, token []string) ([]string, error)
	GetRemoteImageJSON(imageID string, registry string, token []string) ([]byte, int, error)
	GetRemoteImageLayer(imageID string, registry string, token []string, size int64) (io.ReadCloser, error)
}

// apes docker's *graph.Graph
type Graph interface {
	Get(name string) (*image.Image, error)
	Exists(imageID string) bool
	Register(image *image.Image, layer archive.ArchiveReader) error
	Create(layerData archive.ArchiveReader, containerID, containerImage, comment, author string, containerConfig, config *runconfig.Config) (*image.Image, error)
	Delete(name string) error
}

type DockerRepositoryFetcher struct {
	registryProvider RegistryProvider
	graph            Graph

	fetchingLayers map[string]chan struct{}
	fetchingMutex  *sync.Mutex
	
	clock         clock.Clock
}

type dockerImage struct {
	layers []*dockerLayer
}

func (d dockerImage) Env() process.Env {
	envs := process.Env{}
	for _, l := range d.layers {
		envs = envs.Merge(l.env)
	}

	return envs
}

func (d dockerImage) Vols() []string {
	var vols []string
	for _, l := range d.layers {
		vols = append(vols, l.vols...)
	}

	return vols
}

type dockerLayer struct {
	env  process.Env
	vols []string
}

func New(registry RegistryProvider, graph Graph) RepositoryFetcher {
	return &DockerRepositoryFetcher{
		registryProvider: registry,
		graph:            graph,
		fetchingLayers:   map[string]chan struct{}{},
		fetchingMutex:    new(sync.Mutex),
		clock:			  clock.NewClock(),
	}
}

func fetchError(context, registry, reponame string, err error) error {
	return garden.NewServiceUnavailableError(fmt.Sprintf("repository_fetcher: %s: could not fetch image %s from registry %s: %s", context, reponame, registry, err))
}

func (fetcher *DockerRepositoryFetcher) Fetch(
	logger lager.Logger,
	repoURL *url.URL,
	tag string,
) (string, process.Env, []string, error) {
	fLog := logger.Session("fetch", lager.Data{
		"repo": repoURL,
		"tag":  tag,
	})

	fLog.Debug("fetching")

	path := repoURL.Path[1:]
	hostname := fetcher.registryProvider.ApplyDefaultHostname(repoURL.Host)

	registry, err := fetcher.registryProvider.ProvideRegistry(hostname)
	if err != nil {
		logger.Error("failed-to-construct-registry-endpoint", err)
		return "", nil, nil, fetchError("ProvideRegistry", hostname, path, err)
	}

	repoData, err := registry.GetRepositoryData(path)
	if err != nil {
		return "", nil, nil, fetchError("GetRepositoryData", hostname, path, err)
	}

	tagsList, err := registry.GetRemoteTags(repoData.Endpoints, path, repoData.Tokens)
	if err != nil {
		return "", nil, nil, fetchError("GetRemoteTags", hostname, path, err)
	}

	imgID, ok := tagsList[tag]
	if !ok {
		return "", nil, nil, fetchError("looking up tag", hostname, path, fmt.Errorf("unknown tag: %v", tag))
	}

	token := repoData.Tokens

	for _, endpoint := range repoData.Endpoints {
		fLog.Debug("trying", lager.Data{
			"endpoint": endpoint,
			"image":    imgID,
		})

		var image *dockerImage
		image, err = fetcher.fetchFromEndpoint(fLog, registry, endpoint, imgID, token)
		if err == nil {
			fLog.Debug("fetched", lager.Data{
				"endpoint": endpoint,
				"image":    imgID,
				"env":      image.Env(),
				"volumes":  image.Vols(),
			})

			return imgID, image.Env(), image.Vols(), nil
		}
	}

	return "", nil, nil, fetchError("fetchFromEndPoint", hostname, path, fmt.Errorf("all endpoints failed: %v", err))
}

func (fetcher *DockerRepositoryFetcher) fetchFromEndpoint(logger lager.Logger, registry Registry, endpoint string, imgID string, token []string) (*dockerImage, error) {
	history, err := registry.GetRemoteHistory(imgID, endpoint, token)
	if err != nil {
		return nil, err
	}

	var allLayers []*dockerLayer
	for i := len(history) - 1; i >= 0; i-- {
		layer, err := fetcher.fetchLayer(logger, registry, endpoint, history[i], token)
		if err != nil {
			return nil, err
		}

		allLayers = append(allLayers, layer)
	}

	return &dockerImage{allLayers}, nil
}

func (fetcher *DockerRepositoryFetcher) fetchLayer(logger lager.Logger, registry Registry, endpoint string, layerID string, token []string) (*dockerLayer, error) {
	for acquired := false; !acquired; acquired = fetcher.fetching(layerID) {
	}

	defer fetcher.doneFetching(layerID)

	img, err := fetcher.graph.Get(layerID)
	if err == nil {
		logger.Info("using-cached", lager.Data{
			"layer": layerID,
		})

		return &dockerLayer{imgEnv(img, logger), imgVolumes(img)}, nil
	}

	imgJSON, imgSize, err := registry.GetRemoteImageJSON(layerID, endpoint, token)
	if err != nil {
		return nil, fmt.Errorf("get remote image JSON: %v", err)
	}

	img, err = image.NewImgJSON(imgJSON)
	if err != nil {
		return nil, fmt.Errorf("new image JSON: %v", err)
	}

	layer, err := registry.GetRemoteImageLayer(img.ID, endpoint, token, int64(imgSize))
	if err != nil {
		return nil, fmt.Errorf("get remote image layer: %v", err)
	}

	defer layer.Close()

	started := time.Now()

	logger.Info("downloading", lager.Data{
		"layer": layerID,
	})

	err = fetcher.graph.Register(img, layer)
	if err != nil {
		return nil, fmt.Errorf("register: %s", err)
	}

	logger.Info("downloaded", lager.Data{
		"layer": layerID,
		"took":  time.Since(started),
		"vols":  imgVolumes(img),
	})

	return &dockerLayer{imgEnv(img, logger), imgVolumes(img)}, nil
}

func (fetcher *DockerRepositoryFetcher) fetching(layerID string) bool {
	fetcher.fetchingMutex.Lock()

	fetching, found := fetcher.fetchingLayers[layerID]
	if !found {
		fetcher.fetchingLayers[layerID] = make(chan struct{})
		fetcher.fetchingMutex.Unlock()
		return true
	} else {
		fetcher.fetchingMutex.Unlock()
		<-fetching
		return false
	}
}

func (fetcher *DockerRepositoryFetcher) doneFetching(layerID string) {
	fetcher.fetchingMutex.Lock()
	close(fetcher.fetchingLayers[layerID])
	delete(fetcher.fetchingLayers, layerID)
	fetcher.fetchingMutex.Unlock()
}

func imgEnv(img *image.Image, logger lager.Logger) process.Env {
	if img.Config == nil {
		return process.Env{}
	}

	return filterEnv(img.Config.Env, logger)
}

func imgVolumes(img *image.Image) []string {
	var volumes []string

	if img.Config != nil {
		for volumePath, _ := range img.Config.Volumes {
			volumes = append(volumes, volumePath)
		}
	}

	return volumes
}

func filterEnv(env []string, logger lager.Logger) process.Env {
	var filtered []string
	for _, e := range env {
		segs := strings.SplitN(e, "=", 2)
		if len(segs) != 2 {
			// malformed docker image metadata?
			logger.Info("Unrecognised environment variable", lager.Data{"e": e})
			continue
		}
		filtered = append(filtered, e)
	}

	filteredWithNoDups, err := process.NewEnv(filtered)
	if err != nil {
		logger.Error("Invalid environment", err)
	}
	return filteredWithNoDups
}

func (fetcher *DockerRepositoryFetcher) FetcherTarImageLayer(logger lager.Logger, name string, dest io.Writer) error {
	if img, err := fetcher.graph.Get(name); err == nil && img != nil {
		fs, err := img.TarLayer()
		if err != nil {
			return err
		}
		defer fs.Close()

		written, err := io.Copy(dest, fs)
		if err != nil {
			return err
		}
		//logrus.Debugf("rendered layer for %s of [%d] size", image.ID, written)
		logger.Info("FetcherTarImageLayer", lager.Data{
			"layer": img.ID,
			"size":	written,
		})
		return nil
	}
	
	return fmt.Errorf("No such image %s", name)
}

func (fetcher *DockerRepositoryFetcher) FetcherExportImage(logger lager.Logger, name, tempdir string) error {
	for n := name; n != ""; {
		// temporary directory
		tmpImageDir := filepath.Join(tempdir, n)
		if err := os.Mkdir(tmpImageDir, os.FileMode(0755)); err != nil {
			if os.IsExist(err) {
				return nil
			}
			return err
		}

		var version = "1.0"
		var versionBuf = []byte(version)

		if err := ioutil.WriteFile(filepath.Join(tmpImageDir, "VERSION"), versionBuf, os.FileMode(0644)); err != nil {
			return err
		}

		// serialize json
		json, err := os.Create(filepath.Join(tmpImageDir, "json"))
		if err != nil {
			return err
		}
		
		image, err := fetcher.graph.Get(n)
		if err != nil || image == nil {
			return fmt.Errorf("No such image %s", n)
		}
		
		imageInspectRaw, err := image.RawJson()
		if err != nil {
			return err
		}
		
		written, err := json.Write(imageInspectRaw)
		if err != nil {
			return err
		}
		
		if written != len(imageInspectRaw) {
			//logrus.Warnf("%d byes should have been written instead %d have been written", written, len(imageInspectRaw))
			logger.Error("image-json-write", nil, lager.Data{
				"expect-written": written,
				"actual-written": len(imageInspectRaw),
			})
		}

		// serialize filesystem
		fsTar, err := os.Create(filepath.Join(tmpImageDir, "layer.tar"))
		if err != nil {
			return err
		}
		if err := fetcher.FetcherTarImageLayer(logger, n, fsTar); err != nil {
			return err
		}

		n = image.Parent
	}
	return nil	
}

func (fetcher *DockerRepositoryFetcher) FetcherDeleteImage(logger lager.Logger, name string) error {
	var err error
	maxAttempts := 10

	for errorCount := 0; errorCount < maxAttempts; errorCount++ {
		err = fetcher.graph.Delete(name)
		if err == nil {
			break
		}

		logger.Error("FetcherDeleteImage", err, lager.Data{
			"imageID": name,
			"current-attempts": errorCount + 1,
			"max-attempts":     maxAttempts,
		})
		
		fetcher.clock.Sleep(200 * time.Millisecond)
	}
	
	return err
}

func (fetcher *DockerRepositoryFetcher) FetcherCreateImage(layerData archive.ArchiveReader, containerID, containerImage string) (*image.Image, error) {
	parent_img, err := fetcher.graph.Get(containerImage)
	if err != nil {
		return nil, err
	}
	
	// diff image config inherit from parent almost
	img_config := parent_img.Config
	img_config.Image = containerImage	// diff image's parent need to update
	
	img := &image.Image{
		ID:            utils.GenerateRandomID(),
		Comment:       parent_img.Comment,
		Created:       time.Now().UTC(),
		DockerVersion: parent_img.DockerVersion,
		Author:        parent_img.Author,
		Config:        img_config,
		Architecture:  parent_img.Architecture,
		OS:            parent_img.OS,
	}

	if containerID != "" {
		img.Parent = containerImage
		img.Container = containerID	// this we use garden container id ,any impact ?
		img.ContainerConfig = *img_config	// just set the same as parent's config
	}

	if err := fetcher.graph.Register(img, layerData); err != nil {
		return nil, err
	}
	return img, nil
}

/*******************************************************************************
*      Func Name: FetcherCommitAndSaveRootFS
*    Description: commit specify container diff and save image to tar
*          Input: logger lager.Logger
*				  id, container id
*				  imageID, container parent image id
*				  dest, save filepath must has suffix .tar
*				  layerData, archive.ArchiveReader, diff layer data
*          InOut: NA
*         Output: NA
*         Return: error
*        Caution: NA
*          Since: NA
*      Reference: NA
*         Depend: NA
*------------------------------------------------------------------------------
*    Modification History
*    DATE                NAME                      DESCRIPTION
*------------------------------------------------------------------------------
*    2015/07/08       lvguanglin 00177705            Create
*
*******************************************************************************/
func (fetcher *DockerRepositoryFetcher) FetcherCommitAndSaveRootFS(logger lager.Logger, id, imageID, dest string, layerData archive.ArchiveReader) error {
	logger.Info("FetcherCommitAndSaveRootFS-START", lager.Data{
		"container-id": id,
		"parent-imageID": imageID,
		"dest":	dest,
	})
	
	if !strings.HasSuffix(dest, ".tar") {
		return fmt.Errorf("specified %s must be has suffix .tar", dest)
	}
	
	// check given dest is exist or not
	if f, err := os.Stat(dest); err == nil {
		if f.IsDir() {
			return fmt.Errorf("specified %s exist and it is directory not file", dest)
		}
		// delete exist file
		if err := os.Remove(dest); err != nil {
			return fmt.Errorf("specified %s exist and delete failure due to %s", dest, err.Error())
		}
	}
	
	// commit diff as a new image
	diff_img, err := fetcher.FetcherCreateImage(layerData,id,imageID)
	if err != nil {
		return err
	}
	
	random := utils.GenerateRandomID()
	shortLen := 12
	if len(id) < shortLen {
		shortLen = len(id)
	}
	random = random[:shortLen]
	tempdir := filepath.Join("/var/vcap/data/", id, random, "garden-export-")
	
	// get image json
	//tempdir, err := ioutil.TempDir("", "garden-export-")
	if err := os.MkdirAll(tempdir, os.FileMode(0755)); err != nil {
		fetcher.FetcherDeleteImage(logger, diff_img.ID)
		return err
	}
	defer os.RemoveAll(filepath.Join("/var/vcap/data/", id))
	
	if err := fetcher.FetcherExportImage(logger, diff_img.ID, tempdir); err != nil {
		logger.Error("export-image-fail", err, lager.Data{
			"imageID": diff_img.ID,
		})
		fetcher.FetcherDeleteImage(logger, diff_img.ID)
		return err
	}
	
	fs, err := archive.Tar(tempdir, archive.Uncompressed)
	if err != nil {
		fetcher.FetcherDeleteImage(logger, diff_img.ID)
		return err
	}
	defer fs.Close()
	
	destFsTar, err := os.Create(dest)
	if err != nil {
		return err
	}
	
	if _, err := io.Copy(destFsTar, fs); err != nil {
		fetcher.FetcherDeleteImage(logger, diff_img.ID)
		return err
	}
	
	logger.Info("FetcherCommitAndSaveRootFS-END", lager.Data{
		"container-id": id,
		"parent-imageID": imageID,
		"save-dest":	dest,
	})
	
	// the diff commit image is not usefull in garden context any more
	fetcher.FetcherDeleteImage(logger, diff_img.ID)
	
	return nil
}
