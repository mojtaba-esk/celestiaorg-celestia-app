package main

import (
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"

	"github.com/celestiaorg/knuu/pkg/knuu"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
	"github.com/tendermint/tendermint/config"
	"github.com/tendermint/tendermint/crypto"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/privval"
	"github.com/tendermint/tendermint/rpc/client/http"
	"github.com/tendermint/tendermint/types"
)

const (
	rpcPort              = 26657
	p2pPort              = 26656
	grpcPort             = 9090
	dockerSrcURL         = "ghcr.io/celestiaorg/celestia-app"
	secp256k1Type        = "secp256k1"
	ed25519Type          = "ed25519"
	remoteRootDir        = "/home/celestia/.celestia-app"
	persistentVolumeSize = "100Gi"
)

var storedInstances = make(map[string]*knuu.Instance)

type Node struct {
	Name           string
	Version        string
	StartHeight    int64
	InitialPeers   []string
	SignerKey      crypto.PrivKey
	NetworkKey     crypto.PrivKey
	AccountKey     crypto.PrivKey
	SelfDelegation int64
	Instance       *knuu.Instance

	rpcProxyPort  int
	grpcProxyPort int
}

func NewNode(
	name, version string,
	startHeight, selfDelegation int64,
	peers []string,
	signerKey, networkKey, accountKey crypto.PrivKey,
) (*Node, error) {

	// create a md5 hash from dockerSrcURL, version, rpcPort, p2pPort, grpcPort, remoteRootDir, persistentVolumeSize
	settings := fmt.Sprintf("%s:%s:%d:%d:%d:%s:%s", dockerSrcURL, version, rpcPort, p2pPort, grpcPort, remoteRootDir, persistentVolumeSize)
	hash := md5.Sum([]byte(settings))

	instance, err := knuu.NewInstance(name)
	if err != nil {
		return nil, err
	}
	// if instance with same hash exists, clone it else create new instance
	// this is to avoid creating multiple instances with the same settings to save time
	if savedInstance, ok := storedInstances[fmt.Sprintf("%x", hash)]; ok {
		instance, err = savedInstance.CloneWithName(name)
		if err != nil {
			return nil, err
		}
	} else {
		instance, err = knuu.NewInstance(name)
		if err != nil {
			return nil, err
		}
		err = instance.SetImage(fmt.Sprintf("%s:%s", dockerSrcURL, version))
		if err != nil {
			return nil, err
		}
		if err := instance.AddPortTCP(rpcPort); err != nil {
			return nil, err
		}
		if err := instance.AddPortTCP(p2pPort); err != nil {
			return nil, err
		}
		if err := instance.AddPortTCP(grpcPort); err != nil {
			return nil, err
		}
		err = instance.SetMemory("200Mi", "200Mi")
		if err != nil {
			return nil, err
		}
		err = instance.SetCPU("300m")
		if err != nil {
			return nil, err
		}
		err = instance.AddVolumeWithOwner(remoteRootDir, persistentVolumeSize, 10001)
		if err != nil {
			return nil, err
		}
		err = instance.SetArgs("start", fmt.Sprintf("--home=%s", remoteRootDir), "--rpc.laddr=tcp://0.0.0.0:26657")
		if err != nil {
			return nil, err
		}
		_, err = instance.ExecuteCommand(fmt.Sprintf("mkdir -p %s/config", remoteRootDir))
		if err != nil {
			return nil, err
		}
		_, err = instance.ExecuteCommand(fmt.Sprintf("mkdir -p %s/data", remoteRootDir))
		if err != nil {
			return nil, err
		}
		err = instance.SetUser("10001")
		if err != nil {
			return nil, err
		}
		err = instance.Commit()
		if err != nil {
			return nil, err
		}
		storedInstances[fmt.Sprintf("%x", hash)] = instance
	}

	return &Node{
		Name:           name,
		Instance:       instance,
		Version:        version,
		StartHeight:    startHeight,
		InitialPeers:   peers,
		SignerKey:      signerKey,
		NetworkKey:     networkKey,
		AccountKey:     accountKey,
		SelfDelegation: selfDelegation,
	}, nil
}

