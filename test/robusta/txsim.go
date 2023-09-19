package main

import (
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/celestiaorg/knuu/pkg/knuu"
	"github.com/tendermint/tendermint/crypto"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/privval"
)

const (
	dockerPackageTxsim = "ghcr.io/celestiaorg/txsim"
)

type Txsim struct {
	Name       string
	Version    string
	SignerKey  crypto.PrivKey
	NetworkKey crypto.PrivKey
	AccountKey crypto.PrivKey
	Instance   *knuu.Instance

	RPCEndpoints  []string
	GRPCEndpoints []string
	PollTime      time.Duration
	BlobSizes     []int
	Blob          int
	BlobAmounts   int
	Seed          int
	Send          int
}

func NewTxsimNode(
	name, version string,
	signerKey, networkKey, accountKey crypto.PrivKey,
	mnemomic string,
	rpcEndpoints, grpcEndpoints []string,
	pollTime time.Duration,
	blobSizes []int,
	blob, blobAmounts, seed, send int,
) (*Txsim, error) {

	// create a md5 hash from dockerSrcURL, version, remoteRootDir, persistentVolumeSize
	settings := fmt.Sprintf("%s:%s:%s:%s", dockerPackageTxsim, version, remoteRootDir, persistentVolumeSize)
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
		err = instance.SetImage(fmt.Sprintf("%s:%s", dockerPackageTxsim, version))
		if err != nil {
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
		err = instance.SetCommand("/bin/txsim")
		if err != nil {
			return nil, err
		}
		if len(blobSizes) != 2 {
			return nil, fmt.Errorf("blob sizes must be a slice of two integers")
		}
		blobSizesString := fmt.Sprintf("%d-%d", blobSizes[0], blobSizes[1])
		err = instance.SetArgs(
			"--key-mnemonic",
			fmt.Sprintf("%s", mnemomic),
			"--rpc-endpoints",
			strings.Join(rpcEndpoints, ","),
			"--grpc-endpoints",
			strings.Join(grpcEndpoints, ","),
			"--poll-time",
			pollTime.String(),
			"--blob-sizes",
			blobSizesString,
			"--blob",
			fmt.Sprintf("%d", blob),
			"--blob-amounts",
			fmt.Sprintf("%d", blobAmounts),
			"--seed",
			fmt.Sprintf("%d", seed),
			"--send",
			fmt.Sprintf("%d", send),
		)
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
		err = instance.Commit()
		if err != nil {
			return nil, err
		}
		storedInstances[fmt.Sprintf("%x", hash)] = instance
	}

	return &Txsim{
		Name:       name,
		Instance:   instance,
		Version:    version,
		SignerKey:  signerKey,
		NetworkKey: networkKey,
		AccountKey: accountKey,

		RPCEndpoints:  rpcEndpoints,
		GRPCEndpoints: grpcEndpoints,
		PollTime:      pollTime,
		BlobSizes:     blobSizes,
		Blob:          blob,
		BlobAmounts:   blobAmounts,
		Seed:          seed,
	}, nil
}

func (ts *Txsim) Init() error {
	// Initialize file directories
	rootDir := os.TempDir()
	nodeDir := filepath.Join(rootDir, ts.Name)
	for _, dir := range []string{
		filepath.Join(nodeDir, "config"),
		filepath.Join(nodeDir, "data"),
	} {
		if err := os.MkdirAll(dir, os.ModePerm); err != nil {
			return fmt.Errorf("error creating directory %s: %w", dir, err)
		}
	}

	//// Create and write the config file
	//cfg, err := MakeConfig(n)
	//if err != nil {
	//	return fmt.Errorf("making config: %w", err)
	//}
	//configFilePath := filepath.Join(nodeDir, "config", "config.toml")
	//config.WriteConfigFile(configFilePath, cfg)
	//
	//// Store the genesis file
	//genesisFilePath := filepath.Join(nodeDir, "config", "genesis.json")
	//err = genesis.SaveAs(genesisFilePath)
	//if err != nil {
	//	return fmt.Errorf("saving genesis: %w", err)
	//}
	//
	//// Create the app.toml file
	//appConfig, err := MakeAppConfig(n)
	//if err != nil {
	//	return fmt.Errorf("making app config: %w", err)
	//}
	//appConfigFilePath := filepath.Join(nodeDir, "config", "app.toml")
	//serverconfig.WriteConfigFile(appConfigFilePath, appConfig)

	// Store the node key for the p2p handshake
	nodeKeyFilePath := filepath.Join(nodeDir, "config", "node_key.json")
	err := (&p2p.NodeKey{PrivKey: ts.NetworkKey}).SaveAs(nodeKeyFilePath)
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
	(privval.NewFilePV(ts.SignerKey, pvKeyPath, pvStatePath)).Save()

	//addrBookFile := filepath.Join(nodeDir, "config", "addrbook.json")
	//err = WriteAddressBook(peers, addrBookFile)
	//if err != nil {
	//	return fmt.Errorf("writing address book: %w", err)
	//}

	//err = n.Instance.AddFile(configFilePath, filepath.Join(remoteRootDir, "config", "config.toml"), "10001:10001")
	//if err != nil {
	//	return fmt.Errorf("adding config file: %w", err)
	//}
	//
	//err = n.Instance.AddFile(genesisFilePath, filepath.Join(remoteRootDir, "config", "genesis.json"), "10001:10001")
	//if err != nil {
	//	return fmt.Errorf("adding genesis file: %w", err)
	//}
	//
	//err = n.Instance.AddFile(appConfigFilePath, filepath.Join(remoteRootDir, "config", "app.toml"), "10001:10001")
	//if err != nil {
	//	return fmt.Errorf("adding app config file: %w", err)
	//}

	err = ts.Instance.AddFile(pvKeyPath, filepath.Join(remoteRootDir, "config", "priv_validator_key.json"), "10001:10001")
	if err != nil {
		return fmt.Errorf("adding priv_validator_key file: %w", err)
	}

	err = ts.Instance.AddFile(pvStatePath, filepath.Join(remoteRootDir, "data", "priv_validator_state.json"), "10001:10001")
	if err != nil {
		return fmt.Errorf("adding priv_validator_state file: %w", err)
	}

	err = ts.Instance.AddFile(nodeKeyFilePath, filepath.Join(remoteRootDir, "config", "node_key.json"), "10001:10001")
	if err != nil {
		return fmt.Errorf("adding node_key file: %w", err)
	}

	//err = n.Instance.AddFile(addrBookFile, filepath.Join(remoteRootDir, "config", "addrbook.json"), "10001:10001")
	//if err != nil {
	//	return fmt.Errorf("adding addrbook file: %w", err)
	//}

	return nil
}

func (ts *Txsim) Start() error {
	if err := ts.Instance.Start(); err != nil {
		return err
	}

	if err := ts.Instance.WaitInstanceIsRunning(); err != nil {
		return err
	}

	return nil
}
