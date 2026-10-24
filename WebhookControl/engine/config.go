// Package engine validates immutable configuration and evaluates bounded,
// deterministic CEL rules and explicit JSON field mappings.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
)

const (
	MaxConfigBytes      = 256 << 10
	MaxResources        = 32 // Per resource kind.
	MaxExpressions      = 256
	MaxExpressionBytes  = 4096
	MaxTemplateFields   = 32
	MaxFanout           = 128
	MaxRenderedBytes    = 64 << 10 // Per matched route.
	MaxTotalRenderBytes = 1 << 20
	MaxExpressionCost   = 5000
	MaxEvaluationCost   = 50000
	MaxEventBytes       = 1 << 20
	MaxJSONDepth        = 64
	MaxJSONNodes        = 65536
)

var (
	ErrConfiguration = errors.New("invalid configuration")
	ErrEvaluation    = errors.New("rule or template evaluation failed")
	ErrLimit         = errors.New("evaluation limit exceeded")
	ErrEvent         = errors.New("event must be a valid JSON object")
)

func ValidID(id domain.ID) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func validRevision(revision domain.Revision) bool { return revision > 0 && revision <= math.MaxInt64 }
func validRef(ref domain.RevisionRef) bool        { return ValidID(ref.ID) && validRevision(ref.Revision) }
func validSecret(ref domain.SecretRef) bool {
	return ValidID(domain.ID(ref.Name)) && ValidID(domain.ID(ref.Version))
}

func validTTL(ttl domain.Retention) bool {
	return ttl >= -1 && ttl <= domain.Retention(math.MaxInt64/int64(time.Second))
}

// ValidateSource checks the source envelope before authentication. Full resource
// validation happens only after authentication and deduplication lookup.
func ValidateSource(source domain.Source) error {
	if !ValidID(source.ID) || !validRevision(source.Revision) || len(source.Name) > 256 || !validTTL(source.OriginalTTL) || !validTTL(source.DeliveryTTL) || !validSecret(source.Auth.Secret) {
		return ErrConfiguration
	}
	if source.Auth.Kind != domain.AuthBearer && source.Auth.Kind != domain.AuthHMAC {
		return ErrConfiguration
	}
	return nil
}

func validateHTTP(config domain.HTTPConfig) error {
	if len(config.URL) == 0 || len(config.URL) > 2048 || config.TimeoutSeconds < 1 || config.TimeoutSeconds > 60 || len(config.Headers)+len(config.SecretHeaders) > 32 {
		return ErrConfiguration
	}
	u, err := url.Parse(config.URL)
	if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return ErrConfiguration
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return ErrConfiguration
		}
	}
	seen := map[string]bool{}
	for key, value := range config.Headers {
		canonical := http.CanonicalHeaderKey(key)
		// Restrict literal headers to known noncredential settings. Other headers,
		// including Authorization, must use server-owned versioned secret refs.
		if canonical != "Accept" && canonical != "Content-Type" && canonical != "User-Agent" {
			return ErrConfiguration
		}
		if seen[canonical] || !headerValue(value) || canonical == "Content-Type" && value != "application/json" {
			return ErrConfiguration
		}
		seen[canonical] = true
	}
	for key, ref := range config.SecretHeaders {
		canonical := http.CanonicalHeaderKey(key)
		if !headerName(key) || seen[canonical] || !validSecret(ref) {
			return ErrConfiguration
		}
		switch canonical {
		case "Host", "Content-Length", "Transfer-Encoding", "Connection", "Keep-Alive", "Trailer", "Upgrade", "Te", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection", "Content-Type", "Content-Encoding", "Idempotency-Key":
			return ErrConfiguration
		}
		seen[canonical] = true
	}
	return nil
}

func headerName(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return false
		}
	}
	return true
}

func headerValue(value string) bool {
	if len(value) > 1024 {
		return false
	}
	for _, c := range value {
		if c < 32 || c > 126 {
			return false
		}
	}
	return true
}

// copyConfiguration bounds and copies all maps/slices before they can become a
// compiled plan or frozen delivery. Callers may subsequently change their draft.
func copyConfiguration(config domain.Configuration) (domain.Configuration, error) {
	if ValidateSource(config.Source) != nil || len(config.Rules) > MaxResources || len(config.Templates) > MaxResources || len(config.Routes) > MaxResources || len(config.Transporters) > MaxResources {
		return domain.Configuration{}, ErrConfiguration
	}
	expressions, fanout := len(config.Rules), 0
	for _, rule := range config.Rules {
		if !validRef(domain.RevisionRef{ID: rule.ID, Revision: rule.Revision}) || rule.SourceID != config.Source.ID || len(rule.Expression) == 0 || len(rule.Expression) > MaxExpressionBytes {
			return domain.Configuration{}, ErrConfiguration
		}
	}
	for _, template := range config.Templates {
		if !validRef(domain.RevisionRef{ID: template.ID, Revision: template.Revision}) || template.SourceID != config.Source.ID || len(template.Fields) > MaxTemplateFields {
			return domain.Configuration{}, ErrConfiguration
		}
		expressions += len(template.Fields)
		for field, expression := range template.Fields {
			if len(field) == 0 || len(field) > 128 || len(expression) == 0 || len(expression) > MaxExpressionBytes {
				return domain.Configuration{}, ErrConfiguration
			}
		}
	}
	for _, route := range config.Routes {
		if !validRef(domain.RevisionRef{ID: route.ID, Revision: route.Revision}) || route.SourceID != config.Source.ID || !validRef(route.Template) || len(route.Rules) > MaxResources || len(route.Transporters) == 0 || len(route.Transporters) > 16 {
			return domain.Configuration{}, ErrConfiguration
		}
		for _, ref := range route.Rules {
			if !validRef(ref) {
				return domain.Configuration{}, ErrConfiguration
			}
		}
		for _, ref := range route.Transporters {
			if !validRef(ref) {
				return domain.Configuration{}, ErrConfiguration
			}
		}
		fanout += len(route.Transporters)
	}
	if expressions > MaxExpressions || fanout > MaxFanout {
		return domain.Configuration{}, ErrConfiguration
	}
	for _, transporter := range config.Transporters {
		if !validRef(domain.RevisionRef{ID: transporter.ID, Revision: transporter.Revision}) || transporter.SourceID != config.Source.ID || transporter.Kind != domain.TransporterHTTP || validateHTTP(transporter.HTTP) != nil {
			return domain.Configuration{}, ErrConfiguration
		}
	}
	data, err := json.Marshal(config)
	if err != nil || len(data) > MaxConfigBytes {
		return domain.Configuration{}, ErrConfiguration
	}
	var copied domain.Configuration
	if err := json.Unmarshal(data, &copied); err != nil {
		return domain.Configuration{}, ErrConfiguration
	}
	return copied, nil
}

func checkPolicy(ctx context.Context, policy ports.DestinationPolicy, sourceID domain.ID, transporter domain.Transporter) error {
	if policy == nil {
		return ports.ErrPolicyDenied
	}
	if err := policy.ApproveDestination(ctx, sourceID, transporter.HTTP); err != nil {
		if errors.Is(err, ports.ErrPolicyDenied) {
			return ports.ErrPolicyDenied
		}
		return ErrConfiguration
	}
	return nil
}
