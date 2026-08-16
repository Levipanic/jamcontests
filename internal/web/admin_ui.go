package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	adminPageSizeDefault = 50
	adminPageSizeMax     = 200
)

// adminPager carries display-only pagination state. All bounds are enforced on
// the server; a hostile page value cannot escalate anything.
type adminPager struct {
	Page       int
	Per        int
	Total      int
	TotalPages int
	BaseURL    string
}

// adminPageParam parses the ?page= parameter: absent or invalid means the
// first page; values beyond the last page are clamped by SQL LIMIT/OFFSET.
func adminPageParam(c *gin.Context) (int, int) {
	page, err := strconv.Atoi(c.Query("page"))
	if err != nil || page < 1 {
		page = 1
	}
	per, err := strconv.Atoi(c.Query("per"))
	if err != nil || per < 1 {
		per = adminPageSizeDefault
	}
	if per > adminPageSizeMax {
		per = adminPageSizeMax
	}
	if page > 1_000_000 {
		page = 1_000_000
	}
	return page, per
}

// buildAdminPager computes the full pager state for a rendered admin list.
func buildAdminPager(baseURL string, page, per, total int) *adminPager {
	pages := 1
	if per > 0 {
		pages = (total + per - 1) / per
	}
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	if !strings.HasSuffix(baseURL, "?") && !strings.HasSuffix(baseURL, "&") {
		if strings.Contains(baseURL, "?") {
			baseURL += "&"
		} else {
			baseURL += "?"
		}
	}
	return &adminPager{Page: page, Per: per, Total: total, TotalPages: pages, BaseURL: baseURL}
}

// adminPagerBase extracts the current query string without ?page= and leaves a
// trailing separator so the pager can append page=N.
func adminPagerBase(c *gin.Context) string {
	values := url.Values{}
	for key, items := range c.Request.URL.Query() {
		if key == "page" {
			continue
		}
		for _, item := range items {
			values.Add(key, item)
		}
	}
	base := c.Request.URL.Path + "?"
	if encoded := values.Encode(); encoded != "" {
		base += encoded + "&"
	}
	return base
}

// adminOkRedirect redirects with a success flash message displayed by the
// shared alert block. It mirrors the existing ?error= pattern.
func adminOkRedirect(c *gin.Context, path, message string) {
	c.Redirect(http.StatusSeeOther, path+"?ok="+url.QueryEscape(message))
}
