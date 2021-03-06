package connection

import (
	"context"
	"fmt"
	"github.com/ihciah/rabbit-tcp/block"
	"github.com/ihciah/rabbit-tcp/logger"
	"go.uber.org/atomic"
	"io"
	"math/rand"
	"net"
	"time"
)

type InboundConnection struct {
	baseConnection
	dataBuffer ByteRingBuffer

	writeCtx context.Context
	readCtx  context.Context
}

func NewInboundConnection(sendQueue chan<- block.Block, ctx context.Context, removeFromPool context.CancelFunc) Connection {
	connectionID := rand.Uint32()
	c := InboundConnection{
		baseConnection: baseConnection{
			blockProcessor:   newBlockProcessor(ctx, removeFromPool),
			connectionID:     connectionID,
			closed:           atomic.NewBool(false),
			sendQueue:        sendQueue,
			recvQueue:        make(chan block.Block, RecvQueueSize),
			orderedRecvQueue: make(chan block.Block, OrderedRecvQueueSize),
			logger:           logger.NewLogger(fmt.Sprintf("[InboundConnection-%d]", connectionID)),
		},
		dataBuffer: NewByteRingBuffer(block.MaxSize),
		readCtx:    ctx,
		writeCtx:   ctx,
	}
	c.logger.Infof("InboundConnection %d created.\n", connectionID)
	return &c
}

func (c *InboundConnection) Read(b []byte) (n int, err error) {
	readN := 0

	if !c.dataBuffer.Empty() {
		// There's something left in buffer
		readN += c.dataBuffer.Read(b)
		if readN == len(b) {
			// if dst is full, return
			return readN, nil
		}
	}

	if c.closed.Load() {
		// Connection is closed, should read all data left in channel
		for {
			select {
			case blk := <-c.orderedRecvQueue:
				_ = c.readBlock(&blk, &readN, b)
				if readN == len(b) {
					return readN, nil
				}
			default:
				if readN != 0 {
					return readN, nil
				} else {
					return 0, io.EOF
				}
			}
		}
	}

	// Read at lease something
	if readN == 0 {
		select {
		case blk := <-c.orderedRecvQueue:
			c.logger.Debugln("Read in a block.")
			err := c.readBlock(&blk, &readN, b)
			if err == io.EOF || readN == len(b) {
				if readN != 0 {
					return readN, nil
				} else {
					return 0, err
				}
			}
		case <-c.readCtx.Done():
			c.logger.Infoln("ReadDeadline exceeded.")
			if readN != 0 {
				return readN, nil
			} else {
				return 0, io.EOF
			}
		}
	}

	if readN == 0 {
		c.logger.Errorln("Unknown error.")
	}

	for {
		select {
		case blk := <-c.orderedRecvQueue:
			err := c.readBlock(&blk, &readN, b)
			c.logger.Debugln("Read in a block.")
			if err == io.EOF || readN == len(b) {
				return readN, nil
			}
		case <-c.readCtx.Done():
			c.logger.Infoln("ReadDeadline exceeded.")
			return readN, nil
		default:
			return readN, nil
		}
	}
}

func (c *InboundConnection) readBlock(blk *block.Block, readN *int, b []byte) (err error) {
	switch blk.Type {
	case block.TypeDisconnect:
		c.closed.Store(true)
		return io.EOF
	case block.TypeData:
		dst := b[*readN:]
		if len(dst) < len(blk.BlockData) {
			// if dst can't put a block, put part of it and return
			c.dataBuffer.OverWrite(blk.BlockData)
			*readN += c.dataBuffer.Read(dst)
			return
		}
		// if dst can put a block, put it
		*readN += copy(dst, blk.BlockData)
	}
	return
}

func (c *InboundConnection) Write(b []byte) (n int, err error) {
	// TODO: tag all blocks from b using WaitGroup
	// TODO: and wait all blocks sent?
	c.sendData(b)
	return len(b), nil
}

func (c *InboundConnection) Close() error {
	if c.closed.CAS(false, true) {
		c.SendDisconnect()
	}
	return nil
}

func (c *InboundConnection) LocalAddr() net.Addr {
	// TODO
	return nil
}

func (c *InboundConnection) RemoteAddr() net.Addr {
	// TODO
	return nil
}

func (c *InboundConnection) SetDeadline(t time.Time) error {
	_ = c.SetReadDeadline(t)
	_ = c.SetWriteDeadline(t)
	return nil
}

func (c *InboundConnection) SetReadDeadline(t time.Time) error {
	c.readCtx, _ = context.WithDeadline(context.Background(), t)
	return nil
}

func (c *InboundConnection) SetWriteDeadline(t time.Time) error {
	c.writeCtx, _ = context.WithDeadline(context.Background(), t)
	return nil
}
