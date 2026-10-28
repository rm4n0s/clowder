package calls

import (
	"sync"

	"github.com/pion/rtp"
)

// rewriter keeps the sequence numbers and the timestamps of the packets that
// go into one local track continuous when the source changes (SPEC §7.18: a
// screen share replaces the camera, and the SSRC of the sender may change).
// The local track already rewrites the SSRC and the payload type. It is safe
// for concurrent use: the forwarders of an old and a new source may overlap.
type rewriter struct {
	// step is the timestamp gap that a switch of source puts between the last
	// packet of the old one and the first of the new one: one frame.
	step uint32

	mu      sync.Mutex
	started bool
	ssrc    uint32
	seqOff  uint16
	tsOff   uint32
	// lastSeq and lastTS are those of the last packet as it left, lastInSeq
	// is the sequence number it had when it came in.
	lastSeq   uint16
	lastTS    uint32
	lastInSeq uint16
}

// maxSequenceJump is the largest step of the sequence numbers of one source
// that counts as the same stream. SRTP tells the order of packets by their
// sequence numbers and breaks when they jump by more than half the range, so
// a source that jumps (a track replaced by one with its own sequencer) is
// treated like a new source.
const maxSequenceJump = 3000

// newRewriter makes a rewriter whose switch leaves a gap of step ticks of the
// clock of the track.
func newRewriter(step uint32) *rewriter {
	return &rewriter{step: step}
}

// rewrite changes the packet in place so that it follows the packets before
// it. It reports true when the packet is the first of a source other than the
// one before, so that the caller asks the new source for a key frame.
func (r *rewriter) rewrite(p *rtp.Packet) (switched bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started {
		r.started = true
		r.ssrc = p.SSRC
		r.lastSeq, r.lastTS, r.lastInSeq = p.SequenceNumber, p.Timestamp, p.SequenceNumber
		return true
	}
	jump := int16(p.SequenceNumber - (r.lastInSeq + 1))
	if p.SSRC != r.ssrc || jump > maxSequenceJump || jump < -maxSequenceJump {
		switched = true
		r.ssrc = p.SSRC
		r.seqOff = r.lastSeq + 1 - p.SequenceNumber
		r.tsOff = r.lastTS + r.step - p.Timestamp
	}
	r.lastInSeq = p.SequenceNumber
	p.SequenceNumber += r.seqOff
	p.Timestamp += r.tsOff
	r.lastSeq, r.lastTS = p.SequenceNumber, p.Timestamp
	return switched
}
