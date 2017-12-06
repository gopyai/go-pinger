package pinger_test

import (
	"fmt"
	"net"
	"time"

	"github.com/gopyai/go-pinger"
)

func Example() {
	// NOTICE: This code requires super user privilege to run
	isErr(pinger.Start())
	isErr(pinger.Start()) // restart / reconnect
	pinger.Stop()

	isErr(pinger.Start())
	defer pinger.Stop()

	pingList := []net.IP{
		net.IPv4(192, 168, 43, 87),
		net.IPv4(172, 217, 24, 4),
		net.IPv4(31, 13, 78, 35),
		net.IPv4(127, 0, 0, 1),
	}
	rst, err := pinger.Ping(pingList, 10, time.Second, time.Second*10)
	isErr(err)

	for idx, r := range rst {
		fmt.Printf("ping %v success %.1f%% with latency %.2f ms\n", pingList[idx], r.ReplyRatio*100, r.LatencyAverage*1000)
	}
}

func isErr(err error) {
	if err != nil {
		panic(err)
	}
}
