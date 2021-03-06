// Package pusher handles pushing to individual Cloud Foundry instances.
package push

import (
	"fmt"
	"io"

	C "github.com/compozed/deployadactyl/constants"
	I "github.com/compozed/deployadactyl/interfaces"
	"github.com/compozed/deployadactyl/state"
	S "github.com/compozed/deployadactyl/structs"
)

// TemporaryNameSuffix is used when deploying the new application in order to
// not overide the existing application name.
const TemporaryNameSuffix = "-new-build-"

// Pusher has a courier used to push applications to Cloud Foundry.
// It represents logging into a single foundation to perform operations.
type Pusher struct {
	Courier        I.Courier
	DeploymentInfo S.DeploymentInfo
	EventManager   I.EventManager
	Response       io.ReadWriter
	Log            I.DeploymentLogger
	FoundationURL  string
	AppPath        string
	Environment    S.Environment
	Fetcher        I.Fetcher
	CFContext      I.CFContext
	Auth           I.Authorization
}

// Login will login to a Cloud Foundry instance.
func (p Pusher) Initially() error {
	p.Log.Debugf(
		`logging into cloud foundry with parameters:
		foundation URL: %+v
		username: %+v
		org: %+v
		space: %+v`,
		p.FoundationURL, p.DeploymentInfo.Username, p.DeploymentInfo.Org, p.DeploymentInfo.Space,
	)

	output, err := p.Courier.Login(
		p.FoundationURL,
		p.DeploymentInfo.Username,
		p.DeploymentInfo.Password,
		p.DeploymentInfo.Org,
		p.DeploymentInfo.Space,
		p.DeploymentInfo.SkipSSL,
	)
	p.Response.Write(output)
	if err != nil {
		p.Log.Errorf("could not login to %s", p.FoundationURL)
		return state.LoginError{p.FoundationURL, output}
	}

	p.Log.Infof("logged into cloud foundry %s", p.FoundationURL)

	return nil
}

// Push pushes a single application to a Clound Foundry instance using blue green deployment.
// Blue green is done by pushing a new application with the appName+TemporaryNameSuffix+UUID.
// It pushes the new application with the existing appName route.
// It will map a load balanced domain if provided in the config.yml.
//
// Returns Cloud Foundry logs if there is an error.

func (p Pusher) Verify() error {
	return nil
}

func (p Pusher) Execute() error {

	var (
		tempAppWithUUID = p.DeploymentInfo.AppName + TemporaryNameSuffix + p.DeploymentInfo.UUID
		err             error
	)

	err = p.pushApplication(tempAppWithUUID, p.AppPath)
	if err != nil {
		return err
	}

	if p.DeploymentInfo.Domain != "" {
		err = p.mapTempAppToLoadBalancedDomain(tempAppWithUUID)
		if err != nil {
			return err
		}
	}

	p.Log.Debugf("emitting a %s event", C.PushFinishedEvent)
	pushData := S.PushEventData{
		AppPath:         p.AppPath,
		FoundationURL:   p.FoundationURL,
		TempAppWithUUID: tempAppWithUUID,
		DeploymentInfo:  &p.DeploymentInfo,
		Courier:         p.Courier,
		Response:        p.Response,
	}

	err = p.EventManager.Emit(I.Event{Type: C.PushFinishedEvent, Data: pushData})
	if err != nil {
		return err
	}
	p.Log.Infof("emitted a %s event", C.PushFinishedEvent)

	event := PushFinishedEvent{
		CFContext:           p.CFContext,
		Auth:                p.Auth,
		Response:            p.Response,
		AppPath:             p.AppPath,
		FoundationURL:       p.FoundationURL,
		TempAppWithUUID:     tempAppWithUUID,
		Data:                p.DeploymentInfo.Data,
		Courier:             p.Courier,
		Manifest:            p.DeploymentInfo.Manifest,
		HealthCheckEndpoint: p.DeploymentInfo.HealthCheckEndpoint,
	}
	err = p.EventManager.EmitEvent(event)
	if err != nil {
		return err
	}
	p.Log.Infof("emitted a %s event", event.Name())

	return nil
}

// FinishPush will delete the original application if it existed. It will always
// rename the the newly pushed application to the appName.
func (p Pusher) Success() error {
	if p.Courier.Exists(p.DeploymentInfo.AppName) {
		err := p.unMapLoadBalancedRoute()
		if err != nil {
			return err
		}

		err = p.deleteApplication(p.DeploymentInfo.AppName)
		if err != nil {
			return err
		}
	}

	err := p.renameNewBuildToOriginalAppName()
	if err != nil {
		return err
	}

	return nil
}

