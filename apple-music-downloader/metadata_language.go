package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
	"github.com/Andrew05213/applemusic_downloader/utils/task"
	"golang.org/x/text/language"
)

type regionalTextQuery struct{ region, locale string }
type nativeTagPolicyKey struct{}
type nativeTagPolicy struct {
	region, manualLocale string
	languages            func() (ampapi.StorefrontLanguages, error)
}

// The account storefront comes from the verified native job credentials, not
// the URL, download setting or the region selector in the renderer.
func withNativeTagPolicy(ctx context.Context, account, region, locale, token string, load func(context.Context, string, string) (ampapi.StorefrontLanguages, error)) context.Context {
	if region == "" {
		region = strings.ToLower(account)
	}
	policy := nativeTagPolicy{region: region, manualLocale: locale}
	policy.languages = sync.OnceValues(func() (ampapi.StorefrontLanguages, error) {
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return load(probe, region, token)
	})
	return context.WithValue(ctx, nativeTagPolicyKey{}, policy)
}

func metadataTagQuery(ctx context.Context, track *task.Track) regionalTextQuery {
	policy, native := ctx.Value(nativeTagPolicyKey{}).(nativeTagPolicy)
	if !native {
		return regionalTextQuery{Config.MetadataRegion, Config.Language}
	}
	if !ampapi.ValidCatalogStorefront(policy.region) {
		return regionalTextQuery{}
	}
	if policy.manualLocale != "" {
		return regionalTextQuery{policy.region, policy.manualLocale}
	}
	preferred, base := parseTagLocale(track.Resp.Attributes.AudioLocale)
	if base == "" {
		return regionalTextQuery{}
	}
	langs, err := policy.languages()
	if err != nil {
		return regionalTextQuery{}
	}
	locale := supportedTagLocale(preferred, base, langs.Supported)
	if locale == "" || (policy.region == track.Storefront && locale == langs.Default) {
		// No supported localization, or the source already uses this default.
		return regionalTextQuery{}
	}
	return regionalTextQuery{policy.region, locale}
}

func parseTagLocale(raw string) (language.Tag, string) {
	if raw == "" || len(raw) > 64 {
		return language.Und, ""
	}
	tag, err := language.Parse(raw)
	if err != nil {
		return language.Und, ""
	}
	base, _, _ := tag.Raw()
	switch code := base.String(); code {
	case "und", "zxx", "mul", "mis":
		return language.Und, ""
	case "cmn", "yue": // Apple storefronts may offer Chinese under zh.
		return tag, "zh"
	default:
		return tag, code
	}
}

func supportedTagLocale(preferred language.Tag, base string, supported []string) string {
	var tags []language.Tag
	var names []string
	for _, name := range supported {
		tag, candidateBase := parseTagLocale(name)
		if candidateBase == base {
			tags = append(tags, tag)
			names = append(names, name)
		}
	}
	if len(tags) == 0 {
		return ""
	}
	_, index, _ := language.NewMatcher(tags).Match(preferred)
	return names[index] // Use the exact locale advertised by Apple.
}
