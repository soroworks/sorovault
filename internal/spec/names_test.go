package spec

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
)

// TestDataFormatName covers every defined event data-format value and the
// out-of-range fallback that a newer SDK may emit.
func TestDataFormatName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   xdr.ScSpecEventDataFormat
		want string
	}{
		{"single value", xdr.ScSpecEventDataFormatScSpecEventDataFormatSingleValue, "single-value"},
		{"vec", xdr.ScSpecEventDataFormatScSpecEventDataFormatVec, "vec"},
		{"map", xdr.ScSpecEventDataFormatScSpecEventDataFormatMap, "map"},
		{"out of range", xdr.ScSpecEventDataFormat(99), "unknown"},
		{"negative", xdr.ScSpecEventDataFormat(-1), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, dataFormatName(tt.in))
		})
	}
}

// TestParamLocationName covers every defined event parameter location value
// and the out-of-range fallback that a newer SDK may emit.
func TestParamLocationName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   xdr.ScSpecEventParamLocationV0
		want string
	}{
		{"data", xdr.ScSpecEventParamLocationV0ScSpecEventParamLocationData, "data"},
		{"topic list", xdr.ScSpecEventParamLocationV0ScSpecEventParamLocationTopicList, "topic-list"},
		{"out of range", xdr.ScSpecEventParamLocationV0(99), "unknown"},
		{"negative", xdr.ScSpecEventParamLocationV0(-1), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, paramLocationName(tt.in))
		})
	}
}
