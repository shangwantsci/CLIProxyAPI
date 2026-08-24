package contentguard

import (
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
)

const clientErrorBody = `{"error":{"message":"Request blocked by content policy.","type":"invalid_request_error","code":"content_policy_violation"}}`

// Intercept scans the latest user turn before credential selection.
// Disabled, parse failures, and panics fail open.
func Intercept(ctx context.Context, cfg *config.SDKConfig, sourceFormat, model, requestedModel string, payload []byte) (errMsg *interfaces.ErrorMessage) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.WithField("recover", recovered).Error("contentguard recovered; failing open")
			errMsg = nil
		}
	}()
	eff := EffectiveFrom(cfg)
	if !eff.Enabled {
		return nil
	}
	text := ExtractLatestUserText(sourceFormat, payload)
	if text == "" {
		return nil
	}
	hit := Evaluate(eff, text, sourceFormat, model)
	if hit == nil {
		return nil
	}
	enforce := eff.Mode == config.ContentGuardModeEnforce
	publishHit(ctx, hit, enforce, model, requestedModel)
	if !enforce {
		return nil
	}
	return &interfaces.ErrorMessage{
		StatusCode:     http.StatusForbidden,
		DirectResponse: true,
		Body:           []byte(clientErrorBody),
		Headers:        http.Header{"Content-Type": []string{"application/json"}},
	}
}
