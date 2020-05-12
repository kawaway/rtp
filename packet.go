package rtp

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Extensions RTP Header extensions
type Extensions map[uint8][]byte

// TODO(@kixelated) Remove Header.PayloadOffset and Packet.Raw

// Header represents an RTP packet header
// NOTE: PayloadOffset is populated by Marshal/Unmarshal and should not be modified
type Header struct {
	Version          uint8
	Padding          bool
	Extension        bool
	Marker           bool
	PayloadOffset    int
	PayloadType      uint8
	SequenceNumber   uint16
	Timestamp        uint32
	SSRC             uint32
	CSRC             []uint32
	ExtensionProfile uint16
	Extensions       Extensions
}

// Packet represents an RTP Packet
// NOTE: Raw is populated by Marshal/Unmarshal and should not be modified
type Packet struct {
	Header
	Raw     []byte
	Payload []byte
}

const (
	headerLength            = 4
	versionShift            = 6
	versionMask             = 0x3
	paddingShift            = 5
	paddingMask             = 0x1
	extensionShift          = 4
	extensionMask           = 0x1
	extensionProfileOneByte = 0xBEDE
	extensionProfileTwoByte = 0x1000
	extensionIDReserved     = 0xF
	ccMask                  = 0xF
	markerShift             = 7
	markerMask              = 0x1
	ptMask                  = 0x7F
	seqNumOffset            = 2
	seqNumLength            = 2
	timestampOffset         = 4
	timestampLength         = 4
	ssrcOffset              = 8
	ssrcLength              = 4
	csrcOffset              = 12
	csrcLength              = 4
)

// String helps with debugging by printing packet information in a readable way
func (p Packet) String() string {
	out := "RTP PACKET:\n"

	out += fmt.Sprintf("\tVersion: %v\n", p.Version)
	out += fmt.Sprintf("\tMarker: %v\n", p.Marker)
	out += fmt.Sprintf("\tPayload Type: %d\n", p.PayloadType)
	out += fmt.Sprintf("\tSequence Number: %d\n", p.SequenceNumber)
	out += fmt.Sprintf("\tTimestamp: %d\n", p.Timestamp)
	out += fmt.Sprintf("\tSSRC: %d (%x)\n", p.SSRC, p.SSRC)
	out += fmt.Sprintf("\tPayload Length: %d\n", len(p.Payload))

	return out
}

// Unmarshal parses the passed byte slice and stores the result in the Header this method is called upon
func (h *Header) Unmarshal(rawPacket []byte) error {
	if len(rawPacket) < headerLength {
		return fmt.Errorf("RTP header size insufficient; %d < %d", len(rawPacket), headerLength)
	}

	/*
	 *  0                   1                   2                   3
	 *  0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
	 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	 * |V=2|P|X|  CC   |M|     PT      |       sequence number         |
	 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	 * |                           timestamp                           |
	 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	 * |           synchronization source (SSRC) identifier            |
	 * +=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+
	 * |            contributing source (CSRC) identifiers             |
	 * |                             ....                              |
	 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	 */

	h.Version = rawPacket[0] >> versionShift & versionMask
	h.Padding = (rawPacket[0] >> paddingShift & paddingMask) > 0
	h.Extension = (rawPacket[0] >> extensionShift & extensionMask) > 0
	h.CSRC = make([]uint32, rawPacket[0]&ccMask)

	h.Marker = (rawPacket[1] >> markerShift & markerMask) > 0
	h.PayloadType = rawPacket[1] & ptMask

	h.SequenceNumber = binary.BigEndian.Uint16(rawPacket[seqNumOffset : seqNumOffset+seqNumLength])
	h.Timestamp = binary.BigEndian.Uint32(rawPacket[timestampOffset : timestampOffset+timestampLength])
	h.SSRC = binary.BigEndian.Uint32(rawPacket[ssrcOffset : ssrcOffset+ssrcLength])

	currOffset := csrcOffset + (len(h.CSRC) * csrcLength)
	if len(rawPacket) < currOffset {
		return fmt.Errorf("RTP header size insufficient; %d < %d", len(rawPacket), currOffset)
	}

	for i := range h.CSRC {
		offset := csrcOffset + (i * csrcLength)
		h.CSRC[i] = binary.BigEndian.Uint32(rawPacket[offset:])
	}

	if h.Extension {
		if len(rawPacket) < currOffset+4 {
			return fmt.Errorf("RTP header size insufficient for extension; %d < %d", len(rawPacket), currOffset)
		}

		h.Extensions = make(map[uint8][]byte)

		h.ExtensionProfile = binary.BigEndian.Uint16(rawPacket[currOffset:])
		currOffset += 2

		switch h.ExtensionProfile {
		// RFC 8285 RTP One Byte Header Extension
		case extensionProfileOneByte:
			extensionLength := int(binary.BigEndian.Uint16(rawPacket[currOffset:]))
			currOffset += 2

			i := 0
			for i < extensionLength {
				if rawPacket[currOffset] == 0x00 { // padding
					currOffset++
					continue
				}

				extid := rawPacket[currOffset] >> 4
				len := int(rawPacket[currOffset]&^0xF0 + 1)
				currOffset++

				if extid == extensionIDReserved {
					break
				}

				h.Extensions[extid] = rawPacket[currOffset : currOffset+len]
				currOffset += len
				i++
			}
		// RFC 8285 RTP Two Byte Header Extension
		case extensionProfileTwoByte:
			extensionLength := int(binary.BigEndian.Uint16(rawPacket[currOffset:]))
			currOffset += 2

			i := 0
			for i < extensionLength {
				if rawPacket[currOffset] == 0x00 { // padding
					currOffset++
					continue
				}

				extid := rawPacket[currOffset]
				currOffset++

				len := int(rawPacket[currOffset])
				currOffset++

				h.Extensions[extid] = rawPacket[currOffset : currOffset+len]
				currOffset += len

				i++
			}

		default: // RFC3550 Extension
			extensionLength := int(binary.BigEndian.Uint16(rawPacket[currOffset:])) * 4
			currOffset += 2

			if len(rawPacket) < currOffset+extensionLength {
				return fmt.Errorf("RTP header size insufficient for extension length; %d < %d", len(rawPacket), currOffset+extensionLength)
			}

			h.Extensions[0] = rawPacket[currOffset : currOffset+extensionLength]
			currOffset += len(h.Extensions[0])
		}
	}

	h.PayloadOffset = currOffset

	return nil
}

