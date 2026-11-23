package preview

import (
	"net/http"
	"net/http/cookiejar"
)

// newJar makes a cookie jar. Cookies are per host, not per port, like in a browser.
func newJar() http.CookieJar {
	j, _ := cookiejar.New(nil)
	return j
}