// UndoPush is only called when a Push fails. If it is not the first deployment, UndoPush will
// delete the temporary application that was pushed.
// If is the first deployment, UndoPush will rename the failed push to have the appName.
func (p Pusher) Undo() error {

	tempAppWithUUID := p.DeploymentInfo.AppName + TemporaryNameSuffix + p.DeploymentInfo.UUID
	if !p.Environment.EnableRollback {
		p.Log.Errorf("Failed to deploy, deployment not rolled back due to EnableRollback=false")

		return p.Success()
	} else {

		if p.Courier.Exists(p.DeploymentInfo.AppName) {
			p.Log.Errorf("rolling back deploy of %s", tempAppWithUUID)

			err := p.deleteApplication(tempAppWithUUID)
			if err != nil {
				return err
			}

		} else {
			p.Log.Errorf("app %s did not previously exist: not rolling back", p.DeploymentInfo.AppName)

			err := p.renameNewBuildToOriginalAppName()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// CleanUp removes the temporary directory created by the Executor.
func (p Pusher) Finally() error {
	return p.Courier.CleanUp()
}

func (p Pusher) pushApplication(appName, appPath string) error {
	p.Log.Debugf("pushing app %s to %s", appName, p.DeploymentInfo.Domain)
	p.Log.Debugf("tempdir for app %s: %s", appName, appPath)

	var (
		pushOutput          []byte
		cloudFoundryLogs    []byte
		err                 error
		cloudFoundryLogsErr error
	)

	defer func() { p.Response.Write(cloudFoundryLogs) }()
	defer func() { p.Response.Write(pushOutput) }()

	pushOutput, err = p.Courier.Push(appName, appPath, p.DeploymentInfo.AppName, p.DeploymentInfo.Instances)
	p.Log.Infof("output from Cloud Foundry: \n%s", pushOutput)
	if err != nil {
		defer func() { p.Log.Errorf("logs from %s: \n%s", appName, cloudFoundryLogs) }()

		cloudFoundryLogs, cloudFoundryLogsErr = p.Courier.Logs(appName)
		if cloudFoundryLogsErr != nil {
			return state.CloudFoundryGetLogsError{err, cloudFoundryLogsErr}
		}

		return state.PushError{}
	}

	p.Log.Infof("successfully deployed new build %s", appName)

	return nil
}

func (p Pusher) mapTempAppToLoadBalancedDomain(appName string) error {
	p.Log.Debugf("mapping route for %s to %s", p.DeploymentInfo.AppName, p.DeploymentInfo.Domain)

	out, err := p.Courier.MapRoute(appName, p.DeploymentInfo.Domain, p.DeploymentInfo.AppName)
	if err != nil {
		p.Log.Errorf("could not map %s to %s", p.DeploymentInfo.AppName, p.DeploymentInfo.Domain)
		return state.MapRouteError{out}
	}

	p.Log.Infof("application route created: %s.%s", p.DeploymentInfo.AppName, p.DeploymentInfo.Domain)

	fmt.Fprintf(p.Response, "application route created: %s.%s", p.DeploymentInfo.AppName, p.DeploymentInfo.Domain)

	return nil
}

func (p Pusher) unMapLoadBalancedRoute() error {
	if p.DeploymentInfo.Domain != "" {
		p.Log.Debugf("unmapping route %s", p.DeploymentInfo.AppName)

		out, err := p.Courier.UnmapRoute(p.DeploymentInfo.AppName, p.DeploymentInfo.Domain, p.DeploymentInfo.AppName)
		if err != nil {
			p.Log.Errorf("could not unmap %s", p.DeploymentInfo.AppName)
			return state.UnmapRouteError{p.DeploymentInfo.AppName, out}
		}

		p.Log.Infof("unmapped route %s", p.DeploymentInfo.AppName)
	}

	return nil
}

func (p Pusher) deleteApplication(appName string) error {
	p.Log.Debugf("deleting %s", appName)

	out, err := p.Courier.Delete(appName)
	if err != nil {
		p.Log.Errorf("could not delete %s", appName)
		p.Log.Errorf("deletion error %s", err.Error())
		p.Log.Errorf("deletion output", string(out))
		return state.DeleteApplicationError{appName, out}
	}

	p.Log.Infof("deleted %s", appName)

	return nil
}

func (p Pusher) renameNewBuildToOriginalAppName() error {
	p.Log.Debugf("renaming %s to %s", p.DeploymentInfo.AppName+TemporaryNameSuffix+p.DeploymentInfo.UUID, p.DeploymentInfo.AppName)

	out, err := p.Courier.Rename(p.DeploymentInfo.AppName+TemporaryNameSuffix+p.DeploymentInfo.UUID, p.DeploymentInfo.AppName)
	if err != nil {
		p.Log.Errorf("could not rename %s to %s", p.DeploymentInfo.AppName+TemporaryNameSuffix+p.DeploymentInfo.UUID, p.DeploymentInfo.AppName)
		return state.RenameError{p.DeploymentInfo.AppName + TemporaryNameSuffix + p.DeploymentInfo.UUID, out}
	}

	p.Log.Infof("renamed %s to %s", p.DeploymentInfo.AppName+TemporaryNameSuffix+p.DeploymentInfo.UUID, p.DeploymentInfo.AppName)

	return nil
}
