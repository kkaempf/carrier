package deployments

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/kyokomi/emoji"
	"github.com/pkg/errors"
	"github.com/suse/carrier/cli/helpers"
	"github.com/suse/carrier/cli/kubernetes"
	"github.com/suse/carrier/cli/paas/ui"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Quarks struct {
	Debug   bool
	Timeout int
}

const (
	quarksDeploymentID = "quarks"
	quarksVersion      = "6.1.17+0.gec409fd7"
	quarksChartURL     = "https://cloudfoundry-incubator.github.io/quarks-helm/cf-operator-6.1.17+0.gec409fd7.tgz"
)

func (k *Quarks) NeededOptions() kubernetes.InstallationOptions {
	return kubernetes.InstallationOptions{}
}

func (k *Quarks) ID() string {
	return quarksDeploymentID
}

func (k *Quarks) Backup(c *kubernetes.Cluster, ui *ui.UI, d string) error {
	return nil
}

func (k *Quarks) Restore(c *kubernetes.Cluster, ui *ui.UI, d string) error {
	return nil
}

func (k Quarks) Describe() string {
	return emoji.Sprintf(":cloud:Quarks version: %s\n:clipboard:Quarks chart: %s", quarksVersion, quarksChartURL)
}

// Delete removes Quarks from kubernetes cluster
func (k Quarks) Delete(c *kubernetes.Cluster, ui *ui.UI) error {
	ui.Note().Msg("Removing Quarks...")

	currentdir, err := os.Getwd()
	if err != nil {
		return errors.New("Failed uninstalling Quarks: " + err.Error())
	}

	message := "Removing helm release " + quarksDeploymentID
	out, err := helpers.WaitForCommandCompletion(ui, message,
		func() (string, error) {
			helmCmd := fmt.Sprintf("helm uninstall quarks --namespace %s", quarksDeploymentID)
			return helpers.RunProc(helmCmd, currentdir, k.Debug)
		},
	)
	if err != nil {
		if strings.Contains(out, "release: not found") {
			ui.Exclamation().Msgf("%s helm release not found, skipping.\n", quarksDeploymentID)
		} else {
			return errors.New("Failed uninstalling Quarks: " + out)
		}
	}

	message = "Deleting Quarks namespace " + quarksDeploymentID
	warning, err := helpers.WaitForCommandCompletion(ui, message,
		func() (string, error) {
			return c.DeleteNamespaceIfOwned(quarksDeploymentID)
		},
	)
	if err != nil {
		return errors.Wrapf(err, "Failed deleting namespace %s", quarksDeploymentID)
	}
	if warning != "" {
		ui.Exclamation().Msg(warning)
	}

	for _, crd := range []string{
		"quarksstatefulsets.quarks.cloudfoundry.org",
		"quarksjobs.quarks.cloudfoundry.org",
		"boshdeployments.quarks.cloudfoundry.org",
		"quarkssecrets.quarks.cloudfoundry.org",
	} {
		out, err := helpers.Kubectl("delete crds --ignore-not-found=true " + crd)
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("Deleting quarks CRD failed:\n%s", out))
		}
	}

	ui.Success().Msg("Quarks removed")

	return nil
}

func (k Quarks) apply(c *kubernetes.Cluster, ui *ui.UI, options kubernetes.InstallationOptions, upgrade bool) error {
	action := "install"
	if upgrade {
		action = "upgrade"
	}

	currentdir, _ := os.Getwd()

	// Setup Quarks helm values
	var helmArgs []string

	helmArgs = append(helmArgs, "--set global.monitoredID=quarks-secret")

	helmCmd := fmt.Sprintf("helm %s quarks --create-namespace --namespace %s %s %s", action, quarksDeploymentID, quarksChartURL, strings.Join(helmArgs, " "))
	if _, err := helpers.RunProc(helmCmd, currentdir, k.Debug); err != nil {
		return errors.New("Failed installing Quarks")
	}

	for _, podname := range []string{
		"cf-operator",
		"quarks-secret",
		"quarks-job",
	} {
		if err := c.WaitUntilPodBySelectorExist(ui, quarksDeploymentID, "name="+podname, k.Timeout); err != nil {
			return errors.Wrap(err, "failed waiting Quarks "+podname+" deployment to exist")
		}
		if err := c.WaitForPodBySelectorRunning(ui, quarksDeploymentID, "name="+podname, k.Timeout); err != nil {
			return errors.Wrap(err, "failed waiting Quarks "+podname+" deployment to come up")
		}
	}
	err := c.LabelNamespace(quarksDeploymentID, kubernetes.CarrierDeploymentLabelKey, kubernetes.CarrierDeploymentLabelValue)
	if err != nil {
		return err
	}

	ui.Success().Msg("Quarks deployed")

	return nil
}

func (k Quarks) GetVersion() string {
	return quarksVersion
}

func (k Quarks) Deploy(c *kubernetes.Cluster, ui *ui.UI, options kubernetes.InstallationOptions) error {

	_, err := c.Kubectl.CoreV1().Namespaces().Get(
		context.Background(),
		quarksDeploymentID,
		metav1.GetOptions{},
	)
	if err == nil {
		return errors.New("Namespace " + quarksDeploymentID + " present already")
	}

	ui.Note().Msg("Deploying Quarks...")

	return k.apply(c, ui, options, false)
}

func (k Quarks) Upgrade(c *kubernetes.Cluster, ui *ui.UI, options kubernetes.InstallationOptions) error {
	_, err := c.Kubectl.CoreV1().Namespaces().Get(
		context.Background(),
		quarksDeploymentID,
		metav1.GetOptions{},
	)
	if err != nil {
		return errors.New("Namespace " + quarksDeploymentID + " not present")
	}

	ui.Note().Msg("Upgrading Quarks...")

	return k.apply(c, ui, options, true)
}
