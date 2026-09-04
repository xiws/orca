package utils

import (
	"sync"
	"time"
)

const (
	// 2026-01-01 00:00:00 UTC
	epoch int64 = 1767225600000

	nodeBits  = 10
	seqBits   = 12
	nodeMask  = (1 << nodeBits) - 1
	seqMask   = (1 << seqBits) - 1
	nodeShift = seqBits
	timeShift = nodeBits + seqBits
)

type Generator struct {
	mu       sync.Mutex
	nodeID   int64
	lastTime int64
	sequence int64
}

func New(nodeID int64) *Generator {
	if nodeID < 0 || nodeID > nodeMask {
		panic("nodeID must be between 0 and 1023")
	}

	return &Generator{
		nodeID: nodeID,
	}
}

func (g *Generator) Next() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now().UnixMilli()

	// 时钟回拨
	if now < g.lastTime {
		now = g.lastTime
	}

	if now == g.lastTime {
		g.sequence = (g.sequence + 1) & seqMask

		// 同一毫秒超过 4096 个
		if g.sequence == 0 {
			for now <= g.lastTime {
				now = time.Now().UnixMilli()
			}
		}
	} else {
		g.sequence = 0
	}

	g.lastTime = now

	return ((now - epoch) << timeShift) |
		(g.nodeID << nodeShift) |
		g.sequence
}

var defaultSnowFlake = New(0)

func GetSnowFlakeId() int64 {
	return defaultSnowFlake.Next()
}
