package controllers

import (
	"fmt"
	"os"

	ethereum2v1alpha1 "github.com/kotalco/kotal/apis/ethereum2/v1alpha1"
)

// PrysmClient is Prysmatic Labs Ethereum 2.0 client
type PrysmClient struct{}

// Images
const (
	// EnvPrysmImage is the environment variable used for Prysmatic Labs Prysm client image
	EnvPrysmImage = "PRYSM_IMAGE"
	// DefaultPrysmImage is Prysmatic Labs Prysm client image
	// TODO: update with validator image
	DefaultPrysmImage = "gcr.io/prysmaticlabs/prysm/beacon-chain:v1.0.5"
)

// Args returns command line arguments required for client
func (t *PrysmClient) Args(node *ethereum2v1alpha1.Node) (args []string) {

	args = append(args, PrysmAcceptTermsOfUse)

	args = append(args, PrysmDataDir, PathBlockchainData)

	if len(node.Spec.Eth1Endpoints) != 0 {
		args = append(args, PrysmWeb3Provider, node.Spec.Eth1Endpoints[0])
		for _, provider := range node.Spec.Eth1Endpoints[1:] {
			args = append(args, PrysmFallbackWeb3Provider, provider)
		}
	}

	args = append(args, fmt.Sprintf("--%s", node.Spec.Join))

	if node.Spec.RPCPort != 0 {
		args = append(args, PrysmRPCPort, fmt.Sprintf("%d", node.Spec.RPCPort))
	}

	if node.Spec.RPCHost != "" {
		args = append(args, PrysmRPCHost, node.Spec.RPCHost)
	}

	if node.Spec.GRPC {
		if node.Spec.GRPCPort != 0 {
			args = append(args, PrysmGRPCPort, fmt.Sprintf("%d", node.Spec.GRPCPort))
		}
		if node.Spec.GRPCHost != "" {
			args = append(args, PrysmGRPCHost, node.Spec.GRPCHost)
		}
	} else {
		args = append(args, PrysmDisableGRPC)
	}

	if node.Spec.P2PPort != 0 {
		args = append(args, PrysmP2PTCPPort, fmt.Sprintf("%d", node.Spec.P2PPort))
		args = append(args, PrysmP2PUDPPort, fmt.Sprintf("%d", node.Spec.P2PPort))
	}

	return
}

// Command returns command for running the client
func (t *PrysmClient) Command() (command []string) {
	return
}

// Image returns prysm docker image
func (t *PrysmClient) Image() string {
	if os.Getenv(EnvPrysmImage) == "" {
		return DefaultPrysmImage
	}
	return os.Getenv(EnvPrysmImage)
}
