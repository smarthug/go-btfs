package upload

import (
	"errors"
	"time"

	"github.com/TRON-US/go-btfs/core/commands/storage/upload/helper"
	"github.com/TRON-US/go-btfs/core/commands/storage/upload/sessions"
	"github.com/TRON-US/go-btfs/core/corehttp/remote"

	"github.com/cenkalti/backoff/v3"
	"github.com/libp2p/go-libp2p-core/peer"
)

func UploadShard(rss *sessions.RenterSession, hp *helper.HostsProvider, price int64, shardSize int64,
	storageLength int,
	offlineSigning bool, renterId peer.ID, fileSize int64, shardIndexes []int, rp *RepairParams) {
	for index, shardHash := range rss.ShardHashes {
		go func(i int, h string) {
			err := backoff.Retry(func() error {
				select {
				case <-rss.Ctx.Done():
					return nil
				default:
					break
				}
				host, err := hp.NextValidHost(price)
				if err != nil {
					_ = rss.To(sessions.RssErrorStatus, err)
					return nil
				}
				contractId := helper.NewContractID(rss.SsId)
				cb := make(chan error)
				ShardErrChanMap.Set(contractId, cb)
				tp := helper.TotalPay(shardSize, price, storageLength)
				var escrowCotractBytes []byte
				errChan := make(chan error, 2)
				go func() {
					tmp := func() error {
						escrowCotractBytes, err = renterSignEscrowContract(rss, h, i, host, tp, offlineSigning,
							renterId, contractId)
						if err != nil {
							log.Errorf("shard %s signs escrow_contract error: %s", h, err.Error())
							return err
						}
						return nil
					}()
					errChan <- tmp
				}()
				var guardContractBytes []byte
				go func() {
					tmp := func() error {
						guardContractBytes, err = RenterSignGuardContract(rss, &ContractParams{
							ContractId:    contractId,
							RenterPid:     renterId.Pretty(),
							HostPid:       host,
							ShardIndex:    int32(i),
							ShardHash:     h,
							ShardSize:     shardSize,
							FileHash:      rss.Hash,
							StartTime:     time.Now(),
							StorageLength: int64(storageLength),
							Price:         price,
							TotalPay:      tp,
						}, offlineSigning, rp)
						if err != nil {
							log.Errorf("shard %s signs guard_contract error: %s", h, err.Error())
							return err
						}
						return nil
					}()
					errChan <- tmp
				}()
				c := 0
				for err := range errChan {
					c++
					if err != nil {
						return err
					}
					if c == 2 {
						break
					}
				}

				hostPid, err := peer.IDB58Decode(host)
				if err != nil {
					log.Errorf("shard %s decodes host_pid error: %s", h, err.Error())
					return err
				}
				go func() {
					_, err := remote.P2PCall(rss.CtxParams.Ctx, rss.CtxParams.N, hostPid, "/storage/upload/init",
						rss.SsId,
						rss.Hash,
						h,
						price,
						escrowCotractBytes,
						guardContractBytes,
						storageLength,
						shardSize,
						i,
						renterId,
					)
					if err != nil {
						switch err.(type) {
						case remote.IoError:
							// NOP
							log.Error("io error", err)
						case remote.BusinessError:
							log.Error("write remote.BusinessError", h, err)
							cb <- err
						default:
							log.Error("write default err", h, err)
							cb <- err
						}
					}
				}()
				// host needs to send recv in 30 seconds, or the contract will be invalid.
				tick := time.Tick(30 * time.Second)
				select {
				case err = <-cb:
					ShardErrChanMap.Remove(contractId)
					return err
				case <-tick:
					return errors.New("host timeout")
				}
			}, helper.HandleShardBo)
			if err != nil {
				_ = rss.To(sessions.RssErrorStatus, err)
			}
		}(shardIndexes[index], shardHash)
	}
	// waiting for contracts of 30(n) shards
	go func(rss *sessions.RenterSession, numShards int) {
		tick := time.Tick(5 * time.Second)
		for true {
			select {
			case <-tick:
				completeNum, errorNum, err := rss.GetCompleteShardsNum()
				if err != nil {
					continue
				}
				log.Info("session", rss.SsId, "contractNum", completeNum, "errorNum", errorNum)
				if completeNum == numShards {
					err := Submit(rss, fileSize, offlineSigning)
					if err != nil {
						_ = rss.To(sessions.RssErrorStatus, err)
					}
					return
				} else if errorNum > 0 {
					_ = rss.To(sessions.RssErrorStatus, errors.New("there are some error shards"))
					log.Error("session:", rss.SsId, ",errorNum:", errorNum)
					return
				}
			case <-rss.Ctx.Done():
				log.Infof("session %s done", rss.SsId)
				return
			}
		}
	}(rss, len(rss.ShardHashes))
}
