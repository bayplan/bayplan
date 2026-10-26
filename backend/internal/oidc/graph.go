package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

const graphMemberOfURL = "https://graph.microsoft.com/v1.0/me/memberOf?$select=id"

type graphMemberOfResponse struct {
	Value []struct {
		ID string `json:"id"`
	} `json:"value"`
	NextLink string `json:"@odata.nextLink"`
}

// FetchMemberGroups queries Microsoft Graph to obtain the full list of groups
// of the authenticated user, paginating through @odata.nextLink. It is the
// mandatory fallback when the ID token signals group overage
// (IDClaims.GroupsOverflowed) — Entra ID does not include the full list in the
// token in that case. Requires the delegated permission GroupMember.Read.All
// (admin consent) on the app registration.
func FetchMemberGroups(ctx context.Context, httpClient *http.Client, accessToken string) ([]string, error) {
	return fetchMemberGroupsFrom(ctx, httpClient, accessToken, graphMemberOfURL)
}

// fetchMemberGroupsFrom implements the pagination from an explicit starting
// URL, so that following @odata.nextLink can be tested against an
// httptest.Server without depending on the real Graph URL.
func fetchMemberGroupsFrom(ctx context.Context, httpClient *http.Client, accessToken, startURL string) ([]string, error) {
	var groupIDs []string
	url := startURL

	for url != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("oidc: failed building Graph request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("oidc: failed calling Microsoft Graph: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("oidc: Microsoft Graph returned %d", resp.StatusCode)
		}

		var page graphMemberOfResponse
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("oidc: failed decoding Graph response: %w", err)
		}

		for _, v := range page.Value {
			groupIDs = append(groupIDs, v.ID)
		}
		url = page.NextLink
	}

	return groupIDs, nil
}
