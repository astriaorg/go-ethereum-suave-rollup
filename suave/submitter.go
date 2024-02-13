package mempool_subscriber

import (
	"context"
	"errors"
	"math/big"

	"github.com/astriaorg/go-ethereum-suave-rollup/core/types"
	"github.com/astriaorg/go-ethereum-suave-rollup/event"
	"github.com/astriaorg/go-ethereum-suave-rollup/log"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"
	suaveSdk "github.com/ethereum/go-ethereum/suave/sdk"
)

type Config struct {
	KettleRPC       string
	SUAVEPrivateKey string
}

type SuappSubmitter struct {
	suaveClient suaveSdk.Client
	suapp       suaveSdk.Contract
	txSub       event.Subscription
	txChan      chan types.Transaction
}

func NewSuappSubmitter(cfg Config, txSub event.Subscription, txChan chan types.Transaction) (*SuappSubmitter, error) {
	// set up private key
	privKey, err := crypto.HexToECDSA(cfg.SUAVEPrivateKey)
	if err != nil {
		log.Error("MempoolSubscriber failed to parse private key")
		return nil, err
	}

	// get kettle addresses
	kettleRPC, _ := rpc.Dial(cfg.KettleRPC)
	var kettleAddrs []common.Address
	if err := kettleRPC.Call(&kettleAddrs, "eth_kettleAddress"); err != nil {
		log.Error("MempoolSubscriber Failed to get kettle addresses")
		return nil, err
	}

	// set up suave client
	suaveClient := suaveSdk.NewClient(kettleRPC, privKey, kettleAddrs[0])

	// make sure the account is funded
	balance, err := suaveClient.RPC().BalanceAt(context.Background(), crypto.PubkeyToAddress(privKey.PublicKey), nil)
	if err != nil {
		log.Error("MempoolSubscriber failed to get balance for the initialized account")
		return nil, err
	}
	if balance.Cmp(big.NewInt(0)) == 0 {
		log.Error("MempoolSubscriber account is not funded")
		return nil, errors.New("account is not funded")
	}

	return &SuappSubmitter{
		suaveClient: *suaveClient,
		txSub:       txSub,
		txChan:      txChan,
	}, nil
}

// handle events written to the channel
func (ms *SuappSubmitter) Start() error {
	// TODO:
	// - set up the subscription

	for {
		select {
		case err := <-ms.txSub.Err():
			log.Error("MempoolSubscriber tx subscription error", "subscription err", err)
			return err
		case tx := <-ms.txChan:
			log.Info("MempoolSubscriber received transaction", "incoming tx", tx)
			handleTx(tx, ms.suapp)
		}
	}
}

func handleTx(tx types.Transaction, suapp suaveSdk.Contract) error {
	// serialize tx to bytes
	txBytes, err := tx.MarshalBinary()
	if err != nil {
		log.Error("MempoolSubscriber failed to marshal transaction to bytes")
		return err
	}

	// make suave tx
	log.Debug("MempoolSubscriber sending transaction to suapp", "tx", tx)
	receipt, err := suapp.SendTransaction("makeBundle", []interface{}{}, txBytes)
	if err != nil {
		log.Error("MempoolSubscriber transaction to suapp failed", "receipt", receipt)
		return errors.New("suapp transaction failed")
	}
	log.Debug("MempoolSubscriber transaction to suapp succeeded", "receipt", receipt)

	return nil
}

func (ms *SuappSubmitter) Stop() error {
	// TODO: unsubscribe
	log.Info("SuappSubmitter stopped")
	return nil
}
