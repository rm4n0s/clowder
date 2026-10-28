package calls

import (
	"github.com/pion/sdp/v3"
	"github.com/rm4n0s/errors"
)

// limitVideoBandwidth adds the bandwidth lines to the video section of an SDP
// (SPEC D64): b=AS in kilobits per second and b=TIAS in bits per second, so
// that the sender of the other side keeps under what the server forwards. REMB
// says the same on the wire.
func limitVideoBandwidth(text string, bitsPerSecond uint64) (string, error) {
	var desc sdp.SessionDescription
	if err := desc.Unmarshal([]byte(text)); err != nil {
		return "", errors.NewErr(tagSDPInvalid, err)
	}
	for _, md := range desc.MediaDescriptions {
		if md.MediaName.Media != "video" {
			continue
		}
		md.Bandwidth = withoutBandwidth(md.Bandwidth, "AS", "TIAS")
		md.Bandwidth = append(md.Bandwidth,
			sdp.Bandwidth{Type: "AS", Bandwidth: bitsPerSecond / 1000},
			sdp.Bandwidth{Type: "TIAS", Bandwidth: bitsPerSecond})
	}
	out, err := desc.Marshal()
	if err != nil {
		return "", errors.NewErr(tagSDPInvalid, err)
	}
	return string(out), nil
}

// withoutBandwidth returns the lines without those of the given types.
func withoutBandwidth(lines []sdp.Bandwidth, types ...string) []sdp.Bandwidth {
	out := lines[:0]
	for _, b := range lines {
		keep := true
		for _, t := range types {
			if b.Type == t {
				keep = false
			}
		}
		if keep {
			out = append(out, b)
		}
	}
	return out
}

const tagSDPInvalid = "SDPInvalid"
