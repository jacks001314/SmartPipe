package lanxin

import (
	"bytes"
	"io"
	"net/url"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

func queryEncode(q url.Values) string {
	if q == nil {
		return ""
	}
	return q.Encode()
}
