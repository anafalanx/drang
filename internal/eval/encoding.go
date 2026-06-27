package eval

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"

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
	return value.MakeStr(base64.StdEncoding.EncodeToString([]byte(s))), nil
}

func builtinFromBase64(args []value.Value) (value.Value, error) {
	s, err := oneString("from_base64", args)
	if err != nil {
		return value.MakeNil(), err
	}
	b, derr := base64.StdEncoding.DecodeString(s)
	if derr != nil {
		return value.MakeErr("from_base64: "+derr.Error(), 1), nil
	}
	return value.MakeStr(string(b)), nil
}

func builtinToHex(args []value.Value) (value.Value, error) {
	s, err := oneString("to_hex", args)
	if err != nil {
		return value.MakeNil(), err
	}
	return value.MakeStr(hex.EncodeToString([]byte(s))), nil
}

func builtinFromHex(args []value.Value) (value.Value, error) {
	s, err := oneString("from_hex", args)
	if err != nil {
		return value.MakeNil(), err
	}
	b, derr := hex.DecodeString(s)
	if derr != nil {
		return value.MakeErr("from_hex: "+derr.Error(), 1), nil
	}
	return value.MakeStr(string(b)), nil
}

func builtinURLEncode(args []value.Value) (value.Value, error) {
	s, err := oneString("url_encode", args)
	if err != nil {
		return value.MakeNil(), err
	}
	return value.MakeStr(url.QueryEscape(s)), nil
}

func builtinURLDecode(args []value.Value) (value.Value, error) {
	s, err := oneString("url_decode", args)
	if err != nil {
		return value.MakeNil(), err
	}
	d, derr := url.QueryUnescape(s)
	if derr != nil {
		return value.MakeErr("url_decode: "+derr.Error(), 1), nil
	}
	return value.MakeStr(d), nil
}
