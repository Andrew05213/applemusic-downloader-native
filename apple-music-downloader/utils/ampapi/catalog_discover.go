package ampapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

// Read the editorial snapshot from the public New page. Do not execute scripts,
// follow editorial links or use the credentials from the private catalog.
func (c *Client) browseDiscover(ctx context.Context, region string) (*BrowseResponse, error) {
	client := *c
	if client.WebBaseURL == "" || client.WebBaseURL == defaultWebBaseURL {
		client.WebBaseURL = "https://music.apple.com"
	}
	transport := *c.httpClient()
	transport.Jar = nil
	transport.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.HTTPClient = &transport
	req, err := client.newRequest(ctx, http.MethodGet, client.webURL(region+"/new"), nil)
	if err != nil {
		return nil, ErrBrowseUnavailable
	}
	req.Header.Set("User-Agent", appleMusicUserAgent)
	req.Header.Set("Accept", "text/html")
	resp, err := client.do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrBrowseUnavailable
	}
	defer resp.Body.Close()
	body, err := client.readBodyWithLimit(resp.Body, minPositive(client.responseLimit(), DefaultMaxResponseBytes))
	if err != nil {
		return nil, browseSafeError(ctx, err)
	}
	return decodeDiscoverPage(body, region)
}

var discoverScript = regexp.MustCompile(`(?i)<script\b[^>]*\s+id\s*=\s*["']serialized-server-data["'][^>]*>`)

type discoverLink struct {
	Title string
}

type discoverItem struct {
	Title, Subtitle, ArtistName, Heading, Description string
	TitleLinks, SubtitleLinks                         []discoverLink
	Artwork                                           struct{ Dictionary struct{ URL string } }
	ContentDescriptor                                 struct {
		Kind        string
		Identifiers struct{ StoreAdamID string }
	}
	IsDisabled bool
}

type discoverSection struct {
	ItemKind string
	Header   struct {
		Item struct{ TitleLink discoverLink }
	}
	Items []json.RawMessage
}

func discoverLinkText(links []discoverLink) string {
	var out strings.Builder
	for _, link := range links {
		out.WriteString(link.Title)
	}
	return out.String()
}

func decodeDiscoverPage(body []byte, region string) (*BrowseResponse, error) {
	match := discoverScript.FindIndex(body)
	if match == nil {
		return nil, ErrBrowseSchema
	}
	payload := body[match[1]:]
	end := bytes.Index(payload, []byte("</script>"))
	if end < 0 {
		return nil, ErrBrowseSchema
	}
	var page struct {
		Data []struct {
			Intent struct {
				Kind       string `json:"$kind"`
				Storefront string
			}
			Data struct{ Sections []discoverSection }
		}
	}
	if err := json.Unmarshal(payload[:end], &page); err != nil {
		return nil, ErrBrowseSchema
	}
	for _, entry := range page.Data {
		if entry.Intent.Kind != "BrowsePageIntent" || entry.Intent.Storefront != region || entry.Data.Sections == nil {
			continue
		}
		result := &BrowseResponse{SchemaVersion: 1, Storefront: region, Feed: BrowseModeDiscover, Groups: []BrowseGroup{}}
		for _, section := range entry.Data.Sections {
			group := normalizeDiscoverSection(section, region)
			if len(group.Items) > 0 {
				result.Groups = append(result.Groups, group)
			}
			if len(result.Groups) == 18 {
				break
			}
		}
		return result, nil
	}
	return nil, ErrBrowseSchema
}

func normalizeDiscoverSection(section discoverSection, region string) BrowseGroup {
	group := BrowseGroup{Type: "editorial", Title: browseText(section.Header.Item.TitleLink.Title, 120)}
	switch section.ItemKind {
	case "flowcaseLockup":
		group.Layout = "featured"
		if group.Title == "" {
			group.Title = "编辑精选"
		}
	case "squareLockup":
		group.Layout = "shelf"
	case "trackLockup":
		group.Layout, group.Type = "tracks", BrowseKindSongs
	default:
		return group
	}
	seen := map[string]bool{}
	for _, raw := range section.Items {
		var item discoverItem
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}
		kind := item.ContentDescriptor.Kind
		id := item.ContentDescriptor.Identifiers.StoreAdamID
		key := kind + ":" + id
		typ := map[string]string{"album": BrowseKindAlbums, "playlist": BrowseKindPlaylists, "song": BrowseKindSongs}[kind]
		if item.IsDisabled || typ == "" || !browseID(typ, id) || seen[key] || (group.Layout == "tracks") != (typ == BrowseKindSongs) {
			continue
		}
		title := browseText(item.Title, 512)
		if title == "" {
			title = browseText(discoverLinkText(item.TitleLinks), 512)
		}
		if title == "" {
			continue
		}
		artist := item.ArtistName
		if artist == "" {
			artist = item.Subtitle
		}
		if artist == "" {
			artist = discoverLinkText(item.SubtitleLinks)
		}
		artwork := item.Artwork.Dictionary.URL
		if group.Layout == "featured" {
			artwork = strings.NewReplacer("{w}", "960", "{h}", "540").Replace(artwork)
		}
		group.Items = append(group.Items, BrowseResource{
			ID: id, Type: typ, Title: title, Artist: browseText(artist, 512), Artwork: browseArtwork(artwork),
			URL: "https://music.apple.com/" + region + "/" + kind + "/" + id, Downloadable: true,
			Description: browseDescription(item.Description, ""), EditorialLabel: browseText(item.Heading, 100),
		})
		seen[key] = true
		if len(group.Items) == 20 {
			break
		}
	}
	return group
}
