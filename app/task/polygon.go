package task

import (
	"context"
	"github.com/v03413/bepusdt/app/model"
	"os"
	"time"

	"github.com/smallnest/chanx"
	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/utils"
)

func polygonInit() {
	if os.Getenv("BEPUSDT_EVM_SCANNER_V2") == "1" {
		registerFastScanner(conf.Polygon, 137, model.UsdtPolygon, 40)
		return
	}
	ctx := context.Background()
	pol := evm{
		Network: conf.Polygon,
		Block: block{
			ConfirmedOffset: 40,
		},
		Client:         utils.NewHttpClient(),
		blockScanQueue: chanx.NewUnboundedChan[evmBlock](ctx, 30),
	}

	Register(Task{Callback: pol.blockDispatch})
	Register(Task{Callback: pol.syncBlocksForward, Duration: time.Second * 5})
	Register(Task{Callback: pol.tradeConfirmHandle, Duration: time.Second * 5})
	Register(Task{Callback: pol.lookbackBlocks, Duration: time.Second * 15})
}
