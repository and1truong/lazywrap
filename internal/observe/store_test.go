package observe

import (
	"fmt"
	"log/slog"
	"sync"
	"testing"
)

func TestBoundedSeparateConcurrentStreams(t *testing.T) {
	s:=New(); logger:=slog.New(&Handler{Store:s}).With("service","api")
	var wg sync.WaitGroup
	for i:=0;i<4;i++ { wg.Add(1); go func(){ defer wg.Done(); for j:=0;j<Capacity;j++ { logger.Info(fmt.Sprint(j),"stream","stdout") } }() }
	wg.Wait(); logger.Info("ready","port",1234)
	logs:=s.Entries("api",false); events:=s.Entries("api",true)
	if len(logs)!=Capacity || len(events)!=1 || events[0].Stream!="" { t.Fatalf("logs=%d events=%+v",len(logs),events) }
	logs[0].Text="mutated"
	if s.Entries("api",false)[0].Text=="mutated" { t.Fatal("snapshot aliases mutable storage") }
}
