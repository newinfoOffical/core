/*
File Name:  Command Traverse.go
Copyright:  2021 Peernet s.r.o.
Author:     Peter Kleissner
*/

package core

import (
	"math/rand"
	"time"
)

// cmdTraverseForward handles an incoming traverse message that should be forwarded to another peer
func (peer *PeerInfo) cmdTraverseForward(msg *MessageTraverse) {
	// Verify the signature. This makes sure that a fowarded message cannot be replayed by others.
	if !msg.SignerPublicKey.IsEqual(peer.PublicKey) || !msg.SignerPublicKey.IsEqual(msg.SenderPublicKey) {
		return
	}

	// Check expiration
	if msg.Expires.Before(time.Now()) {
		return
	}

	// Check if the target peer is known in the peer list. If not, nothing will be done.
	// The original sender should only send the Traverse message as answer to a Response that contains a reported peer that is behind a NAT.
	// In that case the target peer should be still in this peers' peer list.
	peerTarget := PeerlistLookup(msg.TargetPeer)
	if peerTarget == nil {
		return
	}

	// Get the right IP:Port of the original sender to share to the target peer.
	allowIPv4 := peerTarget.Features&(1<<FeatureIPv4Listen) > 0
	allowIPv6 := peerTarget.Features&(1<<FeatureIPv6Listen) > 0
	connectionIPv4 := peer.GetConnection2Share(false, allowIPv4, false)
	connectionIPv6 := peer.GetConnection2Share(false, false, allowIPv6)

	if connectionIPv4 == nil && connectionIPv6 == nil {
		return
	}

	if err := msgEncodeTraverseSetAddress(msg.Payload, connectionIPv4, connectionIPv6); err != nil {
		return
	}

	peerTarget.send(&PacketRaw{Command: CommandTraverse, Payload: msg.Payload})
}

func (peer *PeerInfo) cmdTraverseReceive(msg *MessageTraverse) {
	if msg.Expires.Before(time.Now()) {
		return
	}

	// Already an active connection established? The relayed message should not be needed in this case.
	// This could be changed in the future if it turns out that there are 1-way connection issues.
	if peerTarget := PeerlistLookup(msg.SignerPublicKey); peerTarget != nil {
		return
	}

	// parse IP addresses of the original sender
	var addresses []*peerAddress

	if !msg.IPv4.IsUnspecified() {
		port := msg.PortIPv4
		if msg.PortIPv4ReportedExternal > 0 {
			port = msg.PortIPv4ReportedExternal
		}
		addresses = append(addresses, &peerAddress{IP: msg.IPv4, Port: port, PortInternal: 0})
	}
	if !msg.IPv6.IsUnspecified() {
		port := msg.PortIPv6
		if msg.PortIPv6ReportedExternal > 0 {
			port = msg.PortIPv6ReportedExternal
		}
		addresses = append(addresses, &peerAddress{IP: msg.IPv6, Port: port, PortInternal: 0})
	}
	if len(addresses) == 0 {
		return
	}

	// ---- fork packetWorker to decode and validate embedded packet ---
	// Due to missing connection and other embedded details in the message (such as ports), the packet is not just simply queued to rawPacketsIncoming.
	decoded, senderPublicKey, err := PacketDecrypt(msg.EmbeddedPacketRaw, peerPublicKey)
	if err != nil {
		return
	}
	if !senderPublicKey.IsEqual(msg.SignerPublicKey) {
		return
	} else if senderPublicKey.IsEqual(peerPublicKey) {
		return
	} else if decoded.Protocol != 0 {
		return
	} else if decoded.Command != CommandAnnouncement {
		return
	}

	// process the packet and create a virtual peer
	raw := &MessageRaw{SenderPublicKey: senderPublicKey, PacketRaw: *decoded, connection: nil}
	peerV := &PeerInfo{PublicKey: senderPublicKey, connectionActive: nil, connectionLatest: nil, NodeID: publicKey2NodeID(senderPublicKey), messageSequence: rand.Uint32(), isVirtual: true, targetAddresses: addresses}

	// process it!
	switch decoded.Command {
	case CommandAnnouncement: // Announce
		if announce, _ := msgDecodeAnnouncement(raw); announce != nil {
			if len(announce.UserAgent) > 0 {
				peerV.UserAgent = announce.UserAgent
			}
			peerV.Features = announce.Features

			peerV.cmdAnouncement(announce)
		}

	default:
	}
}