// Unmarshal parses the passed byte slice and stores the result in the Packet this method is called upon
func (p *Packet) Unmarshal(rawPacket []byte) error {
	if err := p.Header.Unmarshal(rawPacket); err != nil {
		return err
	}

	p.Payload = rawPacket[p.PayloadOffset:]
	p.Raw = rawPacket
	return nil
}

// Marshal serializes the header into bytes.
func (h *Header) Marshal() (buf []byte, err error) {
	buf = make([]byte, h.MarshalSize())

	n, err := h.MarshalTo(buf)
	if err != nil {
		return nil, err
	}

	return buf[:n], nil
}

// MarshalTo serializes the header and writes to the buffer.
func (h *Header) MarshalTo(buf []byte) (n int, err error) {
	/*
	 *  0                   1                   2                   3
	 *  0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
	 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	 * |V=2|P|X|  CC   |M|     PT      |       sequence number         |
	 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	 * |                           timestamp                           |
	 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	 * |           synchronization source (SSRC) identifier            |
	 * +=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+=+
	 * |            contributing source (CSRC) identifiers             |
	 * |                             ....                              |
	 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	 */

	size := h.MarshalSize()
	if size > len(buf) {
		return 0, io.ErrShortBuffer
	}

	// The first byte contains the version, padding bit, extension bit, and csrc size
	buf[0] = (h.Version << versionShift) | uint8(len(h.CSRC))
	if h.Padding {
		buf[0] |= 1 << paddingShift
	}

	if h.Extension {
		buf[0] |= 1 << extensionShift
	}

	// The second byte contains the marker bit and payload type.
	buf[1] = h.PayloadType
	if h.Marker {
		buf[1] |= 1 << markerShift
	}

	binary.BigEndian.PutUint16(buf[2:4], h.SequenceNumber)
	binary.BigEndian.PutUint32(buf[4:8], h.Timestamp)
	binary.BigEndian.PutUint32(buf[8:12], h.SSRC)

	n = 12
	for _, csrc := range h.CSRC {
		binary.BigEndian.PutUint32(buf[n:n+4], csrc)
		n += 4
	}

	// Calculate the size of the header by seeing how many bytes we're written.
	// TODO This is a BUG but fixing it causes more issues.
	h.PayloadOffset = n

	if h.Extension {
		binary.BigEndian.PutUint16(buf[n+0:n+2], h.ExtensionProfile)
		n += 2

		switch h.ExtensionProfile {
		// RFC 8285 RTP One Byte Header Extension
		case extensionProfileOneByte:
			extSize := uint16(len(h.Extensions))
			binary.BigEndian.PutUint16(buf[n:n+2], extSize)
			n += 2

			for extid, payload := range h.Extensions {
				buf[n] = extid<<4 | (uint8(len(payload)) - 1)
				n++
				n += copy(buf[n:], payload)
			}
		// RFC 8285 RTP Two Byte Header Extension
		case extensionProfileTwoByte:
			extSize := uint16(len(h.Extensions))
			binary.BigEndian.PutUint16(buf[n:n+2], extSize)
			n += 2

			for extid, payload := range h.Extensions {
				buf[n] = extid
				n++
				buf[n] = uint8(len(payload))
				n++
				n += copy(buf[n:], payload)
			}
		default: // RFC3550 Extension
			if len(h.Extensions[0])%4 != 0 {
				//the payload must be in 32-bit words.
				return 0, io.ErrShortBuffer
			}
			extSize := uint16(len(h.Extensions[0]) / 4)
			binary.BigEndian.PutUint16(buf[n:n+2], extSize)
			n += 2
			n += copy(buf[n:], h.Extensions[0])
		}
	}

	return n, nil
}

