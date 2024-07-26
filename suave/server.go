package suave

import (
	"context"

	astriaPb "buf.build/gen/go/astria/astria/protocolbuffers/go/astria/execution/v1alpha2"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/grpc/execution"
	"github.com/ethereum/go-ethereum/log"
	suaveProto "github.com/ethereum/go-ethereum/proto/gen/suave"
	suaveSdk "github.com/ethereum/go-ethereum/suave/sdk"
	"google.golang.org/protobuf/proto"
)

type Kettles = []common.Address

type SUAVEToBExecutionServer struct {
	// The inner server implementation, drives Geth's engine API using Astria's Exeuction API requests
	inner execution.ExecutionServiceServerV1Alpha2

	// TODO: Client to the SUAVE network
	suaveClient *suaveSdk.Client

	// Kettle addresses to validate against
	kettles Kettles
}

func NewSUAVEToBExecutionServer(eth *eth.Ethereum, kettleRPC string) (*SUAVEToBExecutionServer, error) {
	// initialize suave sdk client
	suaveClient := suave.NewClient(kettleRPC)

	// get list of kettles
	kettles, err := suaveClient.GetKettles()
	if err != nil {
		log.Warn("Error getting kettles from SUAVE network", err)
	}

	return &SUAVEToBExecutionServer{
		inner:       *execution.NewExecutionServiceServerV1Alpha2(eth),
		suaveClient: nil,
		kettles:     Kettles{},
	}, nil

}

// filterSUAVEBundles parses out the SUAVE bundles from the raw rollup txs
func filterSUAVEBundles(txs [][]byte) ([]suaveProto.SUAVEBundle, [][]byte) {
	bundles, rawTxs := []suaveProto.SUAVEBundle{}, [][]byte{}
	for _, tx := range txs {
		// TODO: try to parse as a SUAVE bundle
		bundle := new(suaveProto.SUAVEBundle)
		err := proto.Unmarshal(tx, bundle)
		if err != nil {
			// if it's not a SUAVE bundle, it's a raw tx
			// raw tx parsing and validation done further down the pipeline
			// by the inner Execution API server
			rawTxs = append(rawTxs, tx)
		} else {
			bundles = append(bundles, bundle)
		}
	}
	return bundles, rawTxs
}

func verifyBundleSignature(bundle suaveProto.SUAVEBundle, kettles Kettles) bool {
	// make digestHash out of innerTx
	hashedBody := crypto.Keccak256Hash(bundle.innerTx).Hex()
	digestHash := accounts.TextHash([]byte(hashedBody))

	// get the pubkey from the signature
	signerPubkey, err := crypto.SigToPub(digestHash, bundle.signature)
	if err != nil {
		log.Warn("Error extracting pubkey from bundle signature", err)
		return false
	}

	// verify the signature
	validSig := crypto.VerifySignature(crypto.FromECDSAPub(signerPubkey), digestHash, bundle.signature)
	if !validSig {
		log.Warn("Bundle signature is invalid")
		return false
	}

	// check if the pubkey is one of the kettles
	signerAddr := crypto.PubkeyToAddress(*signerPubkey)
	for _, kettle := range kettles {
		if kettle == signerAddr {
			return true
		}
	}

	// didn't find the signer's pubkey in the list of kettles
	return false
}

// getPayloadsWithValidKettleSignatures returns only the bundles that have valid kettle signatures
func verifyBundleSignatures(bundles []suaveProto.SUAVEBundle, kettles Kettles) []suaveProto.SUAVEBundle {
	validBundles := []suaveProto.SUAVEBundle{}
	for i, bundle := range bundles {
		// verify the signature
		if verifyBundleSignature(bundle, kettles) {
			validBundles = append(validBundles, bundle)
		} else {
			// log error
			log.Warn("Bundle %d has invalid Kettle signature, skipping inclusion in block", i)
		}
	}
	return validBundles
}

func finalTxOrder(bundleTxs []SUAVEBundle, rawTxs [][]byte) [][]byte {
	// prepend the bundle payloads to the rawTxs
	unbundledTxs := [][]byte{}
	for _, bundle := range bundleTxs {
		unbundledTxs = append(unbundledTxs, bundle.innerTx)
	}
	return append(unbundledTxs, rawTxs...)
}

func (s *SUAVEToBExecutionServer) ExecuteBlock(ctx context.Context, req *astriaPb.ExecuteBlockRequest) (*astriaPb.Block, error) {
	// filter out the SUAVE bundles from the raw rollup txs
	bundles, rawTxs := filterSUAVEBundles(req.Transactions)
	// get bundles with valid kettle signatures
	validBundles := verifyBundleSignatures(bundles, s.kettles)
	// derive the tx ordering for this block
	finalTxs := finalTxOrder(validBundles, rawTxs)
	req.Transactions = finalTxs
	// forward to the inner execution server
	return s.inner.ExecuteBlock(ctx, req)
}
