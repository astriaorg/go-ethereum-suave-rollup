package suave

import (
	"context"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/grpc/execution"
	"github.com/ethereum/go-ethereum/log"
)

type Kettles = []common.Address
type SUAVEBundle struct {
	innerTx   []byte
	signature []byte
}

type SUAVEToBExecutionServer struct {
	// The inner server implementation, drives Geth's engine API using Astria's Exeuction API requests
	inner execution.ExecutionServiceServerV1Alpha2

	// Client to the SUAVE network
	suaveClient *uint

	// Kettle addresses to validate against
	kettles Kettles
}

// filterSUAVEBundles parses out the SUAVE bundles from the raw rollup txs
func filterSUAVEBundles(txs [][]byte) ([]SUAVEBundle, [][]byte) {
	bundles, rawTxs := []SUAVEBundle{}, [][]byte{}
	for _, tx := range txs {
		// TODO: try to parse as a SUAVE bundle
		bundle, err := parseSUAVEBundle(tx)
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

func verifyBundleSignature(bundle SUAVEBundle, kettles Kettles) bool {
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
func verifyBundleSignatures(bundles []SUAVEBundle, kettles Kettles) []SUAVEBundle {
	validBundles := []SUAVEBundle{}
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
	txs := [][]byte{}
	for _, bundle := range bundleTxs {
		txs = append(txs, bundle.innerTx)
	}
	return append(txs, rawTxs...)
}

func (s *SUAVEToBExecutionServer) ExecuteBundle(ctx context.Context, req *execution.ExecuteBundleRequest) (*execution.ExecuteBundleResponse, error) {
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