// MarshalSize returns the size of the header once marshaled.
func (h *Header) MarshalSize() int {
	// NOTE: Be careful to match the MarshalTo() method.
	size := 12 + (len(h.CSRC) * csrcLength)

	if h.Extension {
		size += 4

		switch h.ExtensionProfile {
		// RFC 8285 RTP One Byte Header Extension
		case extensionProfileOneByte:
			for _, payload := range h.Extensions {
				size += 1 + len(payload)
			}
		// RFC 8285 RTP Two Byte Header Extension
		case extensionProfileTwoByte:
			for _, payload := range h.Extensions {
				size += 2 + len(payload)
			}
		default:
			size += len(h.Extensions[0])
		}
	}
	return size
}

// SetExtension sets an RTP header extension
func (h *Header) SetExtension(id uint8, payload []byte) error {
	if h.Extension {
		switch h.ExtensionProfile {
		// RFC 8285 RTP One Byte Header Extension
		case extensionProfileOneByte:
			if id < 1 || id > 14 {
				return fmt.Errorf("header extension id must be between 1 and 14 for RFC 5285 extensions")
			}
			if len(payload) > 16 {
				return fmt.Errorf("header extension payload must be 16bytes or less for RFC 5285 one byte extensions")
			}
			h.Extensions[id] = payload
			return nil
		// RFC 8285 RTP Two Byte Header Extension
		case extensionProfileTwoByte:
			if id < 1 || id > 255 {
				return fmt.Errorf("header extension id must be between 1 and 14 for RFC 5285 extensions")
			}
			if len(payload) > 256 {
				return fmt.Errorf("header extension payload must be 256bytes or less for RFC 5285 two byte extensions")
			}
			h.Extensions[id] = payload
			return nil
		default: // RFC3550 Extension
			if id != 0 {
				return fmt.Errorf("header extension id must be 0 for none RFC 5285 extensions")
			}
			h.Extensions[id] = payload
			return nil
		}
	}
	// No existing header extensions
	h.Extension = true

	switch len := len(payload); {
	case len < 16:
		h.ExtensionProfile = extensionProfileOneByte
	case len > 16 && len < 256:
		h.ExtensionProfile = extensionProfileTwoByte
	}

	h.Extensions[id] = payload
	return nil
}

// GetExtension returns an RTP header extension
func (h *Header) GetExtension(id uint8) []byte {
	return h.Extensions[id]
}

// Marshal serializes the packet into bytes.
func (p *Packet) Marshal() (buf []byte, err error) {
	buf = make([]byte, p.MarshalSize())

	n, err := p.MarshalTo(buf)
	if err != nil {
		return nil, err
	}

	return buf[:n], nil
}

// MarshalTo serializes the packet and writes to the buffer.
func (p *Packet) MarshalTo(buf []byte) (n int, err error) {
	n, err = p.Header.MarshalTo(buf)
	if err != nil {
		return 0, err
	}

	// Make sure the buffer is large enough to hold the packet.
	if n+len(p.Payload) > len(buf) {
		return 0, io.ErrShortBuffer
	}

	m := copy(buf[n:], p.Payload)
	p.Raw = buf[:n+m]

	return n + m, nil
}

// MarshalSize returns the size of the packet once marshaled.
func (p *Packet) MarshalSize() int {
	return p.Header.MarshalSize() + len(p.Payload)
}
