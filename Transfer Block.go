/*
File Name:  Transfer Block.go
Copyright:  2021 Peernet s.r.o.
Author:     Peter Kleissner
*/

package core

import (
	"errors"
	"net"
	"time"

	"github.com/PeernetOfficial/core/blockchain"
	"github.com/PeernetOfficial/core/btcec"
	"github.com/PeernetOfficial/core/protocol"
	"github.com/PeernetOfficial/core/udt"
)

// blockSequenceTimeout is the timeout for a follow-up message to appear, otherwise the transfer will be terminated.
var blockSequenceTimeout = time.Second * 10

// startBlockTransfer starts the transfer of blocks. Currently it only serves the user's blockchain.
func (peer *PeerInfo) startBlockTransfer(BlockchainPublicKey *btcec.PublicKey, LimitBlockCount uint64, MaxBlockSize uint64, TargetBlocks []protocol.BlockRange, sequenceNumber uint32) (err error) {
	virtualConn := newVirtualPacketConn(peer, func(data []byte, sequenceNumber uint32) {
		peer.sendGetBlock(data, protocol.GetBlockControlActive, BlockchainPublicKey, 0, 0, nil, sequenceNumber)
	})

	// register the sequence since packets are sent bi-directional
	virtualConn.sequenceNumber = sequenceNumber
	peer.Backend.networks.Sequences.RegisterSequenceBi(peer.PublicKey, sequenceNumber, virtualConn, blockSequenceTimeout, virtualConn.sequenceTerminate)

	udtConfig := udt.DefaultConfig()
	udtConfig.MaxPacketSize = protocol.TransferMaxEmbedSize
	udtConfig.MaxFlowWinSize = maxFlowWinSize

	// start UDT sender
	// Set streaming to true, otherwise udtSocket.Read returns the error "Message truncated" in case the reader has a smaller buffer.
	udtConn, err := udt.DialUDT(udtConfig, virtualConn, virtualConn.incomingData, virtualConn.outgoingData, virtualConn.terminationSignal, true)
	if err != nil {
		return err
	}

	defer udtConn.Close()

	// loop through the requested TargetBlocks range.
	sentBlocks := uint64(0)

	for _, target := range TargetBlocks {
		for blockN := target.Offset; blockN < target.Offset+target.Limit; blockN++ {
			blockData, status, err := peer.Backend.UserBlockchain.GetBlockRaw(blockN)
			if err != nil {
				protocol.BlockTransferWriteHeader(udtConn, protocol.GetBlockStatusNotAvailable, protocol.BlockRange{Offset: blockN, Limit: 1}, 0)
				continue
			}
			blockSize := uint64(len(blockData))

			if status != blockchain.StatusOK {
				protocol.BlockTransferWriteHeader(udtConn, protocol.GetBlockStatusNotAvailable, protocol.BlockRange{Offset: blockN, Limit: 1}, 0)
				continue
			} else if blockSize > MaxBlockSize {
				protocol.BlockTransferWriteHeader(udtConn, protocol.GetBlockStatusSizeExceed, protocol.BlockRange{Offset: blockN, Limit: 1}, blockSize)
				continue
			}

			protocol.BlockTransferWriteHeader(udtConn, protocol.GetBlockStatusAvailable, protocol.BlockRange{Offset: blockN, Limit: 1}, blockSize)
			udtConn.Write(blockData)

			sentBlocks++
			if sentBlocks >= LimitBlockCount {
				break
			}
		}
	}

	return err
}

// BlockTransferRequest requests blocks from the peer.
// The caller must call udtConn.Close() when done. Do not use any of the closing functions of virtualConn.
func (peer *PeerInfo) BlockTransferRequest(BlockchainPublicKey *btcec.PublicKey, LimitBlockCount uint64, MaxBlockSize uint64, TargetBlocks []protocol.BlockRange) (udtConn net.Conn, virtualConn *virtualPacketConn, err error) {
	virtualConn = newVirtualPacketConn(peer, func(data []byte, sequenceNumber uint32) {
		peer.sendGetBlock(data, protocol.GetBlockControlActive, BlockchainPublicKey, 0, 0, nil, sequenceNumber)
	})

	// new sequence
	sequence := peer.Backend.networks.Sequences.NewSequenceBi(peer.PublicKey, &peer.messageSequence, virtualConn, blockSequenceTimeout, virtualConn.sequenceTerminate)
	if sequence == nil {
		return nil, nil, errors.New("cannot acquire sequence")
	}
	virtualConn.sequenceNumber = sequence.SequenceNumber

	udtConfig := udt.DefaultConfig()
	udtConfig.MaxPacketSize = protocol.TransferMaxEmbedSize
	udtConfig.MaxFlowWinSize = maxFlowWinSize

	// start UDT receiver
	udtListener := udt.ListenUDT(udtConfig, virtualConn, virtualConn.incomingData, virtualConn.outgoingData, virtualConn.terminationSignal)

	// request block transfer
	err = peer.sendGetBlock(nil, protocol.GetBlockControlRequestStart, BlockchainPublicKey, LimitBlockCount, MaxBlockSize, TargetBlocks, virtualConn.sequenceNumber)
	if err != nil {
		udtListener.Close()
		return nil, nil, err
	}

	// accept the connection
	udtConn, err = udtListener.Accept() // TODO: Add timeout!
	if err != nil {
		udtListener.Close()
		return nil, nil, err
	}

	// We do not close the UDT listener here. It should automatically close after udtConn is closed.

	return udtConn, virtualConn, nil
}

// Downloads the requested blocks for the selected blockchain from the remote peer. The callback is called for each result.
func (peer *PeerInfo) BlockDownload(BlockchainPublicKey *btcec.PublicKey, LimitBlockCount, MaxBlockSize uint64, TargetBlocks []protocol.BlockRange, callback func(data []byte, targetBlock protocol.BlockRange, blockSize uint64, availability uint8)) (err error) {
	conn, _, err := peer.BlockTransferRequest(BlockchainPublicKey, LimitBlockCount, MaxBlockSize, TargetBlocks)
	if err != nil {
		return err
	}
	defer conn.Close()

	var limit uint64
	for _, target := range TargetBlocks {
		limit += target.Limit
	}

	for n := uint64(0); n < limit; {
		data, targetBlock, blockSize, availability, err := protocol.BlockTransferReadBlock(conn, MaxBlockSize)
		if err != nil {
			return err
		} else if !isTargetInRange(TargetBlocks, targetBlock.Offset, targetBlock.Limit) {
			return errors.New("invalid returned block range")
		}

		// TODO: Check if the block was already returned in case the block is available. This can be done via simple map.

		callback(data, targetBlock, blockSize, availability)

		n += targetBlock.Limit
	}

	return nil
}

func isTargetInRange(targets []protocol.BlockRange, offset, limit uint64) (valid bool) {
	for _, target := range targets {
		if offset >= target.Offset && offset+limit <= target.Offset+target.Limit {
			return true
		}
	}

	return false
}
