package delivery

import (
	"encoding/base64"
	"strings"
)

// base64LineLength is the RFC 2045 limit for an encoded line.
const base64LineLength = 76

// wrapBase64 encodes text and wraps it to the line length MIME requires.
func wrapBase64(text string) string { return wrapBytesBase64([]byte(text)) }

// wrapBytesBase64 encodes bytes and wraps them to the MIME line length.
func wrapBytesBase64(data []byte) string {
	encoded := base64.StdEncoding.EncodeToString(data)

	var out strings.Builder
	for index := 0; index < len(encoded); index += base64LineLength {
		end := index + base64LineLength
		if end > len(encoded) {
			end = len(encoded)
		}
		out.WriteString(encoded[index:end])
		out.WriteString("\r\n")
	}
	return out.String()
}
