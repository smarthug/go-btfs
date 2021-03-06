package upload

import (
	"context"
	config "github.com/TRON-US/go-btfs-config"
	renterpb "github.com/TRON-US/go-btfs/protos/renter"
	"github.com/tron-us/go-btfs-common/utils/grpc"

	"github.com/tron-us/go-btfs-common/crypto"
	"github.com/tron-us/go-btfs-common/ledger"
	escrowpb "github.com/tron-us/go-btfs-common/protos/escrow"
	"github.com/tron-us/protobuf/proto"

	cmap "github.com/orcaman/concurrent-map"
)

var (
	payinReqChanMaps = cmap.New()
)

func pay(rss *RenterSession, result *escrowpb.SignedSubmitContractResult, fileSize int64, offlineSigning bool) error {
	if err := rss.to(rssToPayEvent); err != nil {
		return err
	}
	bc := make(chan []byte)
	payinReqChanMaps.Set(rss.ssId, bc)
	if offlineSigning {
		raw, err := proto.Marshal(result)
		if err != nil {
			return err
		}
		err = rss.saveOfflineSigning(&renterpb.OfflineSigning{
			Raw: raw,
		})
		if err != nil {
			return err
		}
	} else {
		go func() {
			if err := func() error {
				chanState := result.Result.BuyerChannelState
				payerPrivKey, err := rss.ctxParams.cfg.Identity.DecodePrivateKey("")
				if err != nil {
					return err
				}
				sig, err := crypto.Sign(payerPrivKey, chanState.Channel)
				if err != nil {
					return err
				}
				chanState.FromSignature = sig
				payinReq, err := ledger.NewPayinRequest(result.Result.PayinId, payerPrivKey.GetPublic(), chanState)
				if err != nil {
					return err
				}
				payinSig, err := crypto.Sign(payerPrivKey, payinReq)
				if err != nil {
					return err
				}
				request := ledger.NewSignedPayinRequest(payinReq, payinSig)
				bs, err := proto.Marshal(request)
				if err != nil {
					return err
				}
				bc <- bs
				return nil
			}(); err != nil {
				_ = rss.to(rssErrorStatus, err)
				return
			}
		}()
	}
	signed := <-bc
	payinReqChanMaps.Remove(rss.ssId)
	signedPayInRequest := new(escrowpb.SignedPayinRequest)
	err := proto.Unmarshal(signed, signedPayInRequest)
	if err != nil {
		return err
	}
	if err := rss.to(rssToPayPayinRequestSignedEvent); err != nil {
		return err
	}
	payinRes, err := payInToEscrow(rss.ctx, rss.ctxParams.cfg, signedPayInRequest)
	if err != nil {
		return err
	}
	return doGuard(rss, payinRes, fileSize, offlineSigning)
}

func payInToEscrow(ctx context.Context, configuration *config.Config, signedPayinReq *escrowpb.SignedPayinRequest) (*escrowpb.SignedPayinResult, error) {
	var signedPayinRes *escrowpb.SignedPayinResult
	err := grpc.EscrowClient(configuration.Services.EscrowDomain).WithContext(ctx,
		func(ctx context.Context, client escrowpb.EscrowServiceClient) error {
			res, err := client.PayIn(ctx, signedPayinReq)
			if err != nil {
				log.Error(err)
				return err
			}
			err = VerifyEscrowRes(configuration, res.Result, res.EscrowSignature)
			if err != nil {
				log.Error(err)
				return err
			}
			signedPayinRes = res
			return nil
		})
	if err != nil {
		return nil, err
	}
	return signedPayinRes, nil
}
