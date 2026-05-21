package helps

import (
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

type failingRoundTripper struct {
	err error
}

func (t failingRoundTripper) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, t.err
}

func proxyConfigurationError(proxyURL string, err error) error {
	return fmt.Errorf("invalid proxy-url %s: %w", proxyutil.Redact(proxyURL), err)
}
