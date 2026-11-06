// Package server transport 層裝配（wire）：HTTP/gRPC 雙協議 + 後台（relay/cron）+ worker。
//
// transport 只做參數校驗與裝配（鐵律 2）；模式分流（api/all/worker）見 cmd/zcard。
package server

import "github.com/google/wire"

// ProviderSet server providers（wire）。
var ProviderSet = wire.NewSet(NewHTTPServer, NewGRPCServer, NewBackgroundServer, NewWorkerServer)
