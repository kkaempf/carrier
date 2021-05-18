package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"path"
	"regexp"
	"time"

	"github.com/epinio/epinio/deployments"
	"github.com/epinio/epinio/helpers/kubernetes/tailer"
	api "github.com/epinio/epinio/internal/api/v1"
	"github.com/epinio/epinio/internal/api/v1/models"
	"github.com/epinio/epinio/internal/duration"
	"github.com/go-logr/logr"
	"github.com/mholt/archiver/v3"
	"github.com/pkg/errors"
	tekton "github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/wait"
)

func collectSources(log logr.Logger, source string) (string, string, error) {
	files, err := ioutil.ReadDir(source)
	if err != nil {
		return "", "", errors.Wrap(err, "cannot read the apps source files")
	}
	sources := []string{}
	for _, f := range files {
		// The FileInfo entries returned by ReadDir provide
		// only the base name of the file or directory they
		// are for. We have to add back the path of the
		// application directory to get the proper paths to
		// the files and directories to assemble in the
		// tarball.

		sources = append(sources, path.Join(source, f.Name()))
	}
	log.V(3).Info("found app data files", "files", sources)

	// create a tmpDir - tarball dir and POST
	tmpDir, err := ioutil.TempDir("", "epinio-app")
	if err != nil {
		return "", "", errors.Wrap(err, "can't create temp directory")
	}

	tarball := path.Join(tmpDir, "blob.tar")
	err = archiver.Archive(sources, tarball)
	if err != nil {
		return tmpDir, "", errors.Wrap(err, "can't create archive")
	}

	return tmpDir, tarball, nil
}

func (c *EpinioClient) uploadCode(app models.AppRef, tarball string) (*models.UploadResponse, error) {
	b, err := c.upload(api.Routes.Path("AppUpload", app.Org, app.Name), tarball)
	if err != nil {
		return nil, errors.Wrap(err, "can't upload archive")
	}

	// returns git commit and app route
	upload := &models.UploadResponse{}
	if err := json.Unmarshal(b, upload); err != nil {
		return nil, err
	}

	return upload, nil
}

func (c *EpinioClient) stageCode(req models.StageRequest) (*models.StageResponse, error) {
	out, err := json.Marshal(req)
	if err != nil {
		return nil, errors.Wrap(err, "can't marshal upload response")
	}

	b, err := c.post(api.Routes.Path("AppStage", req.App.Org, req.App.Name), string(out))
	if err != nil {
		return nil, errors.Wrap(err, "can't stage app")
	}

	// returns staging ID
	stage := &models.StageResponse{}
	if err := json.Unmarshal(b, stage); err != nil {
		return nil, err
	}

	return stage, nil
}

func (c *EpinioClient) logs(app models.AppRef, stageID string) (context.CancelFunc, error) {
	c.ui.ProgressNote().V(1).Msg("Tailing application logs ...")

	ctx, cancelFunc := context.WithCancel(context.Background())

	selector := labels.NewSelector()

	for _, req := range [][]string{
		{"app.kubernetes.io/managed-by", "epinio"},
		{models.EpinioStageIDLabel, stageID},
		{"app.kubernetes.io/part-of", app.Org},
		{"app.kubernetes.io/name", app.Name},
	} {
		req, err := labels.NewRequirement(req[0], selection.Equals, []string{req[1]})
		if err != nil {
			return cancelFunc, err
		}
		selector = selector.Add(*req)
	}

	err := tailer.Run(c.ui, ctx, &tailer.Config{
		ContainerQuery:        regexp.MustCompile(".*"),
		ExcludeContainerQuery: nil,
		ContainerState:        "running",
		Exclude:               nil,
		Include:               nil,
		Timestamps:            false,
		Since:                 duration.LogHistory(),
		AllNamespaces:         true,
		LabelSelector:         selector,
		TailLines:             nil,
		Template:              tailer.DefaultSingleNamespaceTemplate(),
		Namespace:             "",
		PodQuery:              regexp.MustCompile(".*"),
	}, c.KubeClient)
	if err != nil {
		return cancelFunc, errors.Wrap(err, "failed to start log tail")
	}

	return cancelFunc, nil
}

func (c *EpinioClient) waitForPipelineRun(app models.AppRef, id string) error {
	c.ui.ProgressNote().KeeplineUnder(1).Msg("Running staging")

	cs, err := tekton.NewForConfig(c.KubeClient.RestConfig)
	if err != nil {
		return err
	}
	client := cs.TektonV1beta1().PipelineRuns(deployments.TektonStagingNamespace)

	s := c.ui.Progressf("Waiting for pipelinerun %s", id)
	defer s.Stop()

	return wait.PollImmediate(time.Second, duration.ToAppBuilt(),
		func() (bool, error) {
			l, err := client.List(context.TODO(), metav1.ListOptions{LabelSelector: models.EpinioStageIDLabel + "=" + id})
			if err != nil {
				return false, err
			}
			if len(l.Items) == 0 {
				return false, nil
			}
			for _, pr := range l.Items {
				if pr.Status.CompletionTime != nil {
					return true, nil
				}
			}
			// pr exists, but still running
			return false, nil
		})
}

func (c *EpinioClient) waitForApp(app models.AppRef, id string) error {
	c.ui.ProgressNote().KeeplineUnder(1).Msg("Creating application resources")
	err := c.KubeClient.WaitUntilPodBySelectorExist(
		c.ui, app.Org, fmt.Sprintf("app.kubernetes.io/name=%s,%s=%s", app.Name, models.EpinioStageIDLabel, id),
		duration.ToAppBuilt())
	if err != nil {
		return errors.Wrap(err, "waiting for app to be created failed")
	}

	c.ui.ProgressNote().KeeplineUnder(1).Msg("Starting application")

	err = c.KubeClient.WaitForPodBySelectorRunning(
		c.ui, app.Org, fmt.Sprintf("app.kubernetes.io/name=%s", app.Name),
		duration.ToPodReady())

	if err != nil {
		return errors.Wrap(err, "waiting for app to come online failed")
	}

	return nil
}