func (n *Node) Init(genesis types.GenesisDoc, peers []string) error {
	if len(peers) == 0 {
		return fmt.Errorf("no peers provided")
	}

	// Initialize file directories
	rootDir := os.TempDir()
	nodeDir := filepath.Join(rootDir, n.Name)
	for _, dir := range []string{
		filepath.Join(nodeDir, "config"),
		filepath.Join(nodeDir, "data"),
	} {
		if err := os.MkdirAll(dir, os.ModePerm); err != nil {
			return fmt.Errorf("error creating directory %s: %w", dir, err)
		}
	}

	// Create and write the config file
	cfg, err := MakeConfig(n)
	if err != nil {
		return fmt.Errorf("making config: %w", err)
	}
	configFilePath := filepath.Join(nodeDir, "config", "config.toml")
	config.WriteConfigFile(configFilePath, cfg)

	// Store the genesis file
	genesisFilePath := filepath.Join(nodeDir, "config", "genesis.json")
	err = genesis.SaveAs(genesisFilePath)
	if err != nil {
		return fmt.Errorf("saving genesis: %w", err)
	}

	// Create the app.toml file
	appConfig, err := MakeAppConfig(n)
	if err != nil {
		return fmt.Errorf("making app config: %w", err)
	}
	appConfigFilePath := filepath.Join(nodeDir, "config", "app.toml")
	serverconfig.WriteConfigFile(appConfigFilePath, appConfig)

	// Store the node key for the p2p handshake
	nodeKeyFilePath := filepath.Join(nodeDir, "config", "node_key.json")
	err = (&p2p.NodeKey{PrivKey: n.NetworkKey}).SaveAs(nodeKeyFilePath)
	if err != nil {
		return err
	}

	err = os.Chmod(nodeKeyFilePath, 0o777)
	if err != nil {
		return fmt.Errorf("chmod node key: %w", err)
	}

	// Store the validator signer key for consensus
	pvKeyPath := filepath.Join(nodeDir, "config", "priv_validator_key.json")
	pvStatePath := filepath.Join(nodeDir, "data", "priv_validator_state.json")
	(privval.NewFilePV(n.SignerKey, pvKeyPath, pvStatePath)).Save()

	addrBookFile := filepath.Join(nodeDir, "config", "addrbook.json")
	err = WriteAddressBook(peers, addrBookFile)
	if err != nil {
		return fmt.Errorf("writing address book: %w", err)
	}

	err = n.Instance.AddFile(configFilePath, filepath.Join(remoteRootDir, "config", "config.toml"), "10001:10001")
	if err != nil {
		return fmt.Errorf("adding config file: %w", err)
	}

	err = n.Instance.AddFile(genesisFilePath, filepath.Join(remoteRootDir, "config", "genesis.json"), "10001:10001")
	if err != nil {
		return fmt.Errorf("adding genesis file: %w", err)
	}

	err = n.Instance.AddFile(appConfigFilePath, filepath.Join(remoteRootDir, "config", "app.toml"), "10001:10001")
	if err != nil {
		return fmt.Errorf("adding app config file: %w", err)
	}

	err = n.Instance.AddFile(pvKeyPath, filepath.Join(remoteRootDir, "config", "priv_validator_key.json"), "10001:10001")
	if err != nil {
		return fmt.Errorf("adding priv_validator_key file: %w", err)
	}

	err = n.Instance.AddFile(pvStatePath, filepath.Join(remoteRootDir, "data", "priv_validator_state.json"), "10001:10001")
	if err != nil {
		return fmt.Errorf("adding priv_validator_state file: %w", err)
	}

	err = n.Instance.AddFile(nodeKeyFilePath, filepath.Join(remoteRootDir, "config", "node_key.json"), "10001:10001")
	if err != nil {
		return fmt.Errorf("adding node_key file: %w", err)
	}

	err = n.Instance.AddFile(addrBookFile, filepath.Join(remoteRootDir, "config", "addrbook.json"), "10001:10001")
	if err != nil {
		return fmt.Errorf("adding addrbook file: %w", err)
	}

	return nil
}

// AddressP2P returns a P2P endpoint address for the node. This is used for
// populating the address book. This will look something like:
// 3314051954fc072a0678ec0cbac690ad8676ab98@61.108.66.220:26656
func (n Node) AddressP2P(withID bool) string {
	ip, err := n.Instance.GetIP()
	if err != nil {
		panic(err)
	}
	addr := fmt.Sprintf("%v:%d", ip, p2pPort)
	if withID {
		addr = fmt.Sprintf("%x@%v", n.NetworkKey.PubKey().Address().Bytes(), addr)
	}
	return addr
}

// AddressRPC returns an RPC endpoint address for the node.
// This returns the local proxy port that can be used to communicate with the node
func (n Node) AddressRPC() string {
	return fmt.Sprintf("http://127.0.0.1:%d", n.rpcProxyPort)
}

// ExternalAddressRPC returns an RPC endpoint address for the node.
// This returns the external port that can be used to communicate with the node
func (n Node) ExternalAddressRPC() string {
	ip, err := n.Instance.GetIP()
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("http://%s:%d", ip, rpcPort)
}

// AddressGRPC returns a GRPC endpoint address for the node. This returns the
// local proxy port that can be used to communicate with the node
func (n Node) AddressGRPC() string {
	return fmt.Sprintf("127.0.0.1:%d", n.grpcProxyPort)
}

// ExternalAddressGRPC returns a GRPC endpoint address for the node. This returns the
// external port that can be used to communicate with the node
func (n Node) ExternalAddressGRPC() string {
	ip, err := n.Instance.GetIP()
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%s:%d", ip, grpcPort)
}

func (n Node) IsValidator() bool {
	return n.SelfDelegation != 0
}

func (n Node) Client() (*http.HTTP, error) {
	return http.New(n.AddressRPC(), "/websocket")
}

func (n *Node) Start() error {
	if err := n.Instance.Start(); err != nil {
		return err
	}

	if err := n.Instance.WaitInstanceIsRunning(); err != nil {
		return err
	}

	//rpcProxyPort, err := n.Instance.PortForwardTCP(rpcPort)
	//if err != nil {
	//	return fmt.Errorf("forwarding port %d: %w", rpcPort, err)
	//}
	//
	//grpcProxyPort, err := n.Instance.PortForwardTCP(grpcPort)
	//if err != nil {
	//	return fmt.Errorf("forwarding port %d: %w", grpcPort, err)
	//}
	//n.rpcProxyPort = rpcProxyPort
	//n.grpcProxyPort = grpcProxyPort
	return nil
}

func (n *Node) Clone(
	name string,
	signerKey, networkKey, accountKey crypto.PrivKey,
) (*Node, error) {
	clone, err := n.Instance.Clone()
	if err != nil {
		return nil, err
	}
	// TODO: set name
	return &Node{
		Name:           name,
		Version:        n.Version,
		StartHeight:    n.StartHeight,
		InitialPeers:   n.InitialPeers,
		SignerKey:      signerKey,
		NetworkKey:     networkKey,
		AccountKey:     accountKey,
		SelfDelegation: n.SelfDelegation,
		Instance:       clone,

		rpcProxyPort:  n.rpcProxyPort,
		grpcProxyPort: n.grpcProxyPort,
	}, nil
}
