package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode"
)

// BuildACLYql 把观众身份写进 YQL 条件，禁止「先全库检索再在应用层滤权限」。
// 这不是 embedding/ANN；只是带 ACL 的关键词 contains。
func BuildACLYql(query, viewerUserID, viewerEmail string, opts SearchOpts) string {
	clauses := make([]string, 0, 3)
	if acl := yqlACLClause(viewerUserID, viewerEmail, opts.ElevatedMeetingIDs); acl != "" {
		clauses = append(clauses, acl)
	}
	if meeting := yqlInClause("meeting_id", opts.AllowedMeetingIDs); meeting != "" {
		clauses = append(clauses, meeting)
	}
	if text := yqlTextClause(query); text != "" {
		clauses = append(clauses, text)
	}
	where := strings.Join(clauses, " and ")
	if where == "" {
		where = `meeting_id contains "___no_search_clause___"`
	}
	return "select * from doc where " + where
}

func yqlACLClause(userID, email string, elevated []string) string {
	parts := make([]string, 0, 3)
	if id := yqlBare(userID); id != "" {
		parts = append(parts, "allowed_user_ids contains "+yqlQuote(id))
	}
	if mail := yqlBare(strings.ToLower(strings.TrimSpace(email))); mail != "" {
		parts = append(parts, "allowed_guest_emails contains "+yqlQuote(mail))
	}
	if in := yqlInClause("meeting_id", elevated); in != "" {
		parts = append(parts, in)
	}
	if len(parts) == 0 {
		// 没有任何观众身份时不能搜到文档。
		return `meeting_id contains "___no_acl_principal___"`
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "(" + strings.Join(parts, " or ") + ")"
}

func yqlTextClause(query string) string {
	tok := sanitizeQueryToken(query)
	if tok == "" {
		return ""
	}
	q := yqlQuote(tok)
	return "(title contains " + q + " or text contains " + q + ")"
}

func yqlInClause(field string, ids []string) string {
	quoted := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		safe := yqlBare(id)
		if safe == "" {
			continue
		}
		if _, ok := seen[safe]; ok {
			continue
		}
		seen[safe] = struct{}{}
		quoted = append(quoted, yqlQuote(safe))
	}
	if len(quoted) == 0 {
		return ""
	}
	return field + " in (" + strings.Join(quoted, ", ") + ")"
}

func yqlQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func yqlBare(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' || r == '@' || r == '+' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sanitizeQueryToken(query string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(query) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Han, r) || r == '-' || r == '_' || r == ' ' {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

type vespaSearchRequest struct {
	YQL  string `json:"yql"`
	Hits int    `json:"hits"`
}

type vespaSearchResponse struct {
	Root struct {
		Children []struct {
			Fields vespaDocFields `json:"fields"`
		} `json:"children"`
	} `json:"root"`
}

type vespaDocFields struct {
	MeetingID          string   `json:"meeting_id"`
	Title              string   `json:"title"`
	Text               string   `json:"text"`
	SourceType         string   `json:"source_type"`
	SourceID           string   `json:"source_id"`
	AllowedUserIDs     []string `json:"allowed_user_ids"`
	AllowedGuestEmails []string `json:"allowed_guest_emails"`
}

func (v *VespaClient) searchYQL(ctx context.Context, yql string) ([]SearchHit, error) {
	payload, err := json.Marshal(vespaSearchRequest{YQL: yql, Hits: 50})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.base+"/search/", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vespa search: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("vespa search response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("vespa search status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return parseVespaSearchHits(body)
}

func parseVespaSearchHits(body []byte) ([]SearchHit, error) {
	var parsed vespaSearchResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("vespa search json: %w", err)
	}
	out := make([]SearchHit, 0, len(parsed.Root.Children))
	for _, child := range parsed.Root.Children {
		f := child.Fields
		if strings.TrimSpace(f.MeetingID) == "" {
			continue
		}
		doc := Document{
			MeetingID:          f.MeetingID,
			Title:              f.Title,
			Text:               f.Text,
			SourceType:         f.SourceType,
			SourceID:           f.SourceID,
			AllowedUserIDs:     f.AllowedUserIDs,
			AllowedGuestEmails: f.AllowedGuestEmails,
		}
		snippet := doc.Text
		if len(snippet) > 160 {
			snippet = snippet[:160] + "…"
		}
		out = append(out, SearchHit{Document: doc, Snippet: snippet})
	}
	return out, nil
}

func applySearchACL(hits []SearchHit, viewerUserID, viewerEmail string, opts SearchOpts) []SearchHit {
	allow := map[string]struct{}{}
	for _, id := range opts.AllowedMeetingIDs {
		allow[id] = struct{}{}
	}
	elevated := map[string]struct{}{}
	for _, id := range opts.ElevatedMeetingIDs {
		elevated[id] = struct{}{}
	}
	out := make([]SearchHit, 0, len(hits))
	for _, hit := range hits {
		if len(allow) > 0 {
			if _, ok := allow[hit.Document.MeetingID]; !ok {
				continue
			}
		}
		_, elev := elevated[hit.Document.MeetingID]
		if !elev && !viewerAllowed(hit.Document, viewerUserID, viewerEmail) {
			continue
		}
		out = append(out, hit)
	}
	return out
}
