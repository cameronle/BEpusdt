package task

import (
	"context"
	"os"
	"time"

	"github.com/smallnest/chanx"
	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/utils"
)

func bscInit() {
	if os.Getenv("BEPUSDT_EVM_SCANNER_V2") == "1" {
		registerFastScanner(conf.Bsc, 56, model.UsdtBep20, 15)
		return
	}
	ctx := context.Background()
	bsc := evm{
		Network: conf.Bsc,
		Block: block{
			ConfirmedOffset: 15,
		},
		Native: evmNative{
			Parse:     true,
			Decimal:   conf.BscBnbDecimals,
			TradeType: model.BscBnb,
		},
		Client:         utils.NewHttpClient(),
		blockScanQueue: chanx.NewUnboundedChan[evmBlock](ctx, 30),
	}

	if replay, ok, err := parseEVMReplayBlock(os.Getenv("BEPUSDT_REPLAY_BSC_BLOCK")); err != nil {
		log.Task.Warn(err)
	} else if ok {
		bsc.blockScanQueue.In <- replay
	}
	Register(Task{Callback: bsc.blockDispatch})
	Register(Task{Callback: bsc.syncBlocksForward, Duration: time.Second * 5})
	Register(Task{Callback: bsc.tradeConfirmHandle, Duration: time.Second * 5})
	Register(Task{Callback: bsc.lookbackBlocks, Duration: time.Second * 15})
}
