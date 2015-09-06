package rootfs_provider

import (
	"fmt"
	"errors"
	"net/url"
	"time"
	"sync"

	"github.com/docker/docker/daemon/graphdriver"
	"github.com/docker/docker/pkg/ioutils"
	"github.com/pivotal-golang/clock"
	"github.com/pivotal-golang/lager"

	"github.com/cloudfoundry-incubator/garden-linux/old/repository_fetcher"
	"github.com/cloudfoundry-incubator/garden-linux/process"
)

type dockerRootFSProvider struct {
	graphDriver   graphdriver.Driver
	volumeCreator VolumeCreator
	repoFetcher   repository_fetcher.RepositoryFetcher
	clock         clock.Clock

	fallback RootFSProvider
	activeMutex  	*sync.Mutex
	active     		map[string]string
}

var ErrInvalidDockerURL = errors.New("invalid docker url")

//go:generate counterfeiter -o fake_graph_driver/fake_graph_driver.go . GraphDriver
type GraphDriver interface {
	graphdriver.Driver
}

func NewDocker(
	repoFetcher repository_fetcher.RepositoryFetcher,
	graphDriver GraphDriver,
	volumeCreator VolumeCreator,
	clock clock.Clock,
) (RootFSProvider, error) {
	return &dockerRootFSProvider{
		repoFetcher:   repoFetcher,
		graphDriver:   graphDriver,
		volumeCreator: volumeCreator,
		clock:         clock,
		activeMutex:	new(sync.Mutex),
		active:			make(map[string]string),
	}, nil
}

func (provider *dockerRootFSProvider) ProvideRootFS(logger lager.Logger, id string, url *url.URL) (string, process.Env, error) {
	if len(url.Path) == 0 {
		return "", nil, ErrInvalidDockerURL
	}

	tag := "latest"
	if len(url.Fragment) > 0 {
		tag = url.Fragment
	}

	imageID, envvars, volumes, err := provider.repoFetcher.Fetch(logger, url, tag)
	if err != nil {
		return "", nil, err
	}

	err = provider.graphDriver.Create(id, imageID)
	if err != nil {
		return "", nil, err
	}

	rootPath, err := provider.graphDriver.Get(id, "")
	if err != nil {
		return "", nil, err
	}

	for _, v := range volumes {
		if err = provider.volumeCreator.Create(rootPath, v); err != nil {
			return "", nil, err
		}
	}
	
	/* Record the container parent image id */
	provider.activeMutex.Lock()
	
	// overwrite maybe better
	//if _,exist := provider.active[id]; !exist {
		provider.active[id] = imageID
	//}

	provider.activeMutex.Unlock()
	
	return rootPath, envvars, nil
}

func (provider *dockerRootFSProvider) CleanupRootFS(logger lager.Logger, id string) error {
	provider.graphDriver.Put(id)

	var err error
	maxAttempts := 10

	for errorCount := 0; errorCount < maxAttempts; errorCount++ {
		err = provider.graphDriver.Remove(id)
		if err == nil {
			break
		}

		logger.Error("cleanup-rootfs", err, lager.Data{
			"current-attempts": errorCount + 1,
			"max-attempts":     maxAttempts,
		})

		provider.clock.Sleep(200 * time.Millisecond)
	}
	
	/* delete the container parent image id */
	provider.activeMutex.Lock()
	delete(provider.active,id)
	provider.activeMutex.Unlock()

	return err
}

/*******************************************************************************
*      Func Name: CommitAndSaveRootFS
*    Description: commit specify container diff and save image to tar
*          Input: logger lager.Logger
*				  id, container id
*				  dest, save filepath must has suffix .tar
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
func (provider *dockerRootFSProvider) CommitAndSaveRootFS(logger lager.Logger, id, dest string) error {
	logger.Info("CommitAndSaveRootFS-START", lager.Data{
		"container-id": id,
		"save-dest":	dest,
	})
	imageID,exist := provider.active[id]; 
	if !exist {
		return fmt.Errorf("Can not find ImageId with specified %s", id)
	}
	
	archive, err := provider.graphDriver.Diff(id,imageID)
	if err != nil {
		return fmt.Errorf("Fail to get diff in %s base %s,due to %s",id,imageID,err.Error())
	}
	
	diff_layer_data := ioutils.NewReadCloserWrapper(archive, func() error {
			err := archive.Close()
			return err
		})
	
	err = provider.repoFetcher.FetcherCommitAndSaveRootFS(logger, id, imageID, dest, diff_layer_data)
	if err != nil {
		return fmt.Errorf("Fail to FetcherCommitAndSaveRootFS in %s base %s,due to %s",id,imageID,err.Error())
	}

	logger.Info("CommitAndSaveRootFS-END", lager.Data{
		"container-id": id,
		"save-dest":	dest,
	})
	return nil
}
