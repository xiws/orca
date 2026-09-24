// Package utils 提供通用工具函数集合。
package utils

import (
	"sync"
	"time"
)

const (
	// 2026-01-01 00:00:00 UTC 自定义纪元时间戳
	epoch int64 = 1767225600000

	// nodeBits 节点 ID 占用位数
	nodeBits = 10
	// seqBits 序列号占用位数
	seqBits = 12
	// nodeMask 节点 ID 掩码
	nodeMask = (1 << nodeBits) - 1
	// seqMask 序列号掩码
	seqMask = (1 << seqBits) - 1
	// nodeShift 节点 ID 左移位数
	nodeShift = seqBits
	// timeShift 时间戳左移位数
	timeShift = nodeBits + seqBits
)

// Generator 雪花算法 ID 生成器
type Generator struct {
	mu       sync.Mutex
	nodeID   int64
	lastTime int64
	sequence int64
}

// New 创建指定节点 ID 的雪花算法生成器，nodeID 范围为 0-1023
func New(nodeID int64) *Generator {
	if nodeID < 0 || nodeID > nodeMask {
		panic("nodeID must be between 0 and 1023")
	}

	return &Generator{
		nodeID: nodeID,
	}
}

// Next 生成下一个唯一 ID
func (g *Generator) Next() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now().UnixMilli()

	// 时钟回拨处理
	if now < g.lastTime {
		now = g.lastTime
	}

	if now == g.lastTime {
		// 同一毫秒内递增序列号
		g.sequence = (g.sequence + 1) & seqMask

		// 同一毫秒超过 4096 个，等待下一毫秒
		if g.sequence == 0 {
			for now <= g.lastTime {
				now = time.Now().UnixMilli()
			}
		}
	} else {
		// 新的毫秒，重置序列号
		g.sequence = 0
	}

	g.lastTime = now

	// 组合 ID：时间戳 | 节点 ID | 序列号
	return ((now - epoch) << timeShift) |
		(g.nodeID << nodeShift) |
		g.sequence
}

// defaultSnowFlake 默认的全局雪花算法生成器实例
var defaultSnowFlake = New(0)

// GetSnowFlakeId 使用默认生成器获取一个雪花算法 ID
func GetSnowFlakeId() int64 {
	return defaultSnowFlake.Next()
}
