package mempool_subscriber

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"

	"github.com/astriaorg/go-ethereum-suave-rollup/core"
	"github.com/astriaorg/go-ethereum-suave-rollup/core/types"
	"github.com/astriaorg/go-ethereum-suave-rollup/event"
	"github.com/astriaorg/go-ethereum-suave-rollup/internal/ethapi"
	"github.com/astriaorg/go-ethereum-suave-rollup/log"
	"github.com/astriaorg/go-ethereum-suave-rollup/node"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/suave/sdk"
	suaveSdk "github.com/ethereum/go-ethereum/suave/sdk"
)

type Config struct {
	KettleRPC       string
	SUAVEPrivateKey string
	SuappAddressHex string
	SuappAbiPath    string
}

type SuappSubmitter struct {
	// suave submission
	kettleAddr  common.Address
	suaveClient suaveSdk.Client
	suapp       suaveSdk.Contract

	// mempool subscription
	ethBackend ethapi.Backend
	txSub      event.Subscription
	done       chan struct{}
}

// New creates a new suapp submitter and registers it as a lifecycle with the node
func New(cfg Config, node *node.Node, backend ethapi.Backend) error {
	// set up private key
	privKey, err := crypto.HexToECDSA(cfg.SUAVEPrivateKey)
	if err != nil {
		log.Error("SuaveSubmitter: failed to parse private key")
		return err
	}

	// Parse Kettle address
	kettleAddr := common.Address{}
	if err := kettleAddr.UnmarshalText([]byte("")); err != nil {
		log.Error("SuaveSubmitter: failed to parse kettle address")
		return err
	}

	// set up suave client
	kettleRPC, _ := rpc.Dial(cfg.KettleRPC)
	suaveClient := suaveSdk.NewClient(kettleRPC, privKey, kettleAddr)

	// make sure the account is funded
	balance, err := suaveClient.RPC().BalanceAt(context.Background(), crypto.PubkeyToAddress(privKey.PublicKey), nil)
	if err != nil {
		log.Error("SuaveSubmitter: failed to get balance for the initialized account")
		return err
	}
	if balance.Cmp(big.NewInt(0)) == 0 {
		log.Error("SuaveSubmitter: account is not funded")
		return errors.New("account is not funded")
	}

	// read contract abi
	suappAbi, err := ReadContractABI(cfg.SuappAbiPath)
	if err != nil {
		log.Error("SuaveSubmitter: failed to read SUAPP abi")
		return err
	}

	// parse suapp address
	suappAddr := common.Address{}
	err = suappAddr.UnmarshalText([]byte(cfg.SuappAddressHex))
	if err != nil {
		log.Error("SuaveSubmitter: failed to parse SUAPP address")
		return err
	}

	// get handle to the suapp
	suapp := sdk.GetContract(suappAddr, suappAbi, suaveClient)

	submitter := &SuappSubmitter{
		kettleAddr:  kettleAddr,
		suaveClient: *suaveClient,
		suapp:       *suapp,
		ethBackend:  backend,
		done:        make(chan struct{}),
	}

	node.RegisterLifecycle(submitter)
	return nil
}

// Starts the subscription to the mempool and runs the submitter loop
func (ss *SuappSubmitter) Start() error {
	txChan := make(chan core.NewTxsEvent)
	ss.txSub = ss.ethBackend.SubscribeNewTxsEvent(txChan)
	go ss.loop(txChan)

	log.Info("SuappSubmitter started")
	return nil
}

// Unsubscribes from the mempool and signals the submitter loop to stop
func (ss *SuappSubmitter) Stop() error {
	ss.txSub.Unsubscribe()
	ss.done <- struct{}{}

	log.Info("SuappSubmitter stopped")
	return nil
}

func (ss *SuappSubmitter) loop(txChan chan core.NewTxsEvent) {
	for {
		select {
		case <-ss.done:
			log.Info("SuaveSubmitter: done signal received, shutting down.")
			return
		case err := <-ss.txSub.Err():
			log.Error("SuaveSubmitter: tx subscription error, shutting down.", "subscription err", err)
			return
		case txEvent := <-txChan:
			log.Info("SuaveSubmitter: received transaction", "incoming tx", txEvent)
			txs := txEvent.Txs
			go submitToSuapp(txs, ss.suapp)
		}
	}
}

// submitToSuapp submits the given transactions to the suapp contract
func submitToSuapp(txs []*types.Transaction, suapp suaveSdk.Contract) error {
	for _, tx := range txs {
		// serialize tx to bytes
		txBytes, err := tx.MarshalBinary()
		if err != nil {
			log.Error("SuaveSubmitter: failed to marshal transaction to bytes")
			return err
		}

		// make suave tx
		log.Debug("SuaveSubmitter: sending transaction to suapp", "tx", tx)
		receipt, err := suapp.SendTransaction("makeBundle", []interface{}{}, txBytes)
		if err != nil {
			log.Error("SuaveSubmitter: transaction to suapp failed", "receipt", receipt)
			return err
		}
		log.Debug("SuaveSubmitter: transaction to suapp succeeded", "receipt", receipt)
	}
	return nil
}

// ReadContractABI reads the SUAPP abi from the given path
func ReadContractABI(path string) (*abi.ABI, error) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return nil, fmt.Errorf("unable to get the current filename")
	}
	dirname := filepath.Dir(filename)

	data, err := os.ReadFile(filepath.Join(dirname, "../out", path))
	if err != nil {
		return nil, err
	}

	var artifact struct {
		Abi      *abi.ABI `json:"abi"`
		Bytecode struct {
			Object string `json:"object"`
		} `json:"bytecode"`
	}
	if err := json.Unmarshal(data, &artifact); err != nil {
		return nil, err
	}

	return artifact.Abi, nil
}
