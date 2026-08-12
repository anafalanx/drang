package eval

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/anafalanx/drang/internal/value"
)

// Hashing and text-encoding builtins — thin bindings over Go's stdlib. Hashes
// return a lowercase hex digest of the input string's bytes. The from_* decoders
// return a catchable Err on malformed input.

func builtinSha256(args []value.Value) (value.Value, error) {
	s, err := oneString("sha256", args)
	if err != nil {
		return value.MakeNil(), err
	}
	sum := sha256.Sum256([]byte(s))
	return value.MakeStr(hex.EncodeToString(sum[:])), nil
}

func builtinSha1(args []value.Value) (value.Value, error) {
	s, err := oneString("sha1", args)
	if err != nil {
		return value.MakeNil(), err
	}
	sum := sha1.Sum([]byte(s))
	return value.MakeStr(hex.EncodeToString(sum[:])), nil
}

func builtinMd5(args []value.Value) (value.Value, error) {
	s, err := oneString("md5", args)
	if err != nil {
		return value.MakeNil(), err
	}
	sum := md5.Sum([]byte(s))
	return value.MakeStr(hex.EncodeToString(sum[:])), nil
}

func builtinToBase64(args []value.Value) (value.Value, error) {
	s, err := oneString("to_base64", args)
	if err != nil {
		return value.MakeNil(), err
	}
	if int64(base64.StdEncoding.EncodedLen(len(s))) > maxStringBytes {
		return value.MakeErr(fmt.Sprintf("to_base64: result exceeds the %d-byte string limit", maxStringBytes), 1), nil
	}
	return value.MakeStr(base64.StdEncoding.EncodeToString([]byte(s))), nil
}

func builtinFromBase64(args []value.Value) (value.Value, error) {
	s, err := oneString("from_base64", args)
	if err != nil {
		return value.MakeNil(), err
	}
	// DecodedLen is an upper bound (padding and accepted CR/LF make it larger
	// than the real output), so decode through a one-byte-over-limit reader rather
	// than rejecting valid boundary-sized data.
	dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(s))
	b, derr := io.ReadAll(io.LimitReader(dec, maxStringBytes+1))
	if derr != nil {
		return value.MakeErr("from_base64: "+derr.Error(), 1), nil
	}
	if int64(len(b)) > maxStringBytes {
		return value.MakeErr(fmt.Sprintf("from_base64: result exceeds the %d-byte string limit", maxStringBytes), 1), nil
	}
	return value.MakeStr(string(b)), nil
}

func builtinToHex(args []value.Value) (value.Value, error) {
	s, err := oneString("to_hex", args)
	if err != nil {
		return value.MakeNil(), err
	}
	if int64(len(s)) > maxStringBytes/2 {
		return value.MakeErr(fmt.Sprintf("to_hex: result exceeds the %d-byte string limit", maxStringBytes), 1), nil
	}
	return value.MakeStr(hex.EncodeToString([]byte(s))), nil
}

func builtinFromHex(args []value.Value) (value.Value, error) {
	s, err := oneString("from_hex", args)
	if err != nil {
		return value.MakeNil(), err
	}
	if int64(hex.DecodedLen(len(s))) > maxStringBytes {
		return value.MakeErr(fmt.Sprintf("from_hex: result exceeds the %d-byte string limit", maxStringBytes), 1), nil
	}
	b, derr := hex.DecodeString(s)
	if derr != nil {
		return value.MakeErr("from_hex: "+derr.Error(), 1), nil
	}
	return value.MakeStr(string(b)), nil
}

// to_url / from_url are the URL percent-encoding codec, named by direction like
// to_hex/from_hex and to_base64/from_base64 (the from_ side errs on bad input).
func builtinToURL(args []value.Value) (value.Value, error) {
	s, err := oneString("to_url", args)
	if err != nil {
		return value.MakeNil(), err
	}
	encoded := int64(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' || c == ' ' {
			encoded++
		} else {
			encoded += 3
		}
		if encoded > maxStringBytes {
			return value.MakeErr(fmt.Sprintf("to_url: result exceeds the %d-byte string limit", maxStringBytes), 1), nil
		}
	}
	return value.MakeStr(url.QueryEscape(s)), nil
}

func builtinFromURL(args []value.Value) (value.Value, error) {
	s, err := oneString("from_url", args)
	if err != nil {
		return value.MakeNil(), err
	}
	decoded := int64(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' {
			if i+2 >= len(s) {
				break // QueryUnescape below returns the precise malformed-input Err.
			}
			decoded -= 2
			i += 2
		}
	}
	if decoded > maxStringBytes {
		return value.MakeErr(fmt.Sprintf("from_url: result exceeds the %d-byte string limit", maxStringBytes), 1), nil
	}
	d, derr := url.QueryUnescape(s)
	if derr != nil {
		return value.MakeErr("from_url: "+derr.Error(), 1), nil
	}
	return value.MakeStr(d), nil
}
