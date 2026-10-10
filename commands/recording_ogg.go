package commands

import (
	"encoding/binary"
	"io"
)

// An Ogg Opus stream (RFC 7845) carrying Discord's packets as they arrived,
// never re-encoded: one track of a recording.

// opusSampleRate is the rate every Opus granule position counts in.
const opusSampleRate = 48000

// oggMaxSegments is the most lacing values one Ogg page holds (RFC 3533).
const oggMaxSegments = 255

// oggWriter writes one Ogg Opus stream. Packets gather into a page until the
// page is full or flush writes it out, so a page holds whole packets only.
type oggWriter struct {
	w       io.Writer
	serial  uint32
	pageSeq uint32
	// granule is the stream's length so far, in 48 kHz samples: the end of
	// the last packet written.
	granule uint64

	// The page being filled.
	lacing []byte
	body   []byte
}

// newOggWriter starts a stream on w with its two header pages: the
// identification header, alone on the first page as RFC 7845 requires, and
// the comment header.
func newOggWriter(w io.Writer, serial uint32) (*oggWriter, error) {
	o := &oggWriter{w: w, serial: serial}
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8] = 1 // version
	head[9] = 2 // channels: Discord sends stereo
	binary.LittleEndian.PutUint32(head[12:], opusSampleRate)
	if err := o.page([]byte{19}, head, 0, oggBOS); err != nil {
		return nil, err
	}
	const vendor = "cavbot2"
	tags := make([]byte, 0, 16+len(vendor))
	tags = append(tags, "OpusTags"...)
	tags = binary.LittleEndian.AppendUint32(tags, uint32(len(vendor)))
	tags = append(tags, vendor...)
	tags = binary.LittleEndian.AppendUint32(tags, 0) // no comments
	if err := o.page(lacingOf(len(tags)), tags, 0, 0); err != nil {
		return nil, err
	}
	return o, nil
}

// The page header type flags.
const (
	oggBOS = 0x02 // beginning of stream
	oggEOS = 0x04 // end of stream
)

// writePacket adds a packet samples long to the stream. It goes out with
// the page it lands on.
func (o *oggWriter) writePacket(pkt []byte, samples int) error {
	lacing := lacingOf(len(pkt))
	if len(o.lacing)+len(lacing) > oggMaxSegments {
		if err := o.flush(); err != nil {
			return err
		}
	}
	o.lacing = append(o.lacing, lacing...)
	o.body = append(o.body, pkt...)
	o.granule += uint64(samples)
	return nil
}

// flush writes out the page being filled, if it holds any packet.
func (o *oggWriter) flush() error {
	if len(o.lacing) == 0 {
		return nil
	}
	return o.emit(0)
}

// close writes out the last page, marked as the end of the stream. The
// stream holds at least one packet by then.
func (o *oggWriter) close() error {
	return o.emit(oggEOS)
}

// emit writes the page being filled with the flags given and starts a new
// one.
func (o *oggWriter) emit(flags byte) error {
	err := o.page(o.lacing, o.body, o.granule, flags)
	o.lacing, o.body = o.lacing[:0], o.body[:0]
	return err
}

// page writes one page.
func (o *oggWriter) page(lacing, body []byte, granule uint64, flags byte) error {
	hdr := make([]byte, 27+len(lacing))
	copy(hdr, "OggS")
	hdr[5] = flags
	binary.LittleEndian.PutUint64(hdr[6:], granule)
	binary.LittleEndian.PutUint32(hdr[14:], o.serial)
	binary.LittleEndian.PutUint32(hdr[18:], o.pageSeq)
	hdr[26] = byte(len(lacing))
	copy(hdr[27:], lacing)
	binary.LittleEndian.PutUint32(hdr[22:], oggCRC(oggCRC(0, hdr), body))
	o.pageSeq++
	if _, err := o.w.Write(hdr); err != nil {
		return err
	}
	_, err := o.w.Write(body)
	return err
}

// lacingOf is a packet's lacing values: a 255 for every full 255 bytes, then
// the rest, which ends the packet.
func lacingOf(size int) []byte {
	lacing := make([]byte, 0, size/255+1)
	for ; size >= 255; size -= 255 {
		lacing = append(lacing, 255)
	}
	return append(lacing, byte(size))
}

// oggCRCTable is Ogg's CRC-32: polynomial 0x04c11db7, not reflected, zero
// initial value.
var oggCRCTable = func() (t [256]uint32) {
	for i := range t {
		r := uint32(i) << 24
		for range 8 {
			if r&0x80000000 != 0 {
				r = r<<1 ^ 0x04c11db7
			} else {
				r <<= 1
			}
		}
		t[i] = r
	}
	return t
}()

func oggCRC(crc uint32, b []byte) uint32 {
	for _, c := range b {
		crc = crc<<8 ^ oggCRCTable[byte(crc>>24)^c]
	}
	return crc
}

// opusPacketSamples is an Opus packet's length in 48 kHz samples, read from
// its TOC byte (RFC 6716, section 3.1).
func opusPacketSamples(pkt []byte) int {
	if len(pkt) == 0 {
		return 0
	}
	config := pkt[0] >> 3
	var frame int
	switch {
	case config < 12:
		frame = []int{480, 960, 1920, 2880}[config&3]
	case config < 16:
		frame = []int{480, 960}[config&1]
	default:
		frame = []int{120, 240, 480, 960}[config&3]
	}
	switch pkt[0] & 3 {
	case 0:
		return frame
	case 1, 2:
		return 2 * frame
	default:
		if len(pkt) < 2 {
			return 0
		}
		return int(pkt[1]&0x3f) * frame
	}
}
