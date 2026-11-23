package preview

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// Purposes of a token. They are part of what is signed, so a token made for one
// purpose is useless for the other.
const (
	// PurposeLink is the token in the URL that opens a preview. It lives for
	// minutes: it only has to survive the trip from the dashboard to the browser.
	PurposeLink = "link"
	// PurposeSession is what the preview sets as a cookie once the link has been used.
	PurposeSession = "session"
)

// Signer makes and checks short-lived tokens that say "this browser may open
// the preview with this key". Tokens are HMACs over the key, the purpose and an
// expiry, so nothing is stored; a restart of Unire invalidates them all.
type Signer struct {
	secret []byte
	now    func() time.Time
}

// NewSigner makes a Signer with a fresh random secret.
func NewSigner() *Signer {
	s := make([]byte, 32)
	if _, err := rand.Read(s); err != nil {
		panic("preview: no randomness: " + err.Error())
	}
	return &Signer{secret: s, now: time.Now}
}

func (s *Signer) mac(key, purpose string, exp int64) []byte {
	m := hmac.New(sha256.New, s.secret)
	// Length-prefix the key so "a|b" and "a" + "|b" cannot collide.
	m.Write([]byte(strconv.Itoa(len(key)) + ":" + key + "|" + purpose + "|" + strconv.FormatInt(exp, 10)))
	return m.Sum(nil)
}

// Mint returns a token for key and purpose that is valid for ttl.
func (s *Signer) Mint(key, purpose string, ttl time.Duration) string {
	exp := s.now().Add(ttl).Unix()
	return strconv.FormatInt(exp, 10) + "." + base64.RawURLEncoding.EncodeToString(s.mac(key, purpose, exp))
}

// Verify reports whether token is a current token for key and purpose. It also
// returns how long the token has left.
func (s *Signer) Verify(key, purpose, token string) (time.Duration, bool) {
	expStr, sig, ok := strings.Cut(token, ".")
	if !ok {
		return 0, false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return 0, false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, s.mac(key, purpose, exp)) {
		return 0, false
	}
	left := time.Unix(exp, 0).Sub(s.now())
	if left <= 0 {
		return 0, false
	}
	return left, true
}
