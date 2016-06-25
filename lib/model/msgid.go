package model

import (
	"github.com/majestrate/srndv2/lib/crypto"

	"crypto/sha1"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

var exp_valid_message_id = regexp.MustCompilePOSIX(`^<[a-zA-Z0-9$.]{2,128}@[a-zA-Z0-9\-.]{2,63}>$`)

type MessageID string

// return true if this message id is well formed, otherwise return false
func (msgid MessageID) Valid() bool {
	return exp_valid_message_id.Copy().MatchString(msgid.String())
}

// get message id as string
func (msgid MessageID) String() string {
	return string(msgid)
}

// compute long form hash of message id
func (msgid MessageID) LongHash() string {
	return fmt.Sprintf("%x", sha1.Sum([]byte(msgid)))
}

// compute truncated form of message id hash
func (msgid MessageID) ShortHash() string {
	return strings.ToLower(msgid.LongHash()[:18])
}

// compute blake2 hash of message id
func (msgid MessageID) Blake2Hash() string {
	h := crypto.Hash()
	io.WriteString(h, msgid.String())
	return strings.ToLower(fmt.Sprintf("%x", h.Sum(nil)))
}

// generate a new message id given name of server
func GenMessageID(name string) MessageID {
	r := crypto.RandBytes(4)
	t := time.Now()
	return MessageID(fmt.Sprintf("<%x$%d@%s>", r, t.Unix(), name))
}