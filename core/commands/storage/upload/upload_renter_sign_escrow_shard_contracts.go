package upload

import (
	"fmt"

	"github.com/tron-us/go-btfs-common/crypto"
	"github.com/tron-us/go-btfs-common/ledger"
	escrowpb "github.com/tron-us/go-btfs-common/protos/escrow"
	"github.com/tron-us/protobuf/proto"

	"github.com/libp2p/go-libp2p-core/peer"
	cmap "github.com/orcaman/concurrent-map"
)

var (
	escrowChanMaps     = cmap.New()
	escrowContractMaps = cmap.New()
)

func renterSignEscrowContract(rss *RenterSession, shardHash string, shardIndex int, host string, totalPay int64,
	offlineSigning bool, offSignPid peer.ID,
	contractId string) ([]byte, error) {
	hostPid, err := peer.IDB58Decode(host)
	if err != nil {
		return nil, err
	}
	escrowContract, err := newContract(rss, hostPid, totalPay, false, 0, offSignPid, contractId)
	if err != nil {
		return nil, fmt.Errorf("create escrow contract failed: [%v] ", err)
	}
	bc := make(chan []byte)
	shardId := getShardId(rss.ssId, shardHash, shardIndex)
	escrowChanMaps.Set(shardId, bc)
	bytes, err := proto.Marshal(escrowContract)
	if err != nil {
		return nil, err
	}
	escrowContractMaps.Set(shardId, bytes)
	if !offlineSigning {
		errChan := make(chan error)
		go func() {
			sign, err := crypto.Sign(rss.ctxParams.n.PrivateKey, escrowContract)
			if err != nil {
				errChan <- err
				return
			}
			errChan <- nil
			bc <- sign
		}()
		err = <-errChan
		if err != nil {
			return nil, err
		}
	}
	renterSignBytes := <-bc
	escrowChanMaps.Remove(shardId)
	escrowContractMaps.Remove(shardId)
	renterSignedEscrowContract, err := signContractAndMarshalOffSign(escrowContract, renterSignBytes, nil)
	if err != nil {
		return nil, err
	}
	return renterSignedEscrowContract, nil
}

func newContract(rss *RenterSession, hostPid peer.ID, totalPay int64, customizedSchedule bool, period int,
	pid peer.ID, contractId string) (*escrowpb.EscrowContract, error) {
	var err error
	payerPubKey, err := pid.ExtractPublicKey()
	if err != nil {
		return nil, err
	}
	hostPubKey, err := hostPid.ExtractPublicKey()
	if err != nil {
		return nil, err
	}
	if len(rss.ctxParams.cfg.Services.GuardPubKeys) == 0 {
		return nil, fmt.Errorf("No Services.GuardPubKeys are set in config")
	}
	authPubKey, err := convertToPubKey(rss.ctxParams.cfg.Services.GuardPubKeys[0])
	if err != nil {
		return nil, err
	}
	ps := escrowpb.Schedule_MONTHLY
	p := 0
	if customizedSchedule {
		ps = escrowpb.Schedule_CUSTOMIZED
		p = period
	}
	return ledger.NewEscrowContract(contractId,
		payerPubKey, hostPubKey, authPubKey, totalPay, ps, int32(p))
}

func signContractAndMarshalOffSign(unsignedContract *escrowpb.EscrowContract, signedBytes []byte,
	signedContract *escrowpb.SignedEscrowContract) ([]byte, error) {

	if signedContract == nil {
		signedContract = newSignedContract(unsignedContract)
	}
	signedContract.BuyerSignature = signedBytes
	result, err := proto.Marshal(signedContract)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func newSignedContract(contract *escrowpb.EscrowContract) *escrowpb.SignedEscrowContract {
	return &escrowpb.SignedEscrowContract{
		Contract: contract,
	}
}
