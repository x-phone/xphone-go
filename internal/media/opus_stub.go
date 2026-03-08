//go:build !opus

package media

func newOpusProcessor() CodecProcessor { return nil }
