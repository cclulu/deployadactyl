package mocks

import (
	"github.com/compozed/deployadactyl/interfaces"
	S "github.com/compozed/deployadactyl/structs"

	"io"
)

type receivedCall struct {
	DeploymentInfo S.DeploymentInfo
	Response       io.ReadWriter
}

type StopperCreator struct {
	CreateStopperCall struct {
		TimesCalled int
		Received    []receivedCall
		Returns     struct {
			Stoppers []interfaces.Stopper
			Error    []error
		}
	}
}

func (s *StopperCreator) Create(deploymentInfo S.DeploymentInfo, cfContext interfaces.CFContext, authorization interfaces.Authorization, response io.ReadWriter, foundationURL, appPath string) (interfaces.Action, error) {
	defer func() { s.CreateStopperCall.TimesCalled++ }()

	received := receivedCall{
		DeploymentInfo: deploymentInfo,
		Response:       response,
	}
	s.CreateStopperCall.Received = append(s.CreateStopperCall.Received, received)

	return s.CreateStopperCall.Returns.Stoppers[s.CreateStopperCall.TimesCalled], s.CreateStopperCall.Returns.Error[s.CreateStopperCall.TimesCalled]
}